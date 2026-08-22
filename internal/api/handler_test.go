package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/api"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

type fakeJobStore struct {
	createJob func(context.Context, domain.JobSubmission) (domain.Job, error)
	getJob    func(context.Context, domain.JobID) (domain.Job, error)
}

func (store *fakeJobStore) CreateJob(ctx context.Context, submission domain.JobSubmission) (domain.Job, error) {
	return store.createJob(ctx, submission)
}

func (store *fakeJobStore) GetJob(ctx context.Context, id domain.JobID) (domain.Job, error) {
	return store.getJob(ctx, id)
}

func TestCreateJobUsesDefaultsAndReturnsCreatedJob(t *testing.T) {
	createdAt := time.Date(2026, time.August, 22, 12, 30, 0, 123456000, time.UTC)
	var captured domain.JobSubmission
	store := &fakeJobStore{
		createJob: func(_ context.Context, submission domain.JobSubmission) (domain.Job, error) {
			captured = submission
			return jobFromSubmission(submission, createdAt), nil
		},
	}
	handler := api.NewHandler(store)
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(
		`{"type":"email.send","payload":{"recipient":"user@example.com"},"timeout_ms":30000}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got, want := response.Header().Get("Location"), "/v1/jobs/"+captured.ID().String(); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if got := captured.Type().String(); got != "email.send" {
		t.Fatalf("stored job type = %q, want email.send", got)
	}
	if got := string(captured.Payload().JSON()); got != `{"recipient":"user@example.com"}` {
		t.Fatalf("stored payload = %s", got)
	}
	if got := captured.MaxAttempts(); got != domain.DefaultMaxAttempts {
		t.Fatalf("stored maximum attempts = %d, want %d", got, domain.DefaultMaxAttempts)
	}
	if got := captured.Timeout(); got != 30*time.Second {
		t.Fatalf("stored timeout = %s, want 30s", got)
	}

	var body struct {
		ID           string           `json:"id"`
		Status       domain.JobStatus `json:"status"`
		Deduplicated bool             `json:"deduplicated"`
		CreatedAt    time.Time        `json:"created_at"`
	}
	decodeJSONResponse(t, response, &body)
	if body.ID != captured.ID().String() {
		t.Fatalf("response job ID = %q, want %q", body.ID, captured.ID())
	}
	if body.Status != domain.JobStatusQueued {
		t.Fatalf("response status = %q, want queued", body.Status)
	}
	if body.Deduplicated {
		t.Fatal("response marked a new job as deduplicated")
	}
	if !body.CreatedAt.Equal(createdAt) {
		t.Fatalf("response created timestamp = %s, want %s", body.CreatedAt, createdAt)
	}
}

func TestCreateJobAcceptsExplicitLimitsAndNullPayload(t *testing.T) {
	var captured domain.JobSubmission
	store := &fakeJobStore{
		createJob: func(_ context.Context, submission domain.JobSubmission) (domain.Job, error) {
			captured = submission
			return jobFromSubmission(submission, time.Now().UTC()), nil
		},
	}
	handler := api.NewHandler(store)
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(
		`{"type":"example","payload":null,"max_attempts":7,"timeout_ms":1}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	if got := string(captured.Payload().JSON()); got != "null" {
		t.Fatalf("stored payload = %s, want null", got)
	}
	if got := captured.MaxAttempts(); got != 7 {
		t.Fatalf("stored maximum attempts = %d, want 7", got)
	}
	if got := captured.Timeout(); got != time.Millisecond {
		t.Fatalf("stored timeout = %s, want 1ms", got)
	}
}

func TestCreateJobRejectsInvalidRequests(t *testing.T) {
	largeBody := `{"type":"example","payload":"` + strings.Repeat("a", 1<<20) + `","timeout_ms":1}`
	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{name: "empty body", wantCode: "invalid_request"},
		{name: "non-object body", body: `[]`, wantCode: "invalid_request"},
		{name: "unknown field", body: `{"type":"example","payload":{},"timeout_ms":1,"extra":true}`, wantCode: "invalid_request"},
		{name: "trailing JSON value", body: `{"type":"example","payload":{},"timeout_ms":1} {}`, wantCode: "invalid_request"},
		{name: "body larger than one MiB", body: largeBody, wantCode: "invalid_request"},
		{name: "invalid type", body: `{"type":"Example","payload":{},"timeout_ms":1}`, wantCode: "invalid_job_type"},
		{name: "missing payload", body: `{"type":"example","timeout_ms":1}`, wantCode: "invalid_payload"},
		{name: "malformed payload", body: `{"type":"example","payload":{"message":},"timeout_ms":1}`, wantCode: "invalid_request"},
		{name: "null maximum attempts", body: `{"type":"example","payload":{},"max_attempts":null,"timeout_ms":1}`, wantCode: "invalid_max_attempts"},
		{name: "zero maximum attempts", body: `{"type":"example","payload":{},"max_attempts":0,"timeout_ms":1}`, wantCode: "invalid_max_attempts"},
		{name: "negative maximum attempts", body: `{"type":"example","payload":{},"max_attempts":-1,"timeout_ms":1}`, wantCode: "invalid_max_attempts"},
		{name: "non-integer maximum attempts", body: `{"type":"example","payload":{},"max_attempts":1.5,"timeout_ms":1}`, wantCode: "invalid_max_attempts"},
		{name: "maximum attempts overflow", body: `{"type":"example","payload":{},"max_attempts":2147483648,"timeout_ms":1}`, wantCode: "invalid_max_attempts"},
		{name: "missing timeout", body: `{"type":"example","payload":{}}`, wantCode: "invalid_timeout"},
		{name: "null timeout", body: `{"type":"example","payload":{},"timeout_ms":null}`, wantCode: "invalid_timeout"},
		{name: "zero timeout", body: `{"type":"example","payload":{},"timeout_ms":0}`, wantCode: "invalid_timeout"},
		{name: "negative timeout", body: `{"type":"example","payload":{},"timeout_ms":-1}`, wantCode: "invalid_timeout"},
		{name: "non-integer timeout", body: `{"type":"example","payload":{},"timeout_ms":1.5}`, wantCode: "invalid_timeout"},
		{name: "timeout duration overflow", body: `{"type":"example","payload":{},"timeout_ms":9223372036855}`, wantCode: "invalid_timeout"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			createCalls := 0
			store := &fakeJobStore{
				createJob: func(context.Context, domain.JobSubmission) (domain.Job, error) {
					createCalls++
					return domain.Job{}, errors.New("unexpected store call")
				},
			}
			handler := api.NewHandler(store)
			request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(test.body))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			assertErrorResponse(t, response, http.StatusBadRequest, test.wantCode)
			if createCalls != 0 {
				t.Fatalf("store create calls = %d, want 0", createCalls)
			}
		})
	}
}

func TestCreateJobReturnsGenericInternalError(t *testing.T) {
	store := &fakeJobStore{
		createJob: func(context.Context, domain.JobSubmission) (domain.Job, error) {
			return domain.Job{}, errors.New("database password secret")
		},
	}
	handler := api.NewHandler(store)
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(
		`{"type":"example","payload":{},"timeout_ms":1}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusInternalServerError, "internal_error")
	if strings.Contains(response.Body.String(), "database password secret") {
		t.Fatal("response exposed the internal store error")
	}
}

func TestGetJobReturnsStoredStateWithoutPayload(t *testing.T) {
	job := testJob(t)
	store := &fakeJobStore{
		getJob: func(_ context.Context, id domain.JobID) (domain.Job, error) {
			if id != job.ID {
				t.Fatalf("store job ID = %s, want %s", id, job.ID)
			}
			return job, nil
		},
	}
	handler := api.NewHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+job.ID.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var fields map[string]json.RawMessage
	decodeJSONResponse(t, response, &fields)
	if _, exists := fields["payload"]; exists {
		t.Fatal("job response included the stored payload")
	}
	assertJSONField(t, fields, "id", job.ID.String())
	assertJSONField(t, fields, "type", job.Type.String())
	assertJSONField(t, fields, "status", string(job.Status))
	assertJSONField(t, fields, "attempt_count", job.AttemptCount)
	assertJSONField(t, fields, "max_attempts", job.MaxAttempts)
	assertJSONField(t, fields, "timeout_ms", job.Timeout.Milliseconds())
	assertJSONField(t, fields, "created_at", job.CreatedAt.Format(time.RFC3339Nano))
	assertJSONField(t, fields, "updated_at", job.UpdatedAt.Format(time.RFC3339Nano))
}

func TestGetJobRejectsMalformedID(t *testing.T) {
	getCalls := 0
	store := &fakeJobStore{
		getJob: func(context.Context, domain.JobID) (domain.Job, error) {
			getCalls++
			return domain.Job{}, errors.New("unexpected store call")
		},
	}
	handler := api.NewHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/not-a-uuid", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusBadRequest, "invalid_job_id")
	if getCalls != 0 {
		t.Fatalf("store get calls = %d, want 0", getCalls)
	}
}

func TestGetJobReturnsNotFound(t *testing.T) {
	id := domain.NewJobID()
	store := &fakeJobStore{
		getJob: func(context.Context, domain.JobID) (domain.Job, error) {
			return domain.Job{}, fmt.Errorf("lookup failed: %w", domain.ErrJobNotFound)
		},
	}
	handler := api.NewHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+id.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusNotFound, "job_not_found")
}

func TestGetJobReturnsGenericInternalError(t *testing.T) {
	id := domain.NewJobID()
	store := &fakeJobStore{
		getJob: func(context.Context, domain.JobID) (domain.Job, error) {
			return domain.Job{}, errors.New("database password secret")
		},
	}
	handler := api.NewHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+id.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusInternalServerError, "internal_error")
	if strings.Contains(response.Body.String(), "database password secret") {
		t.Fatal("response exposed the internal store error")
	}
}

func jobFromSubmission(submission domain.JobSubmission, createdAt time.Time) domain.Job {
	return domain.Job{
		ID:           submission.ID(),
		Type:         submission.Type(),
		Payload:      submission.Payload(),
		Status:       domain.JobStatusQueued,
		AttemptCount: 0,
		MaxAttempts:  submission.MaxAttempts(),
		Timeout:      submission.Timeout(),
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
}

func testJob(t *testing.T) domain.Job {
	t.Helper()

	jobType, err := domain.ParseJobType("email.send")
	if err != nil {
		t.Fatalf("parse test job type: %v", err)
	}
	payload, err := domain.ParsePayload(json.RawMessage(`{"secret":"omitted"}`))
	if err != nil {
		t.Fatalf("parse test payload: %v", err)
	}

	return domain.Job{
		ID:           domain.NewJobID(),
		Type:         jobType,
		Payload:      payload,
		Status:       domain.JobStatusQueued,
		AttemptCount: 0,
		MaxAttempts:  5,
		Timeout:      30 * time.Second,
		CreatedAt:    time.Date(2026, time.August, 22, 12, 30, 0, 123456000, time.UTC),
		UpdatedAt:    time.Date(2026, time.August, 22, 12, 31, 0, 654321000, time.UTC),
	}
}

func assertErrorResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()

	if response.Code != wantStatus {
		t.Fatalf("response status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeJSONResponse(t, response, &body)
	if body.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q", body.Error.Code, wantCode)
	}
	if body.Error.Message == "" {
		t.Fatal("error message is empty")
	}
}

func decodeJSONResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()

	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("response contains trailing JSON: %v", err)
	}
}

func assertJSONField[T comparable](t *testing.T, fields map[string]json.RawMessage, name string, want T) {
	t.Helper()

	raw, exists := fields[name]
	if !exists {
		t.Fatalf("response field %q is missing", name)
	}
	var got T
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode response field %q: %v", name, err)
	}
	if got != want {
		t.Fatalf("response field %q = %v, want %v", name, got, want)
	}
}
