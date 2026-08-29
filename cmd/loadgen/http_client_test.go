package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shaibalmuhtadee/quarry/internal/loadgen"
)

func TestHTTPClientAndRunnerExercisePublicJobFlow(t *testing.T) {
	type storedJob struct {
		createdAt  time.Time
		finishedAt time.Time
	}
	var mu sync.Mutex
	jobs := make(map[string]storedJob)
	postCalls := 0
	workerID := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/jobs":
			mu.Lock()
			postCalls++
			call := postCalls
			mu.Unlock()
			if call == 1 {
				writer.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": "unavailable", "message": "retry"}})
				return
			}
			if request.Header.Get("Idempotency-Key") == "" {
				t.Error("submission omitted Idempotency-Key")
			}
			var input submitJobRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Errorf("decode submission: %v", err)
			}
			if input.Type != "demo.echo" || input.MaxAttempts != 3 || input.TimeoutMS != 1000 {
				t.Errorf("submission = %#v", input)
			}
			id := uuid.NewString()
			createdAt := time.Now().UTC()
			finishedAt := createdAt.Add(time.Millisecond)
			mu.Lock()
			jobs[id] = storedJob{createdAt: createdAt, finishedAt: finishedAt}
			mu.Unlock()
			writer.Header().Set("Location", "/v1/jobs/"+id)
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(submitJobResponse{ID: id, Status: "queued", CreatedAt: createdAt})
		case request.Method == http.MethodGet && request.URL.Path != "":
			parts := splitJobPath(request.URL.Path)
			if parts.id == "" {
				http.NotFound(writer, request)
				return
			}
			mu.Lock()
			job, exists := jobs[parts.id]
			mu.Unlock()
			if !exists {
				http.NotFound(writer, request)
				return
			}
			if parts.attempts {
				_ = json.NewEncoder(writer).Encode(map[string]any{"attempts": []map[string]any{{
					"attempt_no": 1, "worker_id": workerID, "status": "succeeded",
					"error_code": nil, "error_message": nil,
					"started_at": job.createdAt, "finished_at": job.finishedAt,
				}}})
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": parts.id, "type": "demo.echo", "status": "succeeded", "attempt_count": 1,
				"max_attempts": 3, "timeout_ms": 1000, "result": map[string]any{"ok": true},
				"created_at": job.createdAt, "updated_at": job.finishedAt, "finished_at": job.finishedAt,
				"cancel_requested_at": nil,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := newHTTPAPIClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create HTTP client: %v", err)
	}
	runner, err := loadgen.NewRunner(client, loadgen.Config{
		RunID: "http-integration", MeasurementDuration: 15 * time.Millisecond,
		DrainTimeout: 100 * time.Millisecond, PollInterval: time.Millisecond,
		MaxOutstanding: 1, MaxHTTPConcurrency: 1,
	}, func(sequence uint64) loadgen.Submission {
		return loadgen.Submission{
			JobType: "demo.echo", Payload: json.RawMessage(fmt.Sprintf(`{"sequence":%d}`, sequence)),
			MaxAttempts: 3, Timeout: time.Second,
		}
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run load generator: %v", err)
	}
	if len(result.Samples) == 0 {
		t.Fatal("runner returned no samples")
	}
	first, ok := result.Samples[0].(loadgen.TerminalJobSample)
	if !ok || first.Status != loadgen.JobStatusSucceeded || len(first.Attempts) != 1 {
		t.Fatalf("first sample = %#v", result.Samples[0])
	}
	if len(first.Errors) != 1 || first.Errors[0].Operation != loadgen.OperationSubmit || !first.Errors[0].Retryable {
		t.Fatalf("first sample errors = %#v", first.Errors)
	}
}

func TestHTTPClientClassifiesTransportAndResponseErrors(t *testing.T) {
	transportErr := errors.New("connection reset")
	client, err := newHTTPAPIClient("http://quarry.invalid", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})})
	if err != nil {
		t.Fatalf("create HTTP client: %v", err)
	}
	_, err = client.SubmitJob(context.Background(), loadgen.SubmissionRequest{Submission: loadgen.Submission{
		JobType: "demo.echo", Payload: json.RawMessage(`{}`), MaxAttempts: 1, Timeout: time.Second,
	}, IdempotencyKey: "test"})
	if !loadgen.IsRetryable(err) || !loadgen.IsAmbiguous(err) {
		t.Fatalf("submission transport error = %v", err)
	}
	_, err = client.GetJob(context.Background(), uuid.NewString())
	if !loadgen.IsRetryable(err) || loadgen.IsAmbiguous(err) {
		t.Fatalf("poll transport error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"code":"busy","message":"retry"}}`))
	}))
	defer server.Close()
	client, err = newHTTPAPIClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create status client: %v", err)
	}
	_, err = client.GetJob(context.Background(), uuid.NewString())
	if !loadgen.IsRetryable(err) || loadgen.IsAmbiguous(err) {
		t.Fatalf("HTTP 429 error = %v", err)
	}
}

func TestHTTPClientRejectsMalformedSuccessfulResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":`))
	}))
	defer server.Close()
	client, err := newHTTPAPIClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = client.SubmitJob(context.Background(), loadgen.SubmissionRequest{Submission: loadgen.Submission{
		JobType: "demo.echo", Payload: json.RawMessage(`{}`), MaxAttempts: 1, Timeout: time.Second,
	}, IdempotencyKey: "malformed"})
	if err == nil || loadgen.IsRetryable(err) || !loadgen.IsAmbiguous(err) {
		t.Fatalf("malformed submission error = %v", err)
	}
}

type jobPath struct {
	id       string
	attempts bool
}

func splitJobPath(path string) jobPath {
	const prefix = "/v1/jobs/"
	if len(path) <= len(prefix) || path[:len(prefix)] != prefix {
		return jobPath{}
	}
	remainder := path[len(prefix):]
	const suffix = "/attempts"
	if len(remainder) > len(suffix) && remainder[len(remainder)-len(suffix):] == suffix {
		return jobPath{id: remainder[:len(remainder)-len(suffix)], attempts: true}
	}
	return jobPath{id: remainder}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
