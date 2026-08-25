package dispatcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/api"
	"github.com/shaibalmuhtadee/quarry/internal/dispatcher"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	workerruntime "github.com/shaibalmuhtadee/quarry/internal/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type readyCheck func(context.Context) error

func (check readyCheck) Ping(ctx context.Context) error { return check(ctx) }

func TestPendingJobsCancelThroughHTTPAndPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := startDispatcherTestPostgres(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	handler := api.NewHandler(
		jobStore,
		readyCheck(func(context.Context) error { return nil }),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	for _, originalStatus := range []domain.JobStatus{domain.JobStatusQueued, domain.JobStatusRetryWait} {
		t.Run(string(originalStatus), func(t *testing.T) {
			jobID := submitJobThroughHTTP(t, handler, "http.cancel-"+string(originalStatus), 3, 30*time.Second)
			if originalStatus == domain.JobStatusRetryWait {
				if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'retry_wait' WHERE id = $1`, jobID.UUID()); err != nil {
					t.Fatalf("put job in retry wait: %v", err)
				}
			}

			response := requestCancellationThroughHTTP(t, handler, jobID)
			if response.Code != http.StatusOK {
				t.Fatalf("cancellation status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
			}
			var body struct {
				Status            domain.JobStatus `json:"status"`
				CancelRequestedAt *time.Time       `json:"cancel_requested_at"`
				FinishedAt        *time.Time       `json:"finished_at"`
			}
			decodeHTTPJSON(t, response, &body)
			if body.Status != domain.JobStatusCancelled || body.CancelRequestedAt == nil || body.FinishedAt == nil {
				t.Fatalf("cancellation response = %#v", body)
			}
			stored, err := jobStore.GetJob(ctx, jobID)
			if err != nil {
				t.Fatalf("read cancelled job: %v", err)
			}
			if stored.Status != domain.JobStatusCancelled || stored.CancelRequestedAt == nil || stored.FinishedAt == nil {
				t.Fatalf("stored cancellation = %#v", stored)
			}
		})
	}
}

func TestRunningJobCancelsThroughHTTPGRPCWorkerAndPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := startDispatcherTestPostgres(t, ctx)
	dispatcherStore := newIntegrationDispatcherStore(t, pool, 20*time.Second)
	jobStore := postgres.NewJobStore(pool)
	httpHandler := api.NewHandler(
		jobStore,
		readyCheck(func(context.Context) error { return nil }),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	dispatcherv1.RegisterDispatcherServiceServer(server, dispatcher.NewService(dispatcherStore))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	})
	connection, err := grpc.NewClient(
		"passthrough:///cancellation-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatalf("create gRPC connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client, err := workerruntime.NewGRPCClient(dispatcherv1.NewDispatcherServiceClient(connection), time.Second)
	if err != nil {
		t.Fatalf("create worker gRPC client: %v", err)
	}

	handlerStarted := make(chan struct{})
	handlerCause := make(chan error, 1)
	jobType := "http.running-cancel"
	runtime, err := workerruntime.New(client, map[string]workerruntime.Handler{
		jobType: func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			close(handlerStarted)
			<-ctx.Done()
			handlerCause <- context.Cause(ctx)
			return domain.Result{}, ctx.Err()
		},
	}, workerruntime.Config{
		Registration: workerruntime.Registration{
			WorkerID: domain.NewWorkerID(), Hostname: "cancellation-worker", Version: "test", Concurrency: 1, StartedAt: time.Now().UTC(),
		},
		IdleBackoffMin: time.Millisecond, IdleBackoffMax: 2 * time.Millisecond,
		ReportBackoffMin: time.Millisecond, ReportBackoffMax: 2 * time.Millisecond,
		HeartbeatInterval: 5 * time.Millisecond,
		ShutdownTimeout:   time.Second,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- runtime.Run(workerCtx) }()
	defer stopWorker()

	jobID := submitJobThroughHTTP(t, httpHandler, jobType, 3, 30*time.Second)
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	response := requestCancellationThroughHTTP(t, httpHandler, jobID)
	if response.Code != http.StatusOK {
		t.Fatalf("running cancellation status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	select {
	case cause := <-handlerCause:
		if cause == nil || cause.Error() != "job cancellation was requested" {
			t.Fatalf("handler cancellation cause = %v", cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not observe cancellation")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, err := jobStore.GetJob(ctx, jobID)
		if err != nil {
			t.Fatalf("read running cancellation: %v", err)
		}
		if stored.Status == domain.JobStatusCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job status = %q, want cancelled", stored.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, jobID)
	if err != nil {
		t.Fatalf("list cancelled attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusCancelled || attempts[0].Failure == nil ||
		attempts[0].Failure.Code() != "cancellation_requested" || attempts[0].Failure.Message() != "job cancellation was requested" {
		t.Fatalf("cancelled attempts = %#v", attempts)
	}
	time.Sleep(25 * time.Millisecond)
	if repeated, err := jobStore.ListJobAttempts(ctx, jobID); err != nil || len(repeated) != 1 {
		t.Fatalf("attempts after cancellation = (%#v, %v), want one", repeated, err)
	}

	stopWorker()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("worker stopped with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func submitJobThroughHTTP(t *testing.T, handler http.Handler, jobType string, maxAttempts int32, timeout time.Duration) domain.JobID {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"type": jobType, "payload": map[string]any{"test": true}, "max_attempts": maxAttempts, "timeout_ms": timeout.Milliseconds(),
	})
	if err != nil {
		t.Fatalf("encode submission: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("submission status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeHTTPJSON(t, response, &created)
	jobID, err := domain.ParseJobID(created.ID)
	if err != nil {
		t.Fatalf("parse submitted job ID: %v", err)
	}
	return jobID
}

func requestCancellationThroughHTTP(t *testing.T, handler http.Handler, jobID domain.JobID) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+jobID.String()+"/cancel", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeHTTPJSON(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode HTTP response: %v", err)
	}
}
