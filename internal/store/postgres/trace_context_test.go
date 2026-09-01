package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestPersistenceSpansRemainOnTheAttemptTrace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := provider.Tracer("test")

	submissionCtx, submissionSpan := tracer.Start(ctx, "http.request")
	wantTraceID := submissionSpan.SpanContext().TraceID()
	jobStore := postgres.NewJobStoreWithTracer(pool, tracer)
	job := submitTraceTestJob(t, submissionCtx, jobStore, "trace.connected", "")
	submissionSpan.End()

	retryPolicy, err := domain.NewRetryPolicy(time.Second, time.Minute, func(int64) int64 { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	dispatcherStore := postgres.NewDispatcherStoreWithTracer(pool, testLeaseDuration, retryPolicy, tracer)
	workerID := registerTestWorker(t, ctx, dispatcherStore, 1)
	jobs, err := dispatcherStore.AcquireJobs(ctx, workerID, 1, []domain.JobType{mustJobType(t, "trace.connected")})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("acquire traced job: jobs=%d err=%v", len(jobs), err)
	}
	claimParent := telemetry.ContextFromTraceParent(ctx, jobs[0].TraceParent)
	claimCtx, claimSpan := tracer.Start(claimParent, "dispatcher.claim")
	workerCtx, workerSpan := tracer.Start(claimCtx, "worker.execute")
	result, err := domain.ParseResult([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := domain.NewSucceededOutcome(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcherStore.ReportAttemptTransition(workerCtx, workerID, job.ID, jobs[0].AttemptNumber, outcome); err != nil {
		t.Fatal(err)
	}
	workerSpan.End()
	claimSpan.End()

	spans := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range recorder.Ended() {
		spans[span.Name()] = span
		if span.SpanContext().TraceID() != wantTraceID {
			t.Fatalf("span %q trace ID = %s, want %s", span.Name(), span.SpanContext().TraceID(), wantTraceID)
		}
	}
	insert := spans["db.insert_job"]
	claim := spans["dispatcher.claim"]
	workerExecution := spans["worker.execute"]
	complete := spans["db.complete_attempt"]
	if insert == nil || claim == nil || workerExecution == nil || complete == nil {
		t.Fatalf("missing connected trace spans: %v", spans)
	}
	if claim.Parent().SpanID() != insert.SpanContext().SpanID() ||
		workerExecution.Parent().SpanID() != claim.SpanContext().SpanID() ||
		complete.Parent().SpanID() != workerExecution.SpanContext().SpanID() {
		t.Fatal("persistence and attempt spans do not form one parent chain")
	}
	insertAttributes := traceSpanAttributes(insert)
	if insertAttributes["job.id"] != job.ID.String() || insertAttributes["job.type"] != "trace.connected" {
		t.Fatalf("insert span attributes = %v", insertAttributes)
	}
	completeAttributes := traceSpanAttributes(complete)
	for key, want := range map[string]string{
		"job.id": job.ID.String(), "job.attempt": "1", "worker.id": workerID.String(), "job.outcome": "succeeded",
	} {
		if completeAttributes[key] != want {
			t.Fatalf("completion span attribute %s = %q, want %q: %v", key, completeAttributes[key], want, completeAttributes)
		}
	}
}

func traceSpanAttributes(span sdktrace.ReadOnlySpan) map[string]string {
	attributes := map[string]string{}
	for _, value := range span.Attributes() {
		attributes[string(value.Key)] = value.Value.String()
	}
	return attributes
}

func TestJobTraceContextPersistsAndReachesAcquiredJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)

	validContext := traceTestContext(
		t,
		"0102030405060708090a0b0c0d0e0f10",
		"0102030405060708",
	)
	wantTraceparent, ok := telemetry.TraceParentFromContext(validContext)
	if !ok {
		t.Fatal("valid test context has no traceparent")
	}
	validJob := submitTraceTestJob(t, validContext, jobStore, "trace.valid", "")
	assertStoredTraceparent(t, ctx, pool, validJob.ID, wantTraceparent, true)

	missingJob := submitTraceTestJob(t, context.Background(), jobStore, "trace.missing", "")
	assertStoredTraceparent(t, ctx, pool, missingJob.ID, "", false)

	invalidSpanContext := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		SpanID: mustTraceTestSpanID(t, "1111111111111111"),
	})
	invalidContext := oteltrace.ContextWithSpanContext(context.Background(), invalidSpanContext)
	invalidJob := submitTraceTestJob(t, invalidContext, jobStore, "trace.invalid", "")
	assertStoredTraceparent(t, ctx, pool, invalidJob.ID, "", false)

	idempotentSubmission := newTraceTestSubmission(t, "trace.idempotent", "trace-request-1")
	firstContext := traceTestContext(
		t,
		"11111111111111111111111111111111",
		"1111111111111111",
	)
	firstTraceparent, ok := telemetry.TraceParentFromContext(firstContext)
	if !ok {
		t.Fatal("first idempotent context has no traceparent")
	}
	created, err := jobStore.SubmitJob(firstContext, idempotentSubmission)
	if err != nil {
		t.Fatalf("submit idempotent traced job: %v", err)
	}
	replayContext := traceTestContext(
		t,
		"22222222222222222222222222222222",
		"2222222222222222",
	)
	replayed, err := jobStore.SubmitJob(replayContext, idempotentSubmission)
	if err != nil {
		t.Fatalf("replay idempotent traced job: %v", err)
	}
	if !replayed.Deduplicated || replayed.Job.ID != created.Job.ID {
		t.Fatalf("idempotent replay = %#v, want original job", replayed)
	}
	assertStoredTraceparent(t, ctx, pool, created.Job.ID, firstTraceparent, true)

	claimContext := traceTestContext(
		t,
		"33333333333333333333333333333333",
		"3333333333333333",
	)
	claimTraceparent, ok := telemetry.TraceParentFromContext(claimContext)
	if !ok {
		t.Fatal("claim test context has no traceparent")
	}
	submitTraceTestJob(t, claimContext, jobStore, "trace.claim", "")
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	workerID := registerTestWorker(t, ctx, dispatcherStore, 1)
	jobs, err := dispatcherStore.AcquireJobs(ctx, workerID, 1, []domain.JobType{mustJobType(t, "trace.claim")})
	if err != nil {
		t.Fatalf("claim traced job: %v", err)
	}
	if len(jobs) != 1 || jobs[0].TraceParent != claimTraceparent {
		t.Fatalf("claimed jobs = %#v, want traceparent %q", jobs, claimTraceparent)
	}
}

func submitTraceTestJob(
	t *testing.T,
	ctx context.Context,
	store *postgres.JobStore,
	jobType string,
	idempotencyKey string,
) domain.Job {
	t.Helper()
	submission := newTraceTestSubmission(t, jobType, idempotencyKey)
	result, err := store.SubmitJob(ctx, submission)
	if err != nil {
		t.Fatalf("submit %s job: %v", jobType, err)
	}
	return result.Job
}

func newTraceTestSubmission(t *testing.T, jobType, idempotencyKey string) domain.JobSubmission {
	t.Helper()
	payload, err := domain.ParsePayload(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	submission, err := domain.NewJobSubmission(
		mustJobType(t, jobType),
		payload,
		3,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if idempotencyKey == "" {
		return submission
	}
	key, err := domain.ParseIdempotencyKey(idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	submission, err = submission.WithIdempotencyKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func assertStoredTraceparent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID domain.JobID,
	want string,
	wantValid bool,
) {
	t.Helper()
	var stored pgtype.Text
	if err := pool.QueryRow(ctx, `SELECT traceparent FROM jobs WHERE id = $1`, jobID.UUID()).Scan(&stored); err != nil {
		t.Fatalf("read stored traceparent: %v", err)
	}
	if stored.Valid != wantValid || stored.String != want {
		t.Fatalf("stored traceparent = (%q, %t), want (%q, %t)", stored.String, stored.Valid, want, wantValid)
	}
}

func traceTestContext(t *testing.T, traceIDValue, spanIDValue string) context.Context {
	t.Helper()
	spanContext := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    mustTraceTestTraceID(t, traceIDValue),
		SpanID:     mustTraceTestSpanID(t, spanIDValue),
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	return oteltrace.ContextWithRemoteSpanContext(context.Background(), spanContext)
}

func mustTraceTestTraceID(t *testing.T, value string) oteltrace.TraceID {
	t.Helper()
	id, err := oteltrace.TraceIDFromHex(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTraceTestSpanID(t *testing.T, value string) oteltrace.SpanID {
	t.Helper()
	id, err := oteltrace.SpanIDFromHex(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
