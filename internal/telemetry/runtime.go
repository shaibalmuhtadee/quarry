package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type Runtime struct {
	registry       *prometheus.Registry
	tracerProvider *sdktrace.TracerProvider
}

func New(ctx context.Context, cfg Config) (*Runtime, error) {
	traceEndpoint := cfg.TraceEndpoint
	if traceEndpoint == "" {
		traceEndpoint = defaultTraceEndpoint
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(traceEndpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	return newRuntime(cfg, exporter)
}

func newRuntime(cfg Config, exporter sdktrace.SpanExporter) (*Runtime, error) {
	processResource, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		_ = exporter.Shutdown(context.Background())
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	return &Runtime{
		registry: registry,
		tracerProvider: sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(processResource),
		),
	}, nil
}

func (runtime *Runtime) Registry() *prometheus.Registry {
	return runtime.registry
}

func (runtime *Runtime) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(runtime.registry, promhttp.HandlerOpts{})
}

func (runtime *Runtime) TracerProvider() *sdktrace.TracerProvider {
	return runtime.tracerProvider
}

func (runtime *Runtime) Shutdown(ctx context.Context) error {
	return runtime.tracerProvider.Shutdown(ctx)
}
