package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/stats"
)

type Runtime struct {
	registry       *prometheus.Registry
	metrics        *Metrics
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
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metrics, err := NewMetrics(registry)
	if err != nil {
		_ = exporter.Shutdown(context.Background())
		return nil, fmt.Errorf("register telemetry metrics: %w", err)
	}

	return &Runtime{
		registry: registry,
		metrics:  metrics,
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

func (runtime *Runtime) Metrics() *Metrics {
	return runtime.metrics
}

func (runtime *Runtime) TracerProvider() *sdktrace.TracerProvider {
	return runtime.tracerProvider
}

func (runtime *Runtime) Tracer(name string) trace.Tracer {
	return runtime.tracerProvider.Tracer(name)
}

func (runtime *Runtime) HTTPHandler(next http.Handler, operation string) http.Handler {
	return otelhttp.NewHandler(
		next,
		operation,
		otelhttp.WithTracerProvider(runtime.tracerProvider),
		otelhttp.WithPropagators(propagation.TraceContext{}),
	)
}

func (runtime *Runtime) GRPCServerStatsHandler() stats.Handler {
	return otelgrpc.NewServerHandler(
		otelgrpc.WithTracerProvider(runtime.tracerProvider),
		otelgrpc.WithPropagators(propagation.TraceContext{}),
	)
}

func (runtime *Runtime) GRPCClientStatsHandler() stats.Handler {
	return otelgrpc.NewClientHandler(
		otelgrpc.WithTracerProvider(runtime.tracerProvider),
		otelgrpc.WithPropagators(propagation.TraceContext{}),
	)
}

func (runtime *Runtime) Shutdown(ctx context.Context) error {
	return runtime.tracerProvider.Shutdown(ctx)
}
