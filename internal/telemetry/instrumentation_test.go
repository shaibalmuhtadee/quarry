package telemetry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

func TestHTTPAndGRPCInstrumentationPreserveContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	runtime := &Runtime{tracerProvider: provider}
	tracer := provider.Tracer("test")
	parentCtx, parent := tracer.Start(context.Background(), "caller")
	wantTraceID := parent.SpanContext().TraceID()
	traceparent, _ := TraceParentFromContext(parentCtx)

	httpTrace := make(chan string, 1)
	handler := runtime.HTTPHandler(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		value, _ := TraceParentFromContext(request.Context())
		httpTrace <- value
	}), "http.request")
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", nil)
	request.Header.Set("traceparent", traceparent)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got := <-httpTrace; !IsValidTraceParent(got) {
		t.Fatalf("HTTP handler traceparent = %q", got)
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.StatsHandler(runtime.GRPCServerStatsHandler()))
	healthpb.RegisterHealthServer(server, health.NewServer())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithStatsHandler(runtime.GRPCClientStatsHandler()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if _, err := healthpb.NewHealthClient(connection).Check(parentCtx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	parent.End()

	names := map[string]bool{}
	for _, span := range recorder.Ended() {
		if span.SpanContext().TraceID() != wantTraceID {
			t.Fatalf("span %q trace ID = %s, want %s", span.Name(), span.SpanContext().TraceID(), wantTraceID)
		}
		names[span.Name()] = true
	}
	if !names[http.MethodPost] || !names["grpc.health.v1.Health/Check"] {
		t.Fatalf("instrumented span names = %v", names)
	}
}
