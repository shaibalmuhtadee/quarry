package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidAttemptNumber = errors.New("invalid attempt number")
	ErrInvalidAttemptStatus = errors.New("invalid attempt status")
)

type AttemptNumber struct {
	value int32
}

func NewAttemptNumber(value int32) (AttemptNumber, error) {
	if value <= 0 {
		return AttemptNumber{}, fmt.Errorf("%w: must be positive", ErrInvalidAttemptNumber)
	}

	return AttemptNumber{value: value}, nil
}

func (number AttemptNumber) Int32() int32 {
	return number.value
}

func (number AttemptNumber) IsZero() bool {
	return number.value == 0
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
		return "", fmt.Errorf("%w: %q", ErrInvalidAttemptStatus, value)
	}
}

type Attempt struct {
	JobID      JobID
	Number     AttemptNumber
	WorkerID   WorkerID
	Status     AttemptStatus
	StartedAt  time.Time
	FinishedAt *time.Time
}
