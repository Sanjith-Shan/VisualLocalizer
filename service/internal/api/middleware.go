package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type ctxKey int

const reqIDKey ctxKey = 0

// RequestIDHeader is echoed on every response; a valid inbound value is kept.
const RequestIDHeader = "X-Request-Id"

var reqIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID returns the request id stored in ctx.
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(reqIDKey).(string)
	return s
}

func newRequestID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	code    string // error code, if any
	outcome string // set by handlers that know better than the status
	attrs   []slog.Attr
}

func (w *statusWriter) WriteHeader(s int) {
	if w.status == 0 {
		w.status = s
	}
	w.ResponseWriter.WriteHeader(s)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func outcomeFor(status int) string {
	switch {
	case status < 400:
		return "ok"
	case status == http.StatusServiceUnavailable:
		return "shed"
	case status == http.StatusGatewayTimeout:
		return "timeout"
	case status < 500:
		return "client_error"
	default:
		return "error"
	}
}

// instrument wraps the mux: request id, root span, latency metric, one log line.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get(RequestIDHeader)
		if !reqIDRe.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := context.WithValue(r.Context(), reqIDKey, id)
		ctx, span := s.tracer.Start(ctx, r.Method+" "+r.URL.Path, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("request.id", id)))
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(ctx)
		next.ServeHTTP(sw, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		span.SetName(route)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		outcome := sw.outcome
		if outcome == "" {
			outcome = outcomeFor(sw.status)
		}
		span.SetAttributes(attribute.Int("http.status_code", sw.status), attribute.String("outcome", outcome))
		if sw.status >= 500 {
			span.SetStatus(codes.Error, sw.code)
		}
		span.End()
		d := time.Since(start)
		if route != "GET /metrics" {
			s.m.Requests.WithLabelValues(route, outcome).Observe(d.Seconds())
		}
		lvl := slog.LevelInfo
		if sw.status >= 500 && outcome != "shed" {
			lvl = slog.LevelError
		}
		attrs := append([]slog.Attr{
			slog.String("request_id", id), slog.String("route", route), slog.Int("status", sw.status),
			slog.String("outcome", outcome), slog.Float64("dur_ms", float64(d.Microseconds())/1000),
		}, sw.attrs...)
		if sw.code != "" {
			attrs = append(attrs, slog.String("error_code", sw.code))
		}
		s.log.LogAttrs(ctx, lvl, "request", attrs...)
	})
}

func annotate(w http.ResponseWriter, a ...slog.Attr) {
	if sw, ok := w.(*statusWriter); ok {
		sw.attrs = append(sw.attrs, a...)
	}
}

func setOutcome(w http.ResponseWriter, o string) {
	if sw, ok := w.(*statusWriter); ok {
		sw.outcome = o
	}
}
