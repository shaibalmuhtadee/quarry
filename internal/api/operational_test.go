package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/api"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestHealthDoesNotQueryPostgres(t *testing.T) {
	readinessCalls := 0
	handler := api.NewHandler(
		&fakeJobStore{},
		readinessCheckerFunc(func(context.Context) error {
			readinessCalls++
			return errors.New("unexpected readiness query")
		}),
		discardLogger(),
	)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertOperationalResponse(t, response, http.StatusOK, "ok")
	if readinessCalls != 0 {
		t.Fatalf("readiness calls = %d, want 0", readinessCalls)
	}
}

func TestReadinessReportsPostgresState(t *testing.T) {
	tests := []struct {
		name       string
		checkError error
		wantStatus int
		wantBody   string
	}{
		{name: "ready", wantStatus: http.StatusOK, wantBody: "ready"},
		{name: "not ready", checkError: errors.New("PostgreSQL unavailable"), wantStatus: http.StatusServiceUnavailable, wantBody: "not_ready"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			readinessCalls := 0
			handler := api.NewHandler(
				&fakeJobStore{},
				readinessCheckerFunc(func(ctx context.Context) error {
					readinessCalls++
					deadline, ok := ctx.Deadline()
					if !ok {
						t.Fatal("readiness context has no deadline")
					}
					remaining := time.Until(deadline)
					if remaining <= 0 || remaining > 2*time.Second {
						t.Fatalf("readiness deadline is %s away, want within 2s", remaining)
					}
					return test.checkError
				}),
				discardLogger(),
			)
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			assertOperationalResponse(t, response, test.wantStatus, test.wantBody)
			if readinessCalls != 1 {
				t.Fatalf("readiness calls = %d, want 1", readinessCalls)
			}
		})
	}
}

func TestRequestLogIncludesOutcomeAndJobID(t *testing.T) {
	job := testJob(t)
	store := &fakeJobStore{
		getJob: func(context.Context, domain.JobID) (domain.Job, error) {
			return job, nil
		},
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := api.NewHandler(
		store,
		readinessCheckerFunc(func(context.Context) error { return nil }),
		logger,
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+job.ID.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusOK)
	}
	var entry map[string]any
	decoder := json.NewDecoder(&output)
	if err := decoder.Decode(&entry); err != nil {
		t.Fatalf("decode request log: %v", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("request produced more than one log entry: %v", err)
	}
	assertLogField(t, entry, "msg", "http request")
	assertLogField(t, entry, "method", http.MethodGet)
	assertLogField(t, entry, "path", "/v1/jobs/"+job.ID.String())
	assertLogField(t, entry, "status", float64(http.StatusOK))
	assertLogField(t, entry, "outcome", "success")
	assertLogField(t, entry, "job_id", job.ID.String())
	if _, exists := entry["duration"]; !exists {
		t.Fatal("request log has no duration field")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func assertOperationalResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantBody string,
) {
	t.Helper()

	if response.Code != wantStatus {
		t.Fatalf("response status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Status string `json:"status"`
	}
	decodeJSONResponse(t, response, &body)
	if body.Status != wantBody {
		t.Fatalf("response body status = %q, want %q", body.Status, wantBody)
	}
}

func assertLogField[T comparable](t *testing.T, entry map[string]any, name string, want T) {
	t.Helper()

	got, exists := entry[name]
	if !exists {
		t.Fatalf("request log field %q is missing", name)
	}
	if got != any(want) {
		t.Fatalf("request log field %q = %v, want %v", name, got, want)
	}
}
