package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

const (
	maxRequestBodyBytes    = 1 << 20
	maxTimeoutMilliseconds = int64(9_223_372_036_854)
)

type JobStore interface {
	SubmitJob(context.Context, domain.JobSubmission) (domain.JobSubmissionResult, error)
	GetJob(context.Context, domain.JobID) (domain.Job, error)
	ListJobAttempts(context.Context, domain.JobID) ([]domain.Attempt, error)
}

type handler struct {
	store     JobStore
	readiness ReadinessChecker
}

func NewHandler(store JobStore, readiness ReadinessChecker, logger *slog.Logger) http.Handler {
	handler := &handler{
		store:     store,
		readiness: readiness,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", handler.createJob)
	mux.HandleFunc("GET /v1/jobs/{id}", handler.getJob)
	mux.HandleFunc("GET /v1/jobs/{id}/attempts", handler.getJobAttempts)
	mux.HandleFunc("GET /healthz", handler.health)
	mux.HandleFunc("GET /readyz", handler.ready)

	return logRequests(logger, mux)
}

type createJobRequest struct {
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	MaxAttempts json.RawMessage `json:"max_attempts"`
	TimeoutMS   json.RawMessage `json:"timeout_ms"`
}

type createJobResponse struct {
	ID           string           `json:"id"`
	Status       domain.JobStatus `json:"status"`
	Deduplicated bool             `json:"deduplicated"`
	CreatedAt    time.Time        `json:"created_at"`
}

type jobResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Status       domain.JobStatus `json:"status"`
	AttemptCount int32            `json:"attempt_count"`
	MaxAttempts  int32            `json:"max_attempts"`
	TimeoutMS    int64            `json:"timeout_ms"`
	Result       json.RawMessage  `json:"result"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	FinishedAt   *time.Time       `json:"finished_at"`
}

type jobAttemptsResponse struct {
	Attempts []attemptResponse `json:"attempts"`
}

type attemptResponse struct {
	Number       int32                `json:"attempt_no"`
	WorkerID     string               `json:"worker_id"`
	Status       domain.AttemptStatus `json:"status"`
	ErrorCode    *string              `json:"error_code"`
	ErrorMessage *string              `json:"error_message"`
	StartedAt    time.Time            `json:"started_at"`
	FinishedAt   *time.Time           `json:"finished_at"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (handler *handler) createJob(writer http.ResponseWriter, request *http.Request) {
	input, ok := decodeCreateJobRequest(writer, request)
	if !ok {
		return
	}

	jobType, err := domain.ParseJobType(input.Type)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_job_type", "type must be a valid job type")
		return
	}
	payload, err := domain.ParsePayload(input.Payload)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "payload must contain one JSON value")
		return
	}

	maxAttempts := domain.DefaultMaxAttempts
	if input.MaxAttempts != nil {
		var explicitMaxAttempts *int32
		if err := json.Unmarshal(input.MaxAttempts, &explicitMaxAttempts); err != nil ||
			explicitMaxAttempts == nil || *explicitMaxAttempts <= 0 {
			writeError(writer, http.StatusBadRequest, "invalid_max_attempts", "max_attempts must be a positive integer")
			return
		}
		maxAttempts = *explicitMaxAttempts
	}

	var timeoutMilliseconds int64
	if err := json.Unmarshal(input.TimeoutMS, &timeoutMilliseconds); err != nil ||
		timeoutMilliseconds <= 0 || timeoutMilliseconds > maxTimeoutMilliseconds {
		writeError(writer, http.StatusBadRequest, "invalid_timeout", "timeout_ms must be a positive integer that fits time.Duration")
		return
	}

	submission, err := domain.NewJobSubmission(
		jobType,
		payload,
		maxAttempts,
		time.Duration(timeoutMilliseconds)*time.Millisecond,
	)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "job submission is invalid")
		return
	}
	if values, exists := request.Header[http.CanonicalHeaderKey("Idempotency-Key")]; exists {
		if len(values) != 1 {
			writeError(writer, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must contain one non-empty value of at most 255 bytes")
			return
		}
		key, err := domain.ParseIdempotencyKey(values[0])
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must contain one non-empty value of at most 255 bytes")
			return
		}
		submission, err = submission.WithIdempotencyKey(key)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "job submission is invalid")
			return
		}
	}

	result, err := handler.store.SubmitJob(request.Context(), submission)
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		writeError(writer, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different job submission")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	job := result.Job
	setRequestJobID(request, job.ID.String())

	writer.Header().Set("Location", "/v1/jobs/"+job.ID.String())
	status := http.StatusCreated
	if result.Deduplicated {
		status = http.StatusOK
	}
	writeJSON(writer, status, createJobResponse{
		ID:           job.ID.String(),
		Status:       job.Status,
		Deduplicated: result.Deduplicated,
		CreatedAt:    job.CreatedAt,
	})
}

func decodeCreateJobRequest(writer http.ResponseWriter, request *http.Request) (createJobRequest, bool) {
	limitedBody := http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(limitedBody)
	decoder.DisallowUnknownFields()

	var input *createJobRequest
	if err := decoder.Decode(&input); err != nil || input == nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return createJobRequest{}, false
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return createJobRequest{}, false
	}

	return *input, true
}

func (handler *handler) getJob(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseJobID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_job_id", "job ID must be a valid UUID")
		return
	}
	setRequestJobID(request, id.String())

	job, err := handler.store.GetJob(request.Context(), id)
	if errors.Is(err, domain.ErrJobNotFound) {
		writeError(writer, http.StatusNotFound, "job_not_found", "job not found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	var result json.RawMessage
	if job.Result != nil {
		result = job.Result.JSON()
	}
	writeJSON(writer, http.StatusOK, jobResponse{
		ID:           job.ID.String(),
		Type:         job.Type.String(),
		Status:       job.Status,
		AttemptCount: job.AttemptCount,
		MaxAttempts:  job.MaxAttempts,
		TimeoutMS:    job.Timeout.Milliseconds(),
		Result:       result,
		CreatedAt:    job.CreatedAt,
		UpdatedAt:    job.UpdatedAt,
		FinishedAt:   job.FinishedAt,
	})
}

func (handler *handler) getJobAttempts(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseJobID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_job_id", "job ID must be a valid UUID")
		return
	}
	setRequestJobID(request, id.String())

	attempts, err := handler.store.ListJobAttempts(request.Context(), id)
	if errors.Is(err, domain.ErrJobNotFound) {
		writeError(writer, http.StatusNotFound, "job_not_found", "job not found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	response := jobAttemptsResponse{
		Attempts: make([]attemptResponse, 0, len(attempts)),
	}
	for _, attempt := range attempts {
		var errorCode, errorMessage *string
		if attempt.Failure != nil {
			code := attempt.Failure.Code()
			message := attempt.Failure.Message()
			errorCode = &code
			errorMessage = &message
		}
		response.Attempts = append(response.Attempts, attemptResponse{
			Number:       attempt.Number.Int32(),
			WorkerID:     attempt.WorkerID.String(),
			Status:       attempt.Status,
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
			StartedAt:    attempt.StartedAt,
			FinishedAt:   attempt.FinishedAt,
		})
	}

	writeJSON(writer, http.StatusOK, response)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
