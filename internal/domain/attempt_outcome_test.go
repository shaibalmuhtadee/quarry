package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestAttemptFailure(t *testing.T) {
	t.Parallel()

	failure, err := domain.NewAttemptFailure("dependency_timeout", "dependency did not respond")
	if err != nil {
		t.Fatalf("NewAttemptFailure() error = %v", err)
	}
	if failure.Code() != "dependency_timeout" {
		t.Fatalf("Code() = %q, want dependency_timeout", failure.Code())
	}
	if failure.Message() != "dependency did not respond" {
		t.Fatalf("Message() = %q, want dependency did not respond", failure.Message())
	}
	if failure.IsZero() {
		t.Fatal("valid failure reported zero")
	}
}

func TestAttemptFailureRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		code    string
		message string
	}{
		{name: "missing code", message: "safe"},
		{name: "uppercase code", code: "DEPENDENCY_TIMEOUT", message: "safe"},
		{name: "invalid separator", code: "dependency-timeout", message: "safe"},
		{name: "long code", code: "a" + strings.Repeat("b", domain.MaxAttemptFailureCodeLength), message: "safe"},
		{name: "missing message", code: "handler_error"},
		{name: "whitespace message", code: "handler_error", message: " \t\n"},
		{name: "long message", code: "handler_error", message: strings.Repeat("x", domain.MaxAttemptFailureMessageLength+1)},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewAttemptFailure(test.code, test.message)
			if !errors.Is(err, domain.ErrInvalidAttemptFailure) {
				t.Fatalf("NewAttemptFailure() error = %v, want ErrInvalidAttemptFailure", err)
			}
		})
	}
}

func TestAttemptOutcomeConstructors(t *testing.T) {
	t.Parallel()

	result, err := domain.ParseResult([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	failure, err := domain.NewAttemptFailure("handler_error", "handler failed")
	if err != nil {
		t.Fatalf("NewAttemptFailure() error = %v", err)
	}

	tests := []struct {
		name        string
		kind        domain.AttemptOutcomeKind
		constructor func() (domain.AttemptOutcome, error)
		wantResult  bool
	}{
		{name: "succeeded", kind: domain.AttemptOutcomeKindSucceeded, constructor: func() (domain.AttemptOutcome, error) {
			return domain.NewSucceededOutcome(result)
		}, wantResult: true},
		{name: "retryable failure", kind: domain.AttemptOutcomeKindRetryableFailure, constructor: func() (domain.AttemptOutcome, error) {
			return domain.NewRetryableFailureOutcome(failure)
		}},
		{name: "permanent failure", kind: domain.AttemptOutcomeKindPermanentFailure, constructor: func() (domain.AttemptOutcome, error) {
			return domain.NewPermanentFailureOutcome(failure)
		}},
		{name: "cancelled", kind: domain.AttemptOutcomeKindCancelled, constructor: func() (domain.AttemptOutcome, error) {
			return domain.NewCancelledOutcome(failure)
		}},
		{name: "timed out", kind: domain.AttemptOutcomeKindTimedOut, constructor: func() (domain.AttemptOutcome, error) {
			return domain.NewTimedOutOutcome(failure)
		}},
		{name: "panicked", kind: domain.AttemptOutcomeKindPanicked, constructor: func() (domain.AttemptOutcome, error) {
			return domain.NewPanickedOutcome(failure)
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			outcome, err := test.constructor()
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			if outcome.Kind() != test.kind {
				t.Fatalf("Kind() = %q, want %q", outcome.Kind(), test.kind)
			}
			if outcome.IsZero() {
				t.Fatal("valid outcome reported zero")
			}
			_, hasResult := outcome.Result()
			if hasResult != test.wantResult {
				t.Fatalf("Result() present = %t, want %t", hasResult, test.wantResult)
			}
			_, hasFailure := outcome.Failure()
			if hasFailure == test.wantResult {
				t.Fatalf("Failure() present = %t, want %t", hasFailure, !test.wantResult)
			}
		})
	}
}

func TestAttemptOutcomeConstructorsRejectMissingPayload(t *testing.T) {
	t.Parallel()

	if _, err := domain.NewSucceededOutcome(domain.Result{}); !errors.Is(err, domain.ErrInvalidAttemptOutcome) {
		t.Fatalf("NewSucceededOutcome() error = %v, want ErrInvalidAttemptOutcome", err)
	}

	constructors := []func(domain.AttemptFailure) (domain.AttemptOutcome, error){
		domain.NewRetryableFailureOutcome,
		domain.NewPermanentFailureOutcome,
		domain.NewCancelledOutcome,
		domain.NewTimedOutOutcome,
		domain.NewPanickedOutcome,
	}
	for _, constructor := range constructors {
		if _, err := constructor(domain.AttemptFailure{}); !errors.Is(err, domain.ErrInvalidAttemptOutcome) {
			t.Fatalf("failure constructor error = %v, want ErrInvalidAttemptOutcome", err)
		}
	}
}

func TestAttemptOutcomeKind(t *testing.T) {
	t.Parallel()

	kinds := []domain.AttemptOutcomeKind{
		domain.AttemptOutcomeKindSucceeded,
		domain.AttemptOutcomeKindRetryableFailure,
		domain.AttemptOutcomeKindPermanentFailure,
		domain.AttemptOutcomeKindCancelled,
		domain.AttemptOutcomeKindTimedOut,
		domain.AttemptOutcomeKindPanicked,
	}
	for _, kind := range kinds {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			parsed, err := domain.ParseAttemptOutcomeKind(string(kind))
			if err != nil {
				t.Fatalf("ParseAttemptOutcomeKind(%q) error = %v", kind, err)
			}
			if parsed != kind {
				t.Fatalf("ParseAttemptOutcomeKind(%q) = %q, want %q", kind, parsed, kind)
			}
		})
	}

	if _, err := domain.ParseAttemptOutcomeKind("abandoned"); !errors.Is(err, domain.ErrInvalidAttemptOutcomeKind) {
		t.Fatalf("ParseAttemptOutcomeKind(abandoned) error = %v, want ErrInvalidAttemptOutcomeKind", err)
	}
}
