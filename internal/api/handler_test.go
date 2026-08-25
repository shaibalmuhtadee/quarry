package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/api"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

type fakeJobStore struct {
	submitJob       func(context.Context, domain.JobSubmission) (domain.JobSubmissionResult, error)
	getJob          func(context.Context, domain.JobID) (domain.Job, error)
	listJobAttempts func(context.Context, domain.JobID) ([]domain.Attempt, error)
}

type createJobBody struct {
	ID           string    `json:"id"`
	Deduplicated bool      `json:"deduplicated"`
	CreatedAt    time.Time `json:"created_at"`
}

func (store *fakeJobStore) SubmitJob(ctx context.Context, submission domain.JobSubmission) (domain.JobSubmissionResult, error) {
	return store.submitJob(ctx, submission)
}

func (store *fakeJobStore) GetJob(ctx context.Context, id domain.JobID) (domain.Job, error) {
	return store.getJob(ctx, id)
}

func (store *fakeJobStore) ListJobAttempts(ctx context.Context, id domain.JobID) ([]domain.Attempt, error) {
	return store.listJobAttempts(ctx, id)
}

type readinessCheckerFunc func(context.Context) error

func (check readinessCheckerFunc) Ping(ctx context.Context) error {
	return check(ctx)
}

func newTestHandler(store api.JobStore) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return api.NewHandler(store, readinessCheckerFunc(func(context.Context) error { return nil }), logger)
}

func TestCreateJobUsesDefaultsAndReturnsCreatedJob(t *testing.T) {
	createdAt := time.Date(2026, time.August, 22, 12, 30, 0, 123456000, time.UTC)
	var captured domain.JobSubmission
	store := &fakeJobStore{
		submitJob: func(_ context.Context, submission domain.JobSubmission) (domain.JobSubmissionResult, error) {
			captured = submission
			return domain.JobSubmissionResult{Job: jobFromSubmission(submission, createdAt)}, nil
		},
	}
	handler := newTestHandler(store)
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
	if _, ok := captured.IdempotencyKey(); ok {
		t.Fatal("submission without Idempotency-Key became idempotent")
	}
	if _, ok := captured.RequestHash(); ok {
		t.Fatal("submission without Idempotency-Key has a request hash")
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

func TestCreateJobReturnsDeduplicatedReplay(t *testing.T) {
	createdAt := time.Date(2026, time.August, 24, 12, 30, 0, 0, time.UTC)
	existing := testJob(t)
	existing.CreatedAt = createdAt
	var captured domain.JobSubmission
	store := &fakeJobStore{
		submitJob: func(_ context.Context, submission domain.JobSubmission) (domain.JobSubmissionResult, error) {
			captured = submission
			return domain.JobSubmissionResult{Job: existing, Deduplicated: true}, nil
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(
		`{"type":"email.send","payload":{"b":2,"a":1},"timeout_ms":30000}`,
	))
	request.Header.Set("Idempotency-Key", "customer-order-42")
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	key, ok := captured.IdempotencyKey()
	if !ok || key.String() != "customer-order-42" {
		t.Fatalf("captured idempotency key = %q, present %t", key.String(), ok)
	}
	if hash, ok := captured.RequestHash(); !ok || len(hash) != 32 {
		t.Fatalf("captured request hash length = %d, present %t", len(hash), ok)
	}
	if got, want := response.Header().Get("Location"), "/v1/jobs/"+existing.ID.String(); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	var body createJobBody
	decodeJSONResponse(t, response, &body)
	if body.ID != existing.ID.String() || !body.Deduplicated || !body.CreatedAt.Equal(createdAt) {
		t.Fatalf("deduplicated response = %#v", body)
	}
}

func TestCreateJobReturnsIdempotencyConflict(t *testing.T) {
	store := &fakeJobStore{
		submitJob: func(context.Context, domain.JobSubmission) (domain.JobSubmissionResult, error) {
			return domain.JobSubmissionResult{}, fmt.Errorf("submit: %w", domain.ErrIdempotencyConflict)
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(
		`{"type":"email.send","payload":{},"timeout_ms":30000}`,
	))
	request.Header.Set("Idempotency-Key", "customer-order-42")
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusConflict, "idempotency_conflict")
}

func TestCreateJobHashesResolvedMaximumAttempts(t *testing.T) {
	var hashes [][]byte
	store := &fakeJobStore{
		submitJob: func(_ context.Context, submission domain.JobSubmission) (domain.JobSubmissionResult, error) {
			hash, ok := submission.RequestHash()
			if !ok {
				t.Fatal("idempotent submission has no request hash")
			}
			hashes = append(hashes, hash)
			return domain.JobSubmissionResult{Job: jobFromSubmission(submission, time.Now().UTC())}, nil
		},
	}
	handler := newTestHandler(store)
	for _, body := range []string{
		`{"type":"email.send","payload":{"value":1},"timeout_ms":30000}`,
		`{"type":"email.send","payload":{"value":1},"max_attempts":3,"timeout_ms":30000}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "request-1")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
		}
	}
	if len(hashes) != 2 || !reflect.DeepEqual(hashes[0], hashes[1]) {
		t.Fatalf("resolved-default hashes = %x", hashes)
	}
}

func TestCreateJobRejectsInvalidIdempotencyKey(t *testing.T) {
	for _, values := range [][]string{{""}, {strings.Repeat("a", domain.MaxIdempotencyKeyLength+1)}, {"first", "second"}} {
		t.Run(fmt.Sprint(len(values), " values of length ", len(values[0])), func(t *testing.T) {
			calls := 0
			store := &fakeJobStore{
				submitJob: func(context.Context, domain.JobSubmission) (domain.JobSubmissionResult, error) {
					calls++
					return domain.JobSubmissionResult{}, errors.New("unexpected store call")
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(
				`{"type":"email.send","payload":{},"timeout_ms":30000}`,
			))
			request.Header["Idempotency-Key"] = values
			response := httptest.NewRecorder()

			newTestHandler(store).ServeHTTP(response, request)

			assertErrorResponse(t, response, http.StatusBadRequest, "invalid_idempotency_key")
			if calls != 0 {
				t.Fatalf("store calls = %d, want 0", calls)
			}
		})
	}
}

func TestCreateJobAcceptsExplicitLimitsAndNullPayload(t *testing.T) {
	var captured domain.JobSubmission
	store := &fakeJobStore{
		submitJob: func(_ context.Context, submission domain.JobSubmission) (domain.JobSubmissionResult, error) {
			captured = submission
			return domain.JobSubmissionResult{Job: jobFromSubmission(submission, time.Now().UTC())}, nil
		},
	}
	handler := newTestHandler(store)
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
		{name: "null body", body: `null`, wantCode: "invalid_request"},
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
				submitJob: func(context.Context, domain.JobSubmission) (domain.JobSubmissionResult, error) {
					createCalls++
					return domain.JobSubmissionResult{}, errors.New("unexpected store call")
				},
			}
			handler := newTestHandler(store)
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
		submitJob: func(context.Context, domain.JobSubmission) (domain.JobSubmissionResult, error) {
			return domain.JobSubmissionResult{}, errors.New("database password secret")
		},
	}
	handler := newTestHandler(store)
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
	handler := newTestHandler(store)
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
	if got := string(fields["result"]); got != "null" {
		t.Fatalf("response result = %s, want null", got)
	}
	assertJSONField(t, fields, "created_at", job.CreatedAt.Format(time.RFC3339Nano))
	assertJSONField(t, fields, "updated_at", job.UpdatedAt.Format(time.RFC3339Nano))
	if got := string(fields["finished_at"]); got != "null" {
		t.Fatalf("response finish time = %s, want null", got)
	}
}

func TestGetJobReturnsSuccessfulResult(t *testing.T) {
	job := testJob(t)
	result, err := domain.ParseResult(json.RawMessage(`{"echo":{"message":"done"}}`))
	if err != nil {
		t.Fatalf("parse test result: %v", err)
	}
	finishedAt := job.UpdatedAt.Add(time.Second)
	job.Result = &result
	job.Status = domain.JobStatusSucceeded
	job.AttemptCount = 1
	job.FinishedAt = &finishedAt
	store := &fakeJobStore{
		getJob: func(context.Context, domain.JobID) (domain.Job, error) {
			return job, nil
		},
	}
	handler := newTestHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+job.ID.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var fields map[string]json.RawMessage
	decodeJSONResponse(t, response, &fields)
	var gotResult map[string]any
	if err := json.Unmarshal(fields["result"], &gotResult); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	wantResult := map[string]any{"echo": map[string]any{"message": "done"}}
	if !reflect.DeepEqual(gotResult, wantResult) {
		t.Fatalf("result = %#v, want %#v", gotResult, wantResult)
	}
	assertJSONField(t, fields, "finished_at", finishedAt.Format(time.RFC3339Nano))
}

func TestGetJobAttemptsReturnsEmptyArray(t *testing.T) {
	id := domain.NewJobID()
	store := &fakeJobStore{
		listJobAttempts: func(_ context.Context, gotID domain.JobID) ([]domain.Attempt, error) {
			if gotID != id {
				t.Fatalf("store job ID = %s, want %s", gotID, id)
			}
			return []domain.Attempt{}, nil
		},
	}
	handler := newTestHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+id.String()+"/attempts", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var body struct {
		Attempts []json.RawMessage `json:"attempts"`
	}
	decodeJSONResponse(t, response, &body)
	if body.Attempts == nil || len(body.Attempts) != 0 {
		t.Fatalf("attempts = %#v, want empty array", body.Attempts)
	}
}

func TestGetJobAttemptsReturnsAttemptsInStoreOrder(t *testing.T) {
	id := domain.NewJobID()
	workerID := domain.NewWorkerID()
	firstNumber, err := domain.NewAttemptNumber(1)
	if err != nil {
		t.Fatalf("create first attempt number: %v", err)
	}
	secondNumber, err := domain.NewAttemptNumber(2)
	if err != nil {
		t.Fatalf("create second attempt number: %v", err)
	}
	firstStartedAt := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	firstFinishedAt := firstStartedAt.Add(time.Second)
	secondStartedAt := firstFinishedAt.Add(time.Second)
	secondFinishedAt := secondStartedAt.Add(time.Second)
	failure, err := domain.NewAttemptFailure("invalid_input", "handler rejected the input")
	if err != nil {
		t.Fatalf("create attempt failure: %v", err)
	}
	store := &fakeJobStore{
		listJobAttempts: func(context.Context, domain.JobID) ([]domain.Attempt, error) {
			return []domain.Attempt{
				{
					JobID:      id,
					Number:     firstNumber,
					WorkerID:   workerID,
					Status:     domain.AttemptStatusSucceeded,
					StartedAt:  firstStartedAt,
					FinishedAt: &firstFinishedAt,
				},
				{
					JobID:      id,
					Number:     secondNumber,
					WorkerID:   workerID,
					Status:     domain.AttemptStatusPermanentFailed,
					Failure:    &failure,
					StartedAt:  secondStartedAt,
					FinishedAt: &secondFinishedAt,
				},
			}, nil
		},
	}
	handler := newTestHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+id.String()+"/attempts", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var body struct {
		Attempts []struct {
			Number       int32                `json:"attempt_no"`
			WorkerID     string               `json:"worker_id"`
			Status       domain.AttemptStatus `json:"status"`
			ErrorCode    *string              `json:"error_code"`
			ErrorMessage *string              `json:"error_message"`
			StartedAt    time.Time            `json:"started_at"`
			FinishedAt   *time.Time           `json:"finished_at"`
		} `json:"attempts"`
	}
	decodeJSONResponse(t, response, &body)
	if len(body.Attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(body.Attempts))
	}
	if body.Attempts[0].Number != 1 || body.Attempts[1].Number != 2 {
		t.Fatalf("attempt order = [%d, %d], want [1, 2]", body.Attempts[0].Number, body.Attempts[1].Number)
	}
	for i, attempt := range body.Attempts {
		if attempt.WorkerID != workerID.String() {
			t.Fatalf("attempt %d worker = %q", i, attempt.WorkerID)
		}
		if attempt.FinishedAt == nil {
			t.Fatalf("attempt %d has null finish time", i)
		}
	}
	if body.Attempts[0].Status != domain.AttemptStatusSucceeded || body.Attempts[0].ErrorCode != nil || body.Attempts[0].ErrorMessage != nil {
		t.Fatalf("successful attempt = %#v", body.Attempts[0])
	}
	if body.Attempts[1].Status != domain.AttemptStatusPermanentFailed || body.Attempts[1].ErrorCode == nil ||
		*body.Attempts[1].ErrorCode != "invalid_input" || body.Attempts[1].ErrorMessage == nil ||
		*body.Attempts[1].ErrorMessage != "handler rejected the input" {
		t.Fatalf("failed attempt = %#v", body.Attempts[1])
	}
}

func TestGetJobAttemptsReturnsNotFound(t *testing.T) {
	id := domain.NewJobID()
	store := &fakeJobStore{
		listJobAttempts: func(context.Context, domain.JobID) ([]domain.Attempt, error) {
			return nil, fmt.Errorf("lookup failed: %w", domain.ErrJobNotFound)
		},
	}
	handler := newTestHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+id.String()+"/attempts", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusNotFound, "job_not_found")
}

func TestGetJobAttemptsRejectsMalformedID(t *testing.T) {
	listCalls := 0
	store := &fakeJobStore{
		listJobAttempts: func(context.Context, domain.JobID) ([]domain.Attempt, error) {
			listCalls++
			return nil, errors.New("unexpected store call")
		},
	}
	handler := newTestHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/not-a-uuid/attempts", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusBadRequest, "invalid_job_id")
	if listCalls != 0 {
		t.Fatalf("store list calls = %d, want 0", listCalls)
	}
}

func TestGetJobAttemptsReturnsGenericInternalError(t *testing.T) {
	id := domain.NewJobID()
	store := &fakeJobStore{
		listJobAttempts: func(context.Context, domain.JobID) ([]domain.Attempt, error) {
			return nil, errors.New("database password secret")
		},
	}
	handler := newTestHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+id.String()+"/attempts", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusInternalServerError, "internal_error")
	if strings.Contains(response.Body.String(), "database password secret") {
		t.Fatal("response exposed the internal store error")
	}
}

func TestGetJobRejectsMalformedID(t *testing.T) {
	getCalls := 0
	store := &fakeJobStore{
		getJob: func(context.Context, domain.JobID) (domain.Job, error) {
			getCalls++
			return domain.Job{}, errors.New("unexpected store call")
		},
	}
	handler := newTestHandler(store)
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
	handler := newTestHandler(store)
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
	handler := newTestHandler(store)
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
