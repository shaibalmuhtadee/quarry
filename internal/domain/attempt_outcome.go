package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	MaxAttemptFailureCodeLength    = 64
	MaxAttemptFailureMessageLength = 1024
)

var (
	ErrInvalidAttemptFailure     = errors.New("invalid attempt failure")
	ErrInvalidAttemptOutcome     = errors.New("invalid attempt outcome")
	ErrInvalidAttemptOutcomeKind = errors.New("invalid attempt outcome kind")

	attemptFailureCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
)

type AttemptFailure struct {
	code    string
	message string
}

func NewAttemptFailure(code, message string) (AttemptFailure, error) {
	if len(code) == 0 || len(code) > MaxAttemptFailureCodeLength || !attemptFailureCodePattern.MatchString(code) {
		return AttemptFailure{}, fmt.Errorf("%w: code %q must be lower snake case and at most %d bytes", ErrInvalidAttemptFailure, code, MaxAttemptFailureCodeLength)
	}
	if strings.TrimSpace(message) == "" || len(message) > MaxAttemptFailureMessageLength {
		return AttemptFailure{}, fmt.Errorf("%w: message must contain non-whitespace text and be at most %d bytes", ErrInvalidAttemptFailure, MaxAttemptFailureMessageLength)
	}

	return AttemptFailure{code: code, message: message}, nil
}

func (failure AttemptFailure) Code() string {
	return failure.code
}

func (failure AttemptFailure) Message() string {
	return failure.message
}

func (failure AttemptFailure) IsZero() bool {
	return failure.code == "" && failure.message == ""
}

type AttemptOutcomeKind string

const (
	AttemptOutcomeKindSucceeded        AttemptOutcomeKind = "succeeded"
	AttemptOutcomeKindRetryableFailure AttemptOutcomeKind = "retryable_failure"
	AttemptOutcomeKindPermanentFailure AttemptOutcomeKind = "permanent_failure"
	AttemptOutcomeKindCancelled        AttemptOutcomeKind = "cancelled"
	AttemptOutcomeKindTimedOut         AttemptOutcomeKind = "timed_out"
	AttemptOutcomeKindPanicked         AttemptOutcomeKind = "panicked"
)

func ParseAttemptOutcomeKind(value string) (AttemptOutcomeKind, error) {
	kind := AttemptOutcomeKind(value)
	switch kind {
	case AttemptOutcomeKindSucceeded,
		AttemptOutcomeKindRetryableFailure,
		AttemptOutcomeKindPermanentFailure,
		AttemptOutcomeKindCancelled,
		AttemptOutcomeKindTimedOut,
		AttemptOutcomeKindPanicked:
		return kind, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidAttemptOutcomeKind, value)
	}
}

type AttemptOutcome struct {
	kind    AttemptOutcomeKind
	result  Result
	failure AttemptFailure
}

func NewSucceededOutcome(result Result) (AttemptOutcome, error) {
	if len(result.value) == 0 {
		return AttemptOutcome{}, fmt.Errorf("%w: success result is required", ErrInvalidAttemptOutcome)
	}

	return AttemptOutcome{
		kind:   AttemptOutcomeKindSucceeded,
		result: result,
	}, nil
}

func NewRetryableFailureOutcome(failure AttemptFailure) (AttemptOutcome, error) {
	return newFailureOutcome(AttemptOutcomeKindRetryableFailure, failure)
}

func NewPermanentFailureOutcome(failure AttemptFailure) (AttemptOutcome, error) {
	return newFailureOutcome(AttemptOutcomeKindPermanentFailure, failure)
}

func NewCancelledOutcome(failure AttemptFailure) (AttemptOutcome, error) {
	return newFailureOutcome(AttemptOutcomeKindCancelled, failure)
}

func NewTimedOutOutcome(failure AttemptFailure) (AttemptOutcome, error) {
	return newFailureOutcome(AttemptOutcomeKindTimedOut, failure)
}

func NewPanickedOutcome(failure AttemptFailure) (AttemptOutcome, error) {
	return newFailureOutcome(AttemptOutcomeKindPanicked, failure)
}

func newFailureOutcome(kind AttemptOutcomeKind, failure AttemptFailure) (AttemptOutcome, error) {
	if failure.IsZero() {
		return AttemptOutcome{}, fmt.Errorf("%w: failure details are required", ErrInvalidAttemptOutcome)
	}

	return AttemptOutcome{
		kind:    kind,
		failure: failure,
	}, nil
}

func (outcome AttemptOutcome) Kind() AttemptOutcomeKind {
	return outcome.kind
}

func (outcome AttemptOutcome) Result() (Result, bool) {
	return outcome.result, outcome.kind == AttemptOutcomeKindSucceeded
}

func (outcome AttemptOutcome) Failure() (AttemptFailure, bool) {
	return outcome.failure, outcome.kind != "" && outcome.kind != AttemptOutcomeKindSucceeded
}

func (outcome AttemptOutcome) IsZero() bool {
	return outcome.kind == ""
}
