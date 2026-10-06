// Command vlocd is the positioning service: HTTP in front of the localization core.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/admit"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/api"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/obs"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

type mapFlags []string

func (m *mapFlags) String() string     { return strings.Join(*m, ",") }
func (m *mapFlags) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var (
		addr       = flag.String("addr", ":8080", "listen address")
		grpcAddr   = flag.String("grpc-addr", "", "gRPC listen address (off when empty)")
		engineName = flag.String("engine", "auto", "auto, cgo or fake (auto = cgo when built with -tags vloc)")
		fakeWork   = flag.Duration("fake-work", 20*time.Millisecond, "CPU time the fake engine burns per request")
		mapDir     = flag.String("map-dir", "maps", "directory for ingested maps; every *.vmap in it loads at startup")
		workers    = flag.Int("workers", 0, "worker threads (default: performance cores on Apple silicon, else CPUs - 1)")
		queue      = flag.Int("queue", 0, "max queued requests (default: 4 x workers)")
		noShed     = flag.Bool("no-deadline-shed", false, "disable deadline-aware admission (queue limit only)")
		defDL      = flag.Duration("default-deadline", 2*time.Second, "deadline when the client sends none")
		maxImg     = flag.Int64("max-image-bytes", 8<<20, "max image upload")
		maxMap     = flag.Int64("max-map-bytes", 2<<30, "max map upload")
		traceMode  = flag.String("trace", "auto", "off, stdout, otlp, or auto (otlp if OTEL_EXPORTER_OTLP_ENDPOINT is set)")
		logLevel   = flag.String("log-level", "info", "debug, info, warn, error")
		grace      = flag.Duration("grace", 10*time.Second, "shutdown grace period")
		preload    mapFlags
	)
	flag.Var(&preload, "map", "name=path of a map to load at startup (repeatable)")
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintln(os.Stderr, "bad -log-level:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	if err := run(log, *addr, *grpcAddr, *engineName, *fakeWork, *mapDir, *workers, *queue, *noShed, *defDL, *maxImg, *maxMap, *traceMode, *grace, preload); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr, grpcAddr, engineName string, fakeWork time.Duration, mapDir string, workers, queue int,
	noShed bool, defDL time.Duration, maxImg, maxMap int64, traceMode string, grace time.Duration, preload []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var engine vloc.Engine
	switch engineName {
	case "auto", "cgo":
		engine = vloc.NewCore()
		if engine == nil && engineName == "cgo" {
			return errors.New("built without the vloc tag; rebuild with -tags vloc or use -engine fake")
		}
		if engine == nil {
			engine = &vloc.FakeEngine{Work: fakeWork}
		}
	case "fake":
		engine = &vloc.FakeEngine{Work: fakeWork}
	default:
		return fmt.Errorf("unknown engine %q", engineName)
	}

	tracer, shutdownTrace, err := obs.SetupTracing(ctx, traceMode, os.Stdout)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer shutdownTrace(context.Background())

	if workers <= 0 {
		workers = defaultWorkers()
	}
	pool := admit.New(admit.Config{Workers: workers, MaxQueue: queue, NoDeadlineShed: noShed})
	srv := api.New(api.Config{MaxImageBytes: maxImg, MaxMapBytes: maxMap, DefaultDeadline: defDL, MapDir: mapDir},
		api.Deps{Engine: engine, Pool: pool, Tracer: tracer, Log: log})

	files, _ := filepath.Glob(filepath.Join(mapDir, "*.vmap"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".vmap")
		if _, _, err := srv.LoadFile(name, f); err != nil {
			log.Error("startup map load failed", "map", name, "path", f, "err", err)
		}
	}
	for _, p := range preload {
		name, path, ok := strings.Cut(p, "=")
		if !ok {
			return fmt.Errorf("-map wants name=path, got %q", p)
		}
		if _, _, err := srv.LoadFile(name, path); err != nil {
			return fmt.Errorf("load map %s: %w", name, err)
		}
	}

	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", ln.Addr().String(), "engine", engine.Name(), "workers", pool.Workers(),
		"maps", srv.Maps.Len(), "trace", traceMode)
	errc := make(chan error, 2)
	go func() { errc <- hs.Serve(ln) }()
	var g *grpc.Server
	if grpcAddr != "" {
		gl, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			return err
		}
		g = srv.NewGRPC()
		go func() { errc <- g.Serve(gl) }()
		log.Info("grpc listening", "addr", gl.Addr().String())
	}

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down", "grace", grace)
	srv.Drain()
	sctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err = hs.Shutdown(sctx)
	if g != nil {
		g.GracefulStop() // let in-flight RPCs finish before the pool closes
	}
	pool.Close()
	srv.Maps.Close()
	return err
}
