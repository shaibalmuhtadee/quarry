package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/api"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

func TestSuccessfulJobAndAttemptHistoryThroughHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := postgres.NewDispatcherStore(pool, testLeaseDuration)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(api.NewHandler(jobStore, pool, logger))
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/v1/jobs",
		bytes.NewBufferString(`{"type":"http.success","payload":{"message":"hello"},"timeout_ms":30000}`),
	)
	if err != nil {
		t.Fatalf("create job submission request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("submit job: %v", err)
	}
	var submitted struct {
		ID string `json:"id"`
	}
	decodeAPIResponse(t, response, http.StatusCreated, &submitted)
	jobID, err := domain.ParseJobID(submitted.ID)
	if err != nil {
		t.Fatalf("parse submitted job ID: %v", err)
	}

	workerID := registerTestWorker(t, ctx, dispatcherStore, 1)
	acquired := acquireOneTestJob(t, ctx, dispatcherStore, workerID, "http.success")
	result := mustResult(t, `{"message":"hello","handled":true}`)
	if err := dispatcherStore.ReportSuccess(ctx, workerID, jobID, acquired.AttemptNumber, result); err != nil {
		t.Fatalf("report successful attempt: %v", err)
	}

	response, err = server.Client().Get(server.URL + "/v1/jobs/" + submitted.ID)
	if err != nil {
		t.Fatalf("get successful job: %v", err)
	}
	var job struct {
		ID         string          `json:"id"`
		Status     string          `json:"status"`
		Result     json.RawMessage `json:"result"`
		FinishedAt *time.Time      `json:"finished_at"`
	}
	decodeAPIResponse(t, response, http.StatusOK, &job)
	if job.ID != submitted.ID || job.Status != "succeeded" || job.FinishedAt == nil {
		t.Fatalf("successful HTTP job = %#v", job)
	}
	assertJSONEqual(t, job.Result, result.JSON())

	response, err = server.Client().Get(server.URL + "/v1/jobs/" + submitted.ID + "/attempts")
	if err != nil {
		t.Fatalf("get attempt history: %v", err)
	}
	var history struct {
		Attempts []struct {
			Number     int32      `json:"attempt_no"`
			WorkerID   string     `json:"worker_id"`
			Status     string     `json:"status"`
			FinishedAt *time.Time `json:"finished_at"`
		} `json:"attempts"`
	}
	decodeAPIResponse(t, response, http.StatusOK, &history)
	if len(history.Attempts) != 1 {
		t.Fatalf("HTTP attempt count = %d, want 1", len(history.Attempts))
	}
	attempt := history.Attempts[0]
	if attempt.Number != 1 || attempt.WorkerID != workerID.String() ||
		attempt.Status != "succeeded" || attempt.FinishedAt == nil {
		t.Fatalf("successful HTTP attempt = %#v", attempt)
	}
}
