package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shaibalmuhtadee/quarry/internal/loadgen"
)

const maximumResponseBytes = 1 << 20

type httpAPIClient struct {
	baseURL *url.URL
	client  *http.Client
}

type submitJobRequest struct {
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	MaxAttempts int32           `json:"max_attempts"`
	TimeoutMS   int64           `json:"timeout_ms"`
}

type submitJobResponse struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	Deduplicated bool      `json:"deduplicated"`
	CreatedAt    time.Time `json:"created_at"`
}

type jobResponse struct {
	ID                string          `json:"id"`
	Type              string          `json:"type"`
	Status            string          `json:"status"`
	AttemptCount      int32           `json:"attempt_count"`
	MaxAttempts       int32           `json:"max_attempts"`
	TimeoutMS         int64           `json:"timeout_ms"`
	Result            json.RawMessage `json:"result"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	FinishedAt        *time.Time      `json:"finished_at"`
	CancelRequestedAt *time.Time      `json:"cancel_requested_at"`
	LatestFailure     *struct {
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"latest_failure,omitempty"`
}

type attemptsResponse struct {
	Attempts []struct {
		Number       int32      `json:"attempt_no"`
		WorkerID     string     `json:"worker_id"`
		Status       string     `json:"status"`
		ErrorCode    *string    `json:"error_code"`
		ErrorMessage *string    `json:"error_message"`
		StartedAt    time.Time  `json:"started_at"`
		FinishedAt   *time.Time `json:"finished_at"`
	} `json:"attempts"`
}

type apiErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newHTTPAPIClient(rawBaseURL string, client *http.Client) (*httpAPIClient, error) {
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse API URL: %w", err)
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return nil, errors.New("API URL must use http or https and include a host")
	}
	if baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("API URL must not contain a query or fragment")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	return &httpAPIClient{baseURL: baseURL, client: client}, nil
}

func (client *httpAPIClient) SubmitJob(ctx context.Context, submission loadgen.SubmissionRequest) (loadgen.SubmittedJob, error) {
	body, err := json.Marshal(submitJobRequest{
		Type:        submission.JobType,
		Payload:     submission.Payload,
		MaxAttempts: submission.MaxAttempts,
		TimeoutMS:   submission.Timeout.Milliseconds(),
	})
	if err != nil {
		return loadgen.SubmittedJob{}, fmt.Errorf("encode job submission: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint("/v1/jobs"), bytes.NewReader(body))
	if err != nil {
		return loadgen.SubmittedJob{}, fmt.Errorf("create submission request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", submission.IdempotencyKey)

	response, err := client.client.Do(request)
	if err != nil {
		return loadgen.SubmittedJob{}, loadgen.NewClientError(fmt.Errorf("submit job: %w", err), true, true)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return loadgen.SubmittedJob{}, responseError("submit job", response, true)
	}
	var decoded submitJobResponse
	if err := decodeResponse(response, &decoded); err != nil {
		return loadgen.SubmittedJob{}, loadgen.NewClientError(fmt.Errorf("decode submission response: %w", err), false, true)
	}
	status, err := loadgen.ParseJobStatus(decoded.Status)
	if err != nil {
		return loadgen.SubmittedJob{}, loadgen.NewClientError(fmt.Errorf("decode submission response: %w", err), false, true)
	}
	if _, err := uuid.Parse(decoded.ID); err != nil || decoded.CreatedAt.IsZero() {
		return loadgen.SubmittedJob{}, loadgen.NewClientError(errors.New("decode submission response: invalid job ID or creation time"), false, true)
	}
	return loadgen.SubmittedJob{ID: decoded.ID, Status: status, CreatedAt: decoded.CreatedAt}, nil
}

func (client *httpAPIClient) GetJob(ctx context.Context, id string) (loadgen.Job, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint("/v1/jobs/"+url.PathEscape(id)), nil)
	if err != nil {
		return loadgen.Job{}, fmt.Errorf("create job request: %w", err)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return loadgen.Job{}, loadgen.NewClientError(fmt.Errorf("get job: %w", err), true, false)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return loadgen.Job{}, responseError("get job", response, false)
	}
	var decoded jobResponse
	if err := decodeResponse(response, &decoded); err != nil {
		return loadgen.Job{}, fmt.Errorf("decode job response: %w", err)
	}
	status, err := loadgen.ParseJobStatus(decoded.Status)
	if err != nil {
		return loadgen.Job{}, fmt.Errorf("decode job response: %w", err)
	}
	if decoded.ID != id || decoded.CreatedAt.IsZero() {
		return loadgen.Job{}, errors.New("decode job response: mismatched job ID or missing creation time")
	}
	if decoded.Type == "" || decoded.AttemptCount < 0 || decoded.MaxAttempts <= 0 || decoded.TimeoutMS <= 0 || decoded.UpdatedAt.IsZero() {
		return loadgen.Job{}, errors.New("decode job response: invalid job metadata")
	}
	if status.Terminal() != (decoded.FinishedAt != nil) {
		return loadgen.Job{}, errors.New("decode job response: status and finished_at disagree")
	}
	if decoded.FinishedAt != nil && decoded.FinishedAt.Before(decoded.CreatedAt) {
		return loadgen.Job{}, errors.New("decode job response: finished_at precedes created_at")
	}
	return loadgen.Job{ID: decoded.ID, Status: status, CreatedAt: decoded.CreatedAt, FinishedAt: decoded.FinishedAt}, nil
}

func (client *httpAPIClient) GetJobAttempts(ctx context.Context, id string) ([]loadgen.Attempt, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint("/v1/jobs/"+url.PathEscape(id)+"/attempts"), nil)
	if err != nil {
		return nil, fmt.Errorf("create attempt-history request: %w", err)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, loadgen.NewClientError(fmt.Errorf("get job attempts: %w", err), true, false)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError("get job attempts", response, false)
	}
	var decoded attemptsResponse
	if err := decodeResponse(response, &decoded); err != nil {
		return nil, fmt.Errorf("decode attempt-history response: %w", err)
	}
	if decoded.Attempts == nil {
		return nil, errors.New("decode attempt-history response: attempts must be an array")
	}
	attempts := make([]loadgen.Attempt, 0, len(decoded.Attempts))
	for index, value := range decoded.Attempts {
		status, err := loadgen.ParseAttemptStatus(value.Status)
		if err != nil {
			return nil, fmt.Errorf("decode attempt %d: %w", index, err)
		}
		if value.Number <= 0 || value.StartedAt.IsZero() {
			return nil, fmt.Errorf("decode attempt %d: invalid number or start time", index)
		}
		if _, err := uuid.Parse(value.WorkerID); err != nil {
			return nil, fmt.Errorf("decode attempt %d: invalid worker ID", index)
		}
		if (value.ErrorCode == nil) != (value.ErrorMessage == nil) {
			return nil, fmt.Errorf("decode attempt %d: error code and message must both be present or absent", index)
		}
		if status == loadgen.AttemptStatusRunning && value.FinishedAt != nil {
			return nil, fmt.Errorf("decode attempt %d: running attempt has finished_at", index)
		}
		if status != loadgen.AttemptStatusRunning && value.FinishedAt == nil {
			return nil, fmt.Errorf("decode attempt %d: terminal attempt omitted finished_at", index)
		}
		attempts = append(attempts, loadgen.Attempt{
			Number:       value.Number,
			WorkerID:     value.WorkerID,
			Status:       status,
			ErrorCode:    value.ErrorCode,
			ErrorMessage: value.ErrorMessage,
			StartedAt:    value.StartedAt,
			FinishedAt:   value.FinishedAt,
		})
	}
	return attempts, nil
}

func (client *httpAPIClient) endpoint(path string) string {
	copy := *client.baseURL
	copy.Path += path
	return copy.String()
}

func decodeResponse(response *http.Response, output any) error {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("response Content-Type must be application/json")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maximumResponseBytes {
		return errors.New("response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("response must contain one JSON value")
	}
	return nil
}

func responseError(operation string, response *http.Response, ambiguous bool) error {
	retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	message := response.Status
	var decoded apiErrorResponse
	if err := decodeResponse(response, &decoded); err == nil && decoded.Error.Code != "" {
		message = decoded.Error.Code + ": " + decoded.Error.Message
	}
	return loadgen.NewClientError(
		fmt.Errorf("%s returned HTTP %d: %s", operation, response.StatusCode, message),
		retryable,
		ambiguous && retryable,
	)
}
