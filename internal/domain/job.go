package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const (
	MaxJobTypeLength   = 128
	DefaultMaxAttempts = int32(3)
)

var (
	ErrInvalidJobID       = errors.New("invalid job ID")
	ErrInvalidJobType     = errors.New("invalid job type")
	ErrInvalidPayload     = errors.New("invalid payload")
	ErrInvalidJobStatus   = errors.New("invalid job status")
	ErrInvalidMaxAttempts = errors.New("invalid maximum attempts")
	ErrInvalidTimeout     = errors.New("invalid timeout")
	ErrJobNotFound        = errors.New("job not found")

	jobTypePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
)

type JobID struct {
	value uuid.UUID
}

func NewJobID() JobID {
	return JobID{value: uuid.New()}
}

func ParseJobID(value string) (JobID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return JobID{}, fmt.Errorf("%w: %q", ErrInvalidJobID, value)
	}

	return JobID{value: id}, nil
}

func (id JobID) String() string {
	return id.value.String()
}

func (id JobID) UUID() uuid.UUID {
	return id.value
}

func (id JobID) IsZero() bool {
	return id.value == uuid.Nil
}

func (id JobID) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return nil, ErrInvalidJobID
	}

	return []byte(id.String()), nil
}

type JobType struct {
	value string
}

func ParseJobType(value string) (JobType, error) {
	if len(value) == 0 || len(value) > MaxJobTypeLength || !jobTypePattern.MatchString(value) {
		return JobType{}, fmt.Errorf("%w: %q", ErrInvalidJobType, value)
	}

	return JobType{value: value}, nil
}

func (jobType JobType) String() string {
	return jobType.value
}

func (jobType JobType) MarshalText() ([]byte, error) {
	if jobType.value == "" {
		return nil, ErrInvalidJobType
	}

	return []byte(jobType.value), nil
}

type Payload struct {
	value json.RawMessage
}

func ParsePayload(value json.RawMessage) (Payload, error) {
	if len(value) == 0 {
		return Payload{}, fmt.Errorf("%w: value is required", ErrInvalidPayload)
	}
	if !json.Valid(value) {
		return Payload{}, fmt.Errorf("%w: value must contain one JSON value", ErrInvalidPayload)
	}

	return Payload{value: append(json.RawMessage(nil), value...)}, nil
}

func (payload Payload) JSON() json.RawMessage {
	return append(json.RawMessage(nil), payload.value...)
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
		return "", fmt.Errorf("%w: %q", ErrInvalidJobStatus, value)
	}
}

type Job struct {
	ID           JobID
	Type         JobType
	Payload      Payload
	Result       *Result
	Status       JobStatus
	AttemptCount int32
	MaxAttempts  int32
	Timeout      time.Duration
	CreatedAt    time.Time
	UpdatedAt    time.Time
	FinishedAt   *time.Time
}

type JobSubmission struct {
	id             JobID
	jobType        JobType
	payload        Payload
	maxAttempts    int32
	timeout        time.Duration
	idempotencyKey IdempotencyKey
	requestHash    [32]byte
}

type JobSubmissionResult struct {
	Job          Job
	Deduplicated bool
}

func NewJobSubmission(
	jobType JobType,
	payload Payload,
	maxAttempts int32,
	timeout time.Duration,
) (JobSubmission, error) {
	if jobType.value == "" {
		return JobSubmission{}, ErrInvalidJobType
	}
	if len(payload.value) == 0 {
		return JobSubmission{}, ErrInvalidPayload
	}
	if maxAttempts <= 0 {
		return JobSubmission{}, fmt.Errorf("%w: must be positive", ErrInvalidMaxAttempts)
	}
	if timeout <= 0 || timeout%time.Millisecond != 0 {
		return JobSubmission{}, fmt.Errorf("%w: must be a positive whole number of milliseconds", ErrInvalidTimeout)
	}

	return JobSubmission{
		id:          NewJobID(),
		jobType:     jobType,
		payload:     payload,
		maxAttempts: maxAttempts,
		timeout:     timeout,
	}, nil
}

func (submission JobSubmission) ID() JobID {
	return submission.id
}

func (submission JobSubmission) Type() JobType {
	return submission.jobType
}

func (submission JobSubmission) Payload() Payload {
	return submission.payload
}

func (submission JobSubmission) MaxAttempts() int32 {
	return submission.maxAttempts
}

func (submission JobSubmission) Timeout() time.Duration {
	return submission.timeout
}
