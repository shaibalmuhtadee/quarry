package loadgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const SampleSchemaVersion = 1

type Phase string

const (
	PhaseWarmup      Phase = "warmup"
	PhaseMeasurement Phase = "measurement"
)

func parsePhase(value string) (Phase, error) {
	phase := Phase(value)
	switch phase {
	case PhaseWarmup, PhaseMeasurement:
		return phase, nil
	default:
		return "", fmt.Errorf("invalid sample phase %q", value)
	}
}

type JobStatus string

const (
	JobStatusQueued       JobStatus = "queued"
	JobStatusRunning      JobStatus = "running"
	JobStatusRetryWait    JobStatus = "retry_wait"
	JobStatusSucceeded    JobStatus = "succeeded"
	JobStatusDeadLettered JobStatus = "dead_lettered"
	JobStatusCancelled    JobStatus = "cancelled"
)

func ParseJobStatus(value string) (JobStatus, error) {
	status := JobStatus(value)
	switch status {
	case JobStatusQueued,
		JobStatusRunning,
		JobStatusRetryWait,
		JobStatusSucceeded,
		JobStatusDeadLettered,
		JobStatusCancelled:
		return status, nil
	default:
		return "", fmt.Errorf("invalid job status %q", value)
	}
}

func (status JobStatus) Terminal() bool {
	switch status {
	case JobStatusSucceeded, JobStatusDeadLettered, JobStatusCancelled:
		return true
	case JobStatusQueued, JobStatusRunning, JobStatusRetryWait:
		return false
	default:
		return false
	}
}

type AttemptStatus string

const (
	AttemptStatusRunning         AttemptStatus = "running"
	AttemptStatusSucceeded       AttemptStatus = "succeeded"
	AttemptStatusRetryableFailed AttemptStatus = "retryable_failed"
	AttemptStatusPermanentFailed AttemptStatus = "permanent_failed"
	AttemptStatusCancelled       AttemptStatus = "cancelled"
	AttemptStatusTimedOut        AttemptStatus = "timed_out"
	AttemptStatusPanicked        AttemptStatus = "panicked"
	AttemptStatusAbandoned       AttemptStatus = "abandoned"
)

func ParseAttemptStatus(value string) (AttemptStatus, error) {
	status := AttemptStatus(value)
	switch status {
	case AttemptStatusRunning,
		AttemptStatusSucceeded,
		AttemptStatusRetryableFailed,
		AttemptStatusPermanentFailed,
		AttemptStatusCancelled,
		AttemptStatusTimedOut,
		AttemptStatusPanicked,
		AttemptStatusAbandoned:
		return status, nil
	default:
		return "", fmt.Errorf("invalid attempt status %q", value)
	}
}

type Submission struct {
	JobType     string
	Payload     json.RawMessage
	MaxAttempts int32
	Timeout     time.Duration
}

type SubmissionRequest struct {
	Submission
	IdempotencyKey string
}

type SubmittedJob struct {
	ID        string
	Status    JobStatus
	CreatedAt time.Time
}

type Job struct {
	ID         string
	Status     JobStatus
	CreatedAt  time.Time
	FinishedAt *time.Time
}

type Attempt struct {
	Number       int32
	WorkerID     string
	Status       AttemptStatus
	ErrorCode    *string
	ErrorMessage *string
	StartedAt    time.Time
	FinishedAt   *time.Time
}

type Client interface {
	SubmitJob(context.Context, SubmissionRequest) (SubmittedJob, error)
	GetJob(context.Context, string) (Job, error)
	GetJobAttempts(context.Context, string) ([]Attempt, error)
}

type SubmissionFactory func(sequence uint64) Submission

type clientError struct {
	cause     error
	retryable bool
	ambiguous bool
}

func (err *clientError) Error() string {
	return err.cause.Error()
}

func (err *clientError) Unwrap() error {
	return err.cause
}

func NewClientError(err error, retryable, ambiguous bool) error {
	if err == nil {
		return nil
	}
	return &clientError{cause: err, retryable: retryable, ambiguous: ambiguous}
}

func IsRetryable(err error) bool {
	var target *clientError
	return errors.As(err, &target) && target.retryable
}

func IsAmbiguous(err error) bool {
	var target *clientError
	return errors.As(err, &target) && target.ambiguous
}

type RequestOperation string

const (
	OperationSubmit   RequestOperation = "submit"
	OperationPoll     RequestOperation = "poll"
	OperationAttempts RequestOperation = "attempts"
)

type RequestError struct {
	Operation  RequestOperation `json:"operation"`
	ObservedAt time.Time        `json:"observed_at"`
	Retryable  bool             `json:"retryable"`
	Ambiguous  bool             `json:"ambiguous"`
	Message    string           `json:"message"`
}

type AttemptSample struct {
	Number       int32         `json:"attempt_no"`
	WorkerID     string        `json:"worker_id"`
	Status       AttemptStatus `json:"status"`
	ErrorCode    *string       `json:"error_code,omitempty"`
	ErrorMessage *string       `json:"error_message,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   *time.Time    `json:"finished_at,omitempty"`
}

type SampleKind string

const (
	SampleKindSubmissionFailed SampleKind = "submission_failed"
	SampleKindTerminal         SampleKind = "terminal"
	SampleKindIncomplete       SampleKind = "incomplete"
)

type SampleHeader struct {
	Sequence            uint64
	Phase               Phase
	JobType             string
	SubmissionStartedAt time.Time
}

type Sample interface {
	Kind() SampleKind
	Header() SampleHeader
	sample()
}

type SubmissionFailureSample struct {
	Base             SampleHeader
	MayHaveCommitted bool
	Errors           []RequestError
}

func (sample SubmissionFailureSample) Kind() SampleKind     { return SampleKindSubmissionFailed }
func (sample SubmissionFailureSample) Header() SampleHeader { return sample.Base }
func (SubmissionFailureSample) sample()                     {}

type TerminalJobSample struct {
	Base                  SampleHeader
	JobID                 string
	CreatedAt             time.Time
	SubmissionCompletedAt time.Time
	Status                JobStatus
	FinishedAt            time.Time
	TerminalObservedAt    time.Time
	Attempts              []AttemptSample
	Errors                []RequestError
}

func (sample TerminalJobSample) Kind() SampleKind     { return SampleKindTerminal }
func (sample TerminalJobSample) Header() SampleHeader { return sample.Base }
func (TerminalJobSample) sample()                     {}

type IncompleteJobSample struct {
	Base                  SampleHeader
	JobID                 string
	CreatedAt             time.Time
	SubmissionCompletedAt time.Time
	LastStatus            JobStatus
	DrainEndedAt          time.Time
	Errors                []RequestError
}

func (sample IncompleteJobSample) Kind() SampleKind     { return SampleKindIncomplete }
func (sample IncompleteJobSample) Header() SampleHeader { return sample.Base }
func (IncompleteJobSample) sample()                     {}
