package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/admit"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

// Headers understood by the localize route.
const (
	DeadlineHeader   = "X-Deadline-Ms" // remaining time budget in ms
	IntrinsicsHeader = "X-Intrinsics"  // "fx,fy,cx,cy"
)

// Pose is camera-to-world: q rotates camera axes into map axes, t is the camera center.
type Pose struct {
	Q Quat `json:"q"`
	T Vec3 `json:"t"`
}

// Quat is a unit quaternion, scalar first.
type Quat struct {
	W float64 `json:"w"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Vec3 is a position in map coordinates, meters.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Timings are in milliseconds. decode..pose come from the core; queue is admission
// wait; core is their sum; worker is the whole cgo call as seen from Go; service is the
// handler's wall time up to encoding the response; overhead is service minus core.
type Timings struct {
	Queue    float64 `json:"queue"`
	Decode   float64 `json:"decode"`
	Extract  float64 `json:"extract"`
	Match    float64 `json:"match"`
	Pose     float64 `json:"pose"`
	Core     float64 `json:"core"`
	Worker   float64 `json:"worker"`
	Service  float64 `json:"service"`
	Overhead float64 `json:"overhead"`
}

// LocalizeResponse is the 200 body. OK=false means the core ran but found no pose.
type LocalizeResponse struct {
	RequestID    string  `json:"request_id"`
	Map          string  `json:"map"`
	MapVersion   int64   `json:"map_version"`
	OK           bool    `json:"ok"`
	Reason       string  `json:"reason,omitempty"`
	Pose         *Pose   `json:"pose,omitempty"`
	NumKeypoints int     `json:"num_keypoints"`
	NumMatches   int     `json:"num_matches"`
	NumInliers   int     `json:"num_inliers"`
	ImageWidth   int     `json:"image_width"`
	ImageHeight  int     `json:"image_height"`
	TimingsMs    Timings `json:"timings_ms"`
}

type apiErr struct {
	status int
	code   string
	msg    string
}

func (e *apiErr) Error() string { return e.msg }

func bad(status int, code, f string, a ...any) *apiErr {
	return &apiErr{status, code, fmt.Sprintf(f, a...)}
}

func (s *Server) deadline(r *http.Request) (time.Duration, *apiErr) {
	v := r.Header.Get(DeadlineHeader)
	if v == "" {
		v = r.URL.Query().Get("deadline_ms")
	}
	if v == "" {
		return s.cfg.DefaultDeadline, nil
	}
	ms, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(ms) || ms <= 0 {
		return 0, bad(http.StatusBadRequest, CodeBadDeadline, "deadline must be a positive number of milliseconds, got %q", v)
	}
	d := time.Duration(ms * float64(time.Millisecond))
	return min(d, s.cfg.MaxDeadline), nil
}

func parseIntrinsics(r *http.Request) (vloc.Intrinsics, *apiErr) {
	var vals [4]string
	if h := r.Header.Get(IntrinsicsHeader); h != "" {
		parts := strings.Split(h, ",")
		if len(parts) != 4 {
			return vloc.Intrinsics{}, bad(http.StatusBadRequest, CodeBadIntrinsics, "%s wants 4 comma separated numbers fx,fy,cx,cy", IntrinsicsHeader)
		}
		copy(vals[:], parts)
	} else {
		q := r.URL.Query()
		for i, k := range []string{"fx", "fy", "cx", "cy"} {
			vals[i] = q.Get(k)
		}
	}
	var f [4]float64
	names := [4]string{"fx", "fy", "cx", "cy"}
	for i, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			return vloc.Intrinsics{}, bad(http.StatusBadRequest, CodeMissingIntrinsics, "intrinsics required: query fx,fy,cx,cy or header %s (missing %s)", IntrinsicsHeader, names[i])
		}
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return vloc.Intrinsics{}, bad(http.StatusBadRequest, CodeBadIntrinsics, "%s is not a number: %q", names[i], v)
		}
		f[i] = x
	}
	return vloc.Intrinsics{Fx: f[0], Fy: f[1], Cx: f[2], Cy: f[3]}, nil
}

// readImage enforces the size limit, sniffs the type and reads the dimensions from the
// header without decoding pixels.
func (s *Server) readImage(w http.ResponseWriter, r *http.Request) ([]byte, image.Config, *apiErr) {
	if r.ContentLength > s.cfg.MaxImageBytes {
		return nil, image.Config{}, bad(http.StatusRequestEntityTooLarge, CodeImageTooLarge, "image is %d bytes, limit %d", r.ContentLength, s.cfg.MaxImageBytes)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxImageBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, image.Config{}, bad(http.StatusRequestEntityTooLarge, CodeImageTooLarge, "image exceeds limit %d bytes", s.cfg.MaxImageBytes)
		}
		return nil, image.Config{}, bad(http.StatusBadRequest, CodeBadImage, "reading body: %v", err)
	}
	if len(body) == 0 {
		return nil, image.Config{}, bad(http.StatusBadRequest, CodeEmptyBody, "request body must be a JPEG or PNG image")
	}
	ct := http.DetectContentType(body)
	if ct != "image/jpeg" && ct != "image/png" {
		return nil, image.Config{}, bad(http.StatusUnsupportedMediaType, CodeUnsupportedMedia, "body sniffed as %s, only image/jpeg and image/png are accepted", ct)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, image.Config{}, bad(http.StatusBadRequest, CodeBadImage, "cannot read %s header: %v", ct, err)
	}
	if cfg.Width < s.cfg.MinImageDim || cfg.Height < s.cfg.MinImageDim || cfg.Width > s.cfg.MaxImageDim || cfg.Height > s.cfg.MaxImageDim {
		return nil, image.Config{}, bad(http.StatusUnprocessableEntity, CodeBadImageSize, "image is %dx%d, each side must be in [%d, %d]", cfg.Width, cfg.Height, s.cfg.MinImageDim, s.cfg.MaxImageDim)
	}
	return body, cfg, nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (s *Server) localize(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	name := r.PathValue("name")
	annotate(w, slog.String("map", name))
	fail := func(e *apiErr) { writeErr(w, r, e.status, e.code, e.msg) }
	if !nameRe.MatchString(name) {
		fail(bad(http.StatusBadRequest, CodeBadName, "map name must match %s", nameRe))
		return
	}
	budget, aerr := s.deadline(r)
	if aerr != nil {
		fail(aerr)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()

	_, vspan := s.tracer.Start(ctx, "validate")
	img, icfg, aerr := s.readImage(w, r)
	var k vloc.Intrinsics
	if aerr == nil {
		k, aerr = parseIntrinsics(r)
	}
	if aerr == nil {
		if err := k.Validate(icfg.Width, icfg.Height); err != nil {
			aerr = bad(http.StatusUnprocessableEntity, CodeBadIntrinsics, "%v", err)
		}
	}
	vspan.End()
	if aerr != nil {
		fail(aerr)
		return
	}

	h, err := s.Maps.Acquire(name)
	if err != nil {
		fail(bad(http.StatusNotFound, CodeMapNotFound, "no map named %s; GET /v1/maps lists loaded maps", name))
		return
	}
	defer h.Release()

	var res vloc.Result
	var coreErr error
	enq := time.Now()
	st, err := s.Pool.Do(ctx, func() { res, coreErr = h.Map.Localize(img, k) })
	s.m.Stages.WithLabelValues("queue").Observe(st.Queued.Seconds())
	if err != nil {
		s.reject(w, r, err)
		return
	}
	s.stageSpans(ctx, enq, st, res)
	if coreErr != nil {
		fail(bad(http.StatusInternalServerError, CodeCoreError, "core: %v", coreErr))
		return
	}

	out := LocalizeResponse{
		RequestID: RequestID(r.Context()), Map: name, MapVersion: h.Version, OK: res.OK,
		NumKeypoints: res.NumKeypoints, NumMatches: res.NumMatches, NumInliers: res.NumInliers,
		ImageWidth: icfg.Width, ImageHeight: icfg.Height,
	}
	if res.OK {
		p := &Pose{}
		p.Q.W, p.Q.X, p.Q.Y, p.Q.Z = res.Qw, res.Qx, res.Qy, res.Qz
		p.T.X, p.T.Y, p.T.Z = res.Tx, res.Ty, res.Tz
		out.Pose = p
		s.m.Inliers.Observe(float64(res.NumInliers))
	} else {
		out.Reason = res.Err
		setOutcome(w, "no_pose")
	}
	core := res.CoreMs()
	svc := ms(time.Since(start))
	out.TimingsMs = Timings{Queue: ms(st.Queued), Decode: res.MsDecode, Extract: res.MsExtract, Match: res.MsMatch,
		Pose: res.MsPose, Core: core, Worker: ms(st.Run), Service: svc, Overhead: svc - core}
	for stage, v := range map[string]float64{"decode": res.MsDecode, "extract": res.MsExtract, "match": res.MsMatch,
		"pose": res.MsPose, "core": core, "overhead": svc - core} {
		s.m.Stages.WithLabelValues(stage).Observe(v / 1000)
	}
	annotate(w, slog.Bool("pose_ok", res.OK), slog.Int("inliers", res.NumInliers),
		slog.Float64("queue_ms", out.TimingsMs.Queue), slog.Float64("core_ms", core))
	w.Header().Set("Server-Timing", fmt.Sprintf("queue;dur=%.3f, core;dur=%.3f, service;dur=%.3f", out.TimingsMs.Queue, core, svc))
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request, err error) {
	var rej *admit.Rejection
	if !errors.As(err, &rej) {
		writeErr(w, r, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	reason := map[error]string{admit.ErrQueueFull: "queue_full", admit.ErrWouldMiss: "would_miss_deadline",
		admit.ErrExpired: "expired_in_queue", admit.ErrClosed: "shutting_down"}[rej.Err]
	s.m.Shed.WithLabelValues(reason).Inc()
	annotate(w, slog.String("shed_reason", reason), slog.Float64("est_wait_ms", ms(rej.EstWait)))
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(rej.RetryAfter.Seconds()))))
	switch rej.Err {
	case admit.ErrExpired:
		writeErr(w, r, http.StatusGatewayTimeout, CodeDeadlineExceeded, "deadline passed while queued")
	case admit.ErrClosed:
		writeErr(w, r, http.StatusServiceUnavailable, CodeShuttingDown, "server is shutting down")
	default:
		writeErr(w, r, http.StatusServiceUnavailable, CodeOverloaded,
			fmt.Sprintf("overloaded (%s): estimated queue wait %.0f ms", reason, ms(rej.EstWait)))
	}
}

// stageSpans records the queue wait and the core's four stages as child spans. The core
// reports durations, not timestamps, so the stage spans are laid end to end from the
// moment a worker picked the job up.
func (s *Server) stageSpans(ctx context.Context, enq time.Time, st admit.Stats, res vloc.Result) {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return
	}
	picked := enq.Add(st.Queued)
	_, q := s.tracer.Start(ctx, "queue", trace.WithTimestamp(enq))
	q.End(trace.WithTimestamp(picked))
	lctx, l := s.tracer.Start(ctx, "core.localize", trace.WithTimestamp(picked),
		trace.WithAttributes(attribute.Bool("pose.ok", res.OK), attribute.Int("inliers", res.NumInliers),
			attribute.Int("keypoints", res.NumKeypoints), attribute.Int("matches", res.NumMatches)))
	t := picked
	for _, sg := range []struct {
		n  string
		ms float64
	}{{"decode", res.MsDecode}, {"extract", res.MsExtract}, {"match", res.MsMatch}, {"pose", res.MsPose}} {
		end := t.Add(time.Duration(sg.ms * float64(time.Millisecond)))
		_, sp := s.tracer.Start(lctx, sg.n, trace.WithTimestamp(t))
		sp.End(trace.WithTimestamp(end))
		t = end
	}
	l.End(trace.WithTimestamp(picked.Add(st.Run)))
}
