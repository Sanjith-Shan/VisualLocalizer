package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Sanjith-Shan/VisualLocalizer/service/gen/vlocpb"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/admit"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

type grpcServer struct {
	vlocpb.UnimplementedLocalizerServer
	s *Server
}

// NewGRPC returns a gRPC server exposing the Localizer service on s's engine, maps and
// pool. Its own deadline is the standard gRPC deadline (DefaultDeadline when none).
func (s *Server) NewGRPC() *grpc.Server {
	g := grpc.NewServer(grpc.MaxRecvMsgSize(int(s.cfg.MaxImageBytes)+4096), grpc.UnaryInterceptor(s.grpcInterceptor))
	vlocpb.RegisterLocalizerServer(g, &grpcServer{s: s})
	return g
}

func (s *Server) grpcInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	start := time.Now()
	id := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok && len(md.Get("x-request-id")) > 0 {
		id = md.Get("x-request-id")[0]
	}
	if !reqIDRe.MatchString(id) {
		id = newRequestID()
	}
	grpc.SetHeader(ctx, metadata.Pairs("x-request-id", id))
	ctx = context.WithValue(ctx, reqIDKey, id)
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.DefaultDeadline)
		defer cancel()
	}
	ctx, span := s.tracer.Start(ctx, "grpc "+info.FullMethod, trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("request.id", id)))
	resp, err := h(ctx, req)
	code := status.Code(err)
	outcome := "ok"
	switch code {
	case codes.OK:
		if r, ok := resp.(*vlocpb.LocalizeResponse); ok && !r.Ok {
			outcome = "no_pose"
		}
	case codes.ResourceExhausted, codes.Unavailable:
		outcome = "shed"
	case codes.DeadlineExceeded:
		outcome = "timeout"
	case codes.Internal, codes.Unknown:
		outcome = "error"
	default:
		outcome = "client_error"
	}
	span.SetAttributes(attribute.String("rpc.grpc.status_code", code.String()), attribute.String("outcome", outcome))
	span.End()
	d := time.Since(start)
	s.m.Requests.WithLabelValues("grpc "+info.FullMethod, outcome).Observe(d.Seconds())
	s.log.LogAttrs(ctx, slog.LevelInfo, "request", slog.String("request_id", id), slog.String("route", "grpc "+info.FullMethod),
		slog.String("grpc_code", code.String()), slog.String("outcome", outcome), slog.Float64("dur_ms", ms(d)))
	return resp, err
}

var httpToGRPC = map[int]codes.Code{
	http.StatusBadRequest: codes.InvalidArgument, http.StatusUnprocessableEntity: codes.InvalidArgument,
	http.StatusUnsupportedMediaType: codes.InvalidArgument, http.StatusRequestEntityTooLarge: codes.InvalidArgument,
	http.StatusNotFound: codes.NotFound, http.StatusInternalServerError: codes.Internal,
}

func (g *grpcServer) Localize(ctx context.Context, req *vlocpb.LocalizeRequest) (*vlocpb.LocalizeResponse, error) {
	s := g.s
	start := time.Now()
	if !nameRe.MatchString(req.GetMap()) {
		return nil, status.Errorf(codes.InvalidArgument, "%s: map name must match %s", CodeBadName, nameRe)
	}
	icfg, aerr := s.checkImage(req.GetImage())
	if aerr != nil {
		return nil, status.Errorf(httpToGRPC[aerr.status], "%s: %s", aerr.code, aerr.msg)
	}
	ki := req.GetIntrinsics()
	if ki == nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: intrinsics required", CodeMissingIntrinsics)
	}
	k := vloc.Intrinsics{Fx: ki.Fx, Fy: ki.Fy, Cx: ki.Cx, Cy: ki.Cy}
	if err := k.Validate(icfg.Width, icfg.Height); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: %v", CodeBadIntrinsics, err)
	}
	out, err := s.run(ctx, start, req.GetMap(), req.GetImage(), icfg, k)
	if err != nil {
		var ae *apiErr
		if errors.As(err, &ae) {
			return nil, status.Errorf(httpToGRPC[ae.status], "%s: %s", ae.code, ae.msg)
		}
		var rej *admit.Rejection
		if errors.As(err, &rej) {
			s.m.Shed.WithLabelValues(shedReason(rej.Err)).Inc()
			switch rej.Err {
			case admit.ErrExpired:
				return nil, status.Error(codes.DeadlineExceeded, CodeDeadlineExceeded+": deadline passed while queued")
			case admit.ErrClosed:
				return nil, status.Error(codes.Unavailable, CodeShuttingDown)
			}
			return nil, status.Errorf(codes.ResourceExhausted, "%s: %s, estimated queue wait %.0f ms", CodeOverloaded, shedReason(rej.Err), ms(rej.EstWait))
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	r := &vlocpb.LocalizeResponse{
		RequestId: out.RequestID, MapVersion: out.MapVersion, Ok: out.OK, Reason: out.Reason,
		NumKeypoints: int32(out.NumKeypoints), NumMatches: int32(out.NumMatches), NumInliers: int32(out.NumInliers),
		Timings: &vlocpb.Timings{QueueMs: out.TimingsMs.Queue, DecodeMs: out.TimingsMs.Decode, ExtractMs: out.TimingsMs.Extract,
			MatchMs: out.TimingsMs.Match, PoseMs: out.TimingsMs.Pose, CoreMs: out.TimingsMs.Core, ServiceMs: out.TimingsMs.Service},
	}
	if p := out.Pose; p != nil {
		r.Pose = &vlocpb.Pose{Qw: p.Q.W, Qx: p.Q.X, Qy: p.Q.Y, Qz: p.Q.Z, Tx: p.T.X, Ty: p.T.Y, Tz: p.T.Z}
	}
	return r, nil
}

func (g *grpcServer) ListMaps(ctx context.Context, _ *vlocpb.ListMapsRequest) (*vlocpb.ListMapsResponse, error) {
	var out vlocpb.ListMapsResponse
	for _, m := range g.s.Maps.List() {
		out.Maps = append(out.Maps, &vlocpb.MapEntry{Name: m.Name, Version: m.Version, Bytes: m.Bytes,
			NumPoints: int32(m.Map.NumPoints), Feature: m.Map.Feature})
	}
	return &out, nil
}
