package worker

import (
	"context"
	"sync"
	"testing"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestWorkerContinuesAcquiredTraceThroughHandlerAndReport(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("test")
	parentCtx, parent := tracer.Start(context.Background(), "dispatcher.claim")
	wantTraceID := parent.SpanContext().TraceID()
	traceparent, _ := telemetry.TraceParentFromContext(parentCtx)
	parent.End()

	job := makeJobs(t, 1, "demo.trace")[0]
	job.TraceParent = traceparent
	var acquireMu sync.Mutex
	acquired := false
	reported := make(chan trace.SpanContext, 1)
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		acquireMu.Lock()
		defer acquireMu.Unlock()
		if acquired {
			return nil, nil
		}
		acquired = true
		return []Job{job}, nil
	}
	dispatcher.report = func(
		ctx context.Context,
		_ domain.WorkerID,
		_ domain.JobID,
		_ domain.AttemptNumber,
		_ domain.AttemptOutcome,
	) error {
		reported <- trace.SpanContextFromContext(ctx)
		return nil
	}
	handlerTrace := make(chan trace.SpanContext, 1)
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.trace": func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			handlerTrace <- trace.SpanContextFromContext(ctx)
			return mustResult(t, `{"ok":true}`), nil
		},
	})
	runtime.tracer = tracer

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	gotHandler := <-handlerTrace
	gotReport := <-reported
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	if gotHandler.TraceID() != wantTraceID || gotReport.TraceID() != wantTraceID {
		t.Fatalf("trace IDs handler=%s report=%s, want %s", gotHandler.TraceID(), gotReport.TraceID(), wantTraceID)
	}

	var workerSpan, handlerSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case "worker.execute":
			workerSpan = span
		case "handler":
			handlerSpan = span
		}
	}
	if workerSpan == nil || handlerSpan == nil {
		t.Fatalf("missing worker or handler span: %#v", recorder.Ended())
	}
	if workerSpan.SpanContext().TraceID() != wantTraceID || handlerSpan.Parent().SpanID() != workerSpan.SpanContext().SpanID() {
		t.Fatal("worker and handler spans did not preserve trace parentage")
	}
	workerAttributes := map[string]string{}
	for _, value := range workerSpan.Attributes() {
		workerAttributes[string(value.Key)] = value.Value.Emit()
	}
	for key, want := range map[string]string{
		"job.id": job.ID.String(), "job.type": job.Type.String(), "job.attempt": "1",
		"worker.id": runtime.registration.WorkerID.String(), "job.outcome": "succeeded",
	} {
		if workerAttributes[key] != want {
			t.Fatalf("worker span attribute %s = %q, want %q: %v", key, workerAttributes[key], want, workerSpan.Attributes())
		}
	}
}
