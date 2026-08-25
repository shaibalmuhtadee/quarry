package worker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestHandlerErrorClassifiesRetryableAndPermanentFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("internal dependency address and token")
	tests := []struct {
		name        string
		constructor func(string, string, error) (*HandlerError, error)
		wantKind    domain.AttemptOutcomeKind
	}{
		{name: "retryable", constructor: NewRetryableHandlerError, wantKind: domain.AttemptOutcomeKindRetryableFailure},
		{name: "permanent", constructor: NewPermanentHandlerError, wantKind: domain.AttemptOutcomeKindPermanentFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handlerErr, err := test.constructor("dependency_timeout", "dependency timed out", cause)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := classifyHandlerResult(domain.Result{}, fmt.Errorf("handler wrapper: %w", handlerErr))
			if err != nil {
				t.Fatal(err)
			}
			failure, ok := outcome.Failure()
			if outcome.Kind() != test.wantKind || !ok || failure.Code() != "dependency_timeout" || failure.Message() != "dependency timed out" {
				t.Fatalf("classified outcome = %#v", outcome)
			}
			if !errors.Is(handlerErr, cause) {
				t.Fatal("handler error did not preserve its internal cause")
			}
		})
	}
}

func TestHandlerErrorRejectsInvalidSafeDetails(t *testing.T) {
	t.Parallel()

	if _, err := NewRetryableHandlerError("BAD-CODE", "safe", nil); !errors.Is(err, domain.ErrInvalidAttemptFailure) {
		t.Fatalf("constructor error = %v, want ErrInvalidAttemptFailure", err)
	}
}

func TestClassifyHandlerResultMakesUnknownErrorGenericAndPermanent(t *testing.T) {
	t.Parallel()

	outcome, err := classifyHandlerResult(domain.Result{}, errors.New("database password secret"))
	if err != nil {
		t.Fatal(err)
	}
	failure, ok := outcome.Failure()
	if outcome.Kind() != domain.AttemptOutcomeKindPermanentFailure || !ok ||
		failure.Code() != unclassifiedHandlerErrorCode || failure.Message() != unclassifiedHandlerErrorMessage {
		t.Fatalf("classified outcome = %#v", outcome)
	}
	if failure.Message() == "database password secret" {
		t.Fatal("unclassified outcome exposed the handler error")
	}
}
