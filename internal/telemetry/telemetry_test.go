package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	t.Setenv(serviceNameEnv, "")
	t.Setenv(otlpEndpointEnv, "")
	t.Setenv(otlpTracesEndpointEnv, "")
	t.Setenv("QUARRY_TEST_METRICS_ADDR", "")

	cfg, err := LoadConfig("quarry-test", "QUARRY_TEST_METRICS_ADDR", "localhost:9464")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "quarry-test" || cfg.MetricsAddress != "localhost:9464" ||
		cfg.TraceEndpoint != defaultTraceEndpoint {
		t.Fatalf("default config = %#v", cfg)
	}

	t.Setenv(serviceNameEnv, "custom-service")
	t.Setenv("QUARRY_TEST_METRICS_ADDR", "127.0.0.1:19464")
	t.Setenv(otlpTracesEndpointEnv, "http://collector:4318/v1/traces")
	cfg, err = LoadConfig("quarry-test", "QUARRY_TEST_METRICS_ADDR", "localhost:9464")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "custom-service" || cfg.MetricsAddress != "127.0.0.1:19464" {
		t.Fatalf("overridden config = %#v", cfg)
	}
	if cfg.TraceEndpoint != "http://collector:4318/v1/traces" {
		t.Fatalf("trace endpoint = %q", cfg.TraceEndpoint)
	}

	t.Setenv(otlpTracesEndpointEnv, "")
	t.Setenv(otlpEndpointEnv, "http://collector:4318/prefix")
	cfg, err = LoadConfig("quarry-test", "QUARRY_TEST_METRICS_ADDR", "localhost:9464")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TraceEndpoint != "http://collector:4318/prefix/v1/traces" {
		t.Fatalf("base trace endpoint = %q", cfg.TraceEndpoint)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	t.Setenv(serviceNameEnv, "")
	t.Setenv(otlpEndpointEnv, "")
	t.Setenv(otlpTracesEndpointEnv, "")
	t.Setenv("QUARRY_TEST_METRICS_ADDR", "invalid address")
	if _, err := LoadConfig("quarry-test", "QUARRY_TEST_METRICS_ADDR", "localhost:9464"); err == nil {
		t.Fatal("LoadConfig accepted an invalid metrics address")
	}

	t.Setenv("QUARRY_TEST_METRICS_ADDR", "")
	t.Setenv(otlpTracesEndpointEnv, "collector:4318")
	if _, err := LoadConfig("quarry-test", "QUARRY_TEST_METRICS_ADDR", "localhost:9464"); err == nil {
		t.Fatal("LoadConfig accepted a relative OTLP endpoint")
	}

	t.Setenv(otlpTracesEndpointEnv, "")
	if _, err := LoadConfig("", "", ""); err == nil {
		t.Fatal("LoadConfig accepted an empty service name")
	}
}

func TestRegistriesAreIsolated(t *testing.T) {
	first, err := newRuntime(Config{ServiceName: "first"}, &testExporter{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Shutdown(context.Background())
	second, err := newRuntime(Config{ServiceName: "second"}, &testExporter{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Shutdown(context.Background())

	first.Registry().MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "quarry_test_value"}))
	second.Registry().MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "quarry_test_value"}))
}

func TestMetricsHandlerExposesRuntimeMetrics(t *testing.T) {
	runtime, err := newRuntime(Config{ServiceName: "test"}, &testExporter{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Shutdown(context.Background())

	server := http.Server{Handler: runtime.MetricsHandler()}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType == "" {
		t.Fatal("metrics response has no Content-Type")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"go_goroutines", "process_cpu_seconds"} {
		if !strings.Contains(string(body), metric) {
			t.Fatalf("metrics response does not contain %q", metric)
		}
	}
	server.Close()
	<-done
}

func TestMetricsServerShutsDownAndReleasesListener(t *testing.T) {
	server, err := ListenMetrics("127.0.0.1:0", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	for name, timeout := range map[string]time.Duration{
		"read header": server.server.ReadHeaderTimeout,
		"read":        server.server.ReadTimeout,
		"write":       server.server.WriteTimeout,
		"idle":        server.server.IdleTimeout,
	} {
		if timeout <= 0 {
			t.Fatalf("%s timeout = %s, want a positive duration", name, timeout)
		}
	}
	address := server.Address()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("metrics listener still accepts connections")
	}
}

func TestRuntimeShutdownStopsTracerProvider(t *testing.T) {
	exporter := &testExporter{}
	runtime, err := newRuntime(Config{ServiceName: "test"}, exporter)
	if err != nil {
		t.Fatal(err)
	}
	_, span := runtime.TracerProvider().Tracer("test").Start(context.Background(), "span")
	span.End()
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !exporter.shutdown {
		t.Fatal("trace exporter was not shut down")
	}
}

func TestTraceHandlerAddsContextIdentifiers(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&output, nil)))
	traceID, _ := oteltrace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := oteltrace.SpanIDFromHex("0102030405060708")
	ctx := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))
	logger.InfoContext(ctx, "test")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["trace_id"] != traceID.String() || record["span_id"] != spanID.String() {
		t.Fatalf("trace fields = (%v, %v)", record["trace_id"], record["span_id"])
	}
}

func TestTraceHandlerOmitsIdentifiersWithoutTraceContext(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&output, nil)).WithAttrs([]slog.Attr{slog.String("component", "test")}))
	logger.InfoContext(context.Background(), "test")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if _, exists := record["trace_id"]; exists {
		t.Fatal("trace_id exists without a trace context")
	}
	if record["component"] != "test" {
		t.Fatalf("component = %v", record["component"])
	}
}

type testExporter struct {
	shutdown bool
}

func (*testExporter) ExportSpans(context.Context, []trace.ReadOnlySpan) error {
	return nil
}

func (exporter *testExporter) Shutdown(context.Context) error {
	if exporter.shutdown {
		return errors.New("exporter shut down twice")
	}
	exporter.shutdown = true
	return nil
}
