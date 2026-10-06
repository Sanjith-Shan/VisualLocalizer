package obs

import (
	"context"
	"io"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TraceMode selects the exporter.
type TraceMode string

const (
	TraceOff    TraceMode = "off"
	TraceStdout TraceMode = "stdout"
	TraceOTLP   TraceMode = "otlp"
)

// SetupTracing returns a tracer and a shutdown func. "otlp" uses the standard
// OTEL_EXPORTER_OTLP_* environment variables. "auto" picks otlp when
// OTEL_EXPORTER_OTLP_ENDPOINT is set and off otherwise.
func SetupTracing(ctx context.Context, mode string, w io.Writer) (trace.Tracer, func(context.Context) error, error) {
	if mode == "auto" {
		mode = string(TraceOff)
		if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
			mode = string(TraceOTLP)
		}
	}
	var exp sdktrace.SpanExporter
	var err error
	switch TraceMode(mode) {
	case TraceStdout:
		exp, err = stdouttrace.New(stdouttrace.WithWriter(w))
	case TraceOTLP:
		exp, err = otlptracehttp.New(ctx)
	default:
		return noop.NewTracerProvider().Tracer("vlocd"), func(context.Context) error { return nil }, nil
	}
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName("vlocd"))),
	)
	otel.SetTracerProvider(tp)
	return tp.Tracer("vlocd"), tp.Shutdown, nil
}
