// Package api is the HTTP front of the positioning service.
package api

import (
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/admit"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/maps"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/obs"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

// Config holds the service limits. Zero values take the defaults below.
type Config struct {
	MaxImageBytes   int64         // default 8 MiB
	MaxMapBytes     int64         // default 2 GiB
	MinImageDim     int           // default 32
	MaxImageDim     int           // default 4096
	DefaultDeadline time.Duration // default 2s, used when the client sends none
	MaxDeadline     time.Duration // default 30s
	MapDir          string        // where ingested maps are stored; required for PUT
}

func (c *Config) defaults() {
	if c.MaxImageBytes <= 0 {
		c.MaxImageBytes = 8 << 20
	}
	if c.MaxMapBytes <= 0 {
		c.MaxMapBytes = 2 << 30
	}
	if c.MinImageDim <= 0 {
		c.MinImageDim = 32
	}
	if c.MaxImageDim <= 0 {
		c.MaxImageDim = 4096
	}
	if c.DefaultDeadline <= 0 {
		c.DefaultDeadline = 2 * time.Second
	}
	if c.MaxDeadline <= 0 {
		c.MaxDeadline = 30 * time.Second
	}
}

// Server wires the engine, the map registry, the worker pool and observability.
type Server struct {
	cfg      Config
	engine   vloc.Engine
	Maps     *maps.Registry
	Pool     *admit.Pool
	m        *obs.Metrics
	tracer   trace.Tracer
	log      *slog.Logger
	draining atomic.Bool
	ingestMu sync.Map // name -> *sync.Mutex, serializes uploads of one name
	handler  http.Handler
}

// Deps are the collaborators a Server needs. Tracer and Log may be nil.
type Deps struct {
	Engine vloc.Engine
	Pool   *admit.Pool
	Tracer trace.Tracer
	Log    *slog.Logger
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func New(cfg Config, d Deps) *Server {
	cfg.defaults()
	s := &Server{cfg: cfg, engine: d.Engine, Maps: maps.NewRegistry(), Pool: d.Pool, tracer: d.Tracer, log: d.Log}
	if s.tracer == nil {
		s.tracer = noop.NewTracerProvider().Tracer("vlocd")
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.m = obs.NewMetrics(obs.Gauges{
		QueueDepth:         func() float64 { return float64(s.Pool.QueueLen()) },
		Inflight:           func() float64 { return float64(s.Pool.Inflight()) },
		MapsLoaded:         func() float64 { return float64(s.Maps.Len()) },
		ServiceEstimateSec: func() float64 { return s.Pool.ServiceEstimate().Seconds() },
	})
	s.Maps.OnFree = func(h *maps.Handle) {
		s.m.MapEvents.WithLabelValues(h.Name, "freed").Inc()
		s.log.Info("map freed", "map", h.Name, "version", h.Version)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/maps/{name}/localize", s.localize)
	mux.HandleFunc("PUT /v1/maps/{name}", s.putMap)
	mux.HandleFunc("DELETE /v1/maps/{name}", s.deleteMap)
	mux.HandleFunc("GET /v1/maps", s.listMaps)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.m.Reg, promhttp.HandlerOpts{}))
	s.handler = s.instrument(mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Metrics exposes the metric set, for tests.
func (s *Server) Metrics() *obs.Metrics { return s.m }

// Drain makes /readyz fail so a load balancer stops sending new traffic.
func (s *Server) Drain() { s.draining.Store(true) }

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.draining.Load():
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
	case s.Maps.Len() == 0:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "no maps loaded"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "maps": s.Maps.Len(), "engine": s.engine.Name()})
	}
}

func (s *Server) listMaps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"maps": s.Maps.List()})
}

func (s *Server) deleteMap(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, r, http.StatusBadRequest, CodeBadName, "map name must match "+nameRe.String())
		return
	}
	if !s.Maps.Delete(name) {
		writeErr(w, r, http.StatusNotFound, CodeMapNotFound, "no map named "+name)
		return
	}
	s.m.MapEvents.WithLabelValues(name, "deleted").Inc()
	if s.cfg.MapDir != "" {
		os.Remove(s.MapPath(name))
	}
	w.WriteHeader(http.StatusNoContent)
}
