package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

const (
	maxRequestBodyBytes    = 1 << 20
	maxTimeoutMilliseconds = int64(9_223_372_036_854)
)

type JobStore interface {
	CreateJob(context.Context, domain.JobSubmission) (domain.Job, error)
	GetJob(context.Context, domain.JobID) (domain.Job, error)
}

type handler struct {
	store JobStore
}

func NewHandler(store JobStore) http.Handler {
	handler := &handler{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", handler.createJob)
	mux.HandleFunc("GET /v1/jobs/{id}", handler.getJob)

	return mux
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
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
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

	job, err := handler.store.CreateJob(request.Context(), submission)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	writer.Header().Set("Location", "/v1/jobs/"+job.ID.String())
	writeJSON(writer, http.StatusCreated, createJobResponse{
		ID:           job.ID.String(),
		Status:       job.Status,
		Deduplicated: false,
		CreatedAt:    job.CreatedAt,
	})
}

func decodeCreateJobRequest(writer http.ResponseWriter, request *http.Request) (createJobRequest, bool) {
	limitedBody := http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(limitedBody)
	decoder.DisallowUnknownFields()

	var input createJobRequest
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return createJobRequest{}, false
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must contain one JSON object")
		return createJobRequest{}, false
	}

	return input, true
}

func (handler *handler) getJob(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseJobID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_job_id", "job ID must be a valid UUID")
		return
	}

	job, err := handler.store.GetJob(request.Context(), id)
	if errors.Is(err, domain.ErrJobNotFound) {
		writeError(writer, http.StatusNotFound, "job_not_found", "job not found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	writeJSON(writer, http.StatusOK, jobResponse{
		ID:           job.ID.String(),
		Type:         job.Type.String(),
		Status:       job.Status,
		AttemptCount: job.AttemptCount,
		MaxAttempts:  job.MaxAttempts,
		TimeoutMS:    job.Timeout.Milliseconds(),
		CreatedAt:    job.CreatedAt,
		UpdatedAt:    job.UpdatedAt,
	})
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
