package dispatcher_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/dispatcher"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	workerruntime "github.com/shaibalmuhtadee/quarry/internal/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestWorkerRetryableFailureRetriesUntilMaximumAttempts(t *testing.T) {
	handlerErr, err := workerruntime.NewRetryableHandlerError(
		"dependency_timeout",
		"dependency timed out",
		fmt.Errorf("dial internal dependency"),
	)
	if err != nil {
		t.Fatal(err)
	}
	testWorkerFailureThroughGRPCAndPostgres(
		t,
		2,
		domain.AttemptStatusRetryableFailed,
		func(context.Context, domain.Payload) (domain.Result, error) {
			return domain.Result{}, handlerErr
		},
	)
}

func TestWorkerPermanentFailureDoesNotRetry(t *testing.T) {
	handlerErr, err := workerruntime.NewPermanentHandlerError(
		"invalid_input",
		"input is invalid",
		fmt.Errorf("unsafe parser detail"),
	)
	if err != nil {
		t.Fatal(err)
	}
	testWorkerFailureThroughGRPCAndPostgres(
		t,
		1,
		domain.AttemptStatusPermanentFailed,
		func(context.Context, domain.Payload) (domain.Result, error) {
			return domain.Result{}, handlerErr
		},
	)
}

func testWorkerFailureThroughGRPCAndPostgres(
	t *testing.T,
	wantAttempts int,
	wantAttemptStatus domain.AttemptStatus,
	handler workerruntime.Handler,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := startDispatcherTestPostgres(t, ctx)
	store := newIntegrationDispatcherStore(t, pool, 20*time.Second)
	jobStore := postgres.NewJobStore(pool)

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	dispatcherv1.RegisterDispatcherServiceServer(server, dispatcher.NewService(store))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	})
	connection, err := grpc.NewClient(
		"passthrough:///worker-failure-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("create gRPC connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client, err := workerruntime.NewGRPCClient(dispatcherv1.NewDispatcherServiceClient(connection), time.Second)
	if err != nil {
		t.Fatalf("create worker gRPC client: %v", err)
	}

	jobType := mustJobType(t, "integration.handler_failure")
	payload, err := domain.ParsePayload([]byte(`{"test":true}`))
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	submission, err := domain.NewJobSubmission(jobType, payload, 2, 30*time.Second)
	if err != nil {
		t.Fatalf("create job submission: %v", err)
	}
	job, err := jobStore.CreateJob(ctx, submission)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	workerID := domain.NewWorkerID()
	runtime, err := workerruntime.New(client, map[string]workerruntime.Handler{jobType.String(): handler}, workerruntime.Config{
		Registration: workerruntime.Registration{
			WorkerID: workerID, Hostname: "failure-worker", Version: "test", Concurrency: 1, StartedAt: time.Now().UTC(),
		},
		IdleBackoffMin: time.Millisecond, IdleBackoffMax: 2 * time.Millisecond,
		ReportBackoffMin: time.Millisecond, ReportBackoffMax: 2 * time.Millisecond,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- runtime.Run(workerCtx) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		stored, err := jobStore.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("read failed job: %v", err)
		}
		if stored.Status == domain.JobStatusDeadLettered {
			break
		}
		select {
		case err := <-workerDone:
			t.Fatalf("worker stopped before job dead-lettered: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("job status = %q after waiting for failure reports", stored.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopWorker()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("worker stopped with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}

	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list failed attempts: %v", err)
	}
	if len(attempts) != wantAttempts {
		t.Fatalf("attempt count = %d, want %d", len(attempts), wantAttempts)
	}
	for i, attempt := range attempts {
		if attempt.Number.Int32() != int32(i+1) || attempt.Status != wantAttemptStatus || attempt.Failure == nil {
			t.Fatalf("attempt %d = %#v", i, attempt)
		}
		wantCode, wantMessage := "dependency_timeout", "dependency timed out"
		if wantAttemptStatus == domain.AttemptStatusPermanentFailed {
			wantCode, wantMessage = "invalid_input", "input is invalid"
		}
		if attempt.Failure.Code() != wantCode || attempt.Failure.Message() != wantMessage {
			t.Fatalf("attempt %d failure = (%q, %q), want (%q, %q)", i, attempt.Failure.Code(), attempt.Failure.Message(), wantCode, wantMessage)
		}
	}
}
