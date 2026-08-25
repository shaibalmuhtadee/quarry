package worker

import (
	"errors"
	"fmt"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

const (
	unclassifiedHandlerErrorCode    = "handler_error"
	unclassifiedHandlerErrorMessage = "handler failed"
)

type HandlerError struct {
	outcome domain.AttemptOutcome
	cause   error
}

func NewRetryableHandlerError(code, message string, cause error) (*HandlerError, error) {
	return newHandlerError(code, message, cause, domain.NewRetryableFailureOutcome)
}

func NewPermanentHandlerError(code, message string, cause error) (*HandlerError, error) {
	return newHandlerError(code, message, cause, domain.NewPermanentFailureOutcome)
}

func newHandlerError(
	code string,
	message string,
	cause error,
	constructor func(domain.AttemptFailure) (domain.AttemptOutcome, error),
) (*HandlerError, error) {
	failure, err := domain.NewAttemptFailure(code, message)
	if err != nil {
		return nil, err
	}
	outcome, err := constructor(failure)
	if err != nil {
		return nil, err
	}
	return &HandlerError{outcome: outcome, cause: cause}, nil
}

func (failure *HandlerError) Error() string {
	if failure == nil {
		return "handler failure"
	}
	if failure.cause != nil {
		return failure.cause.Error()
	}
	details, ok := failure.outcome.Failure()
	if !ok {
		return "handler failure"
	}
	return details.Message()
}

func (failure *HandlerError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func classifyHandlerResult(result domain.Result, handlerErr error) (domain.AttemptOutcome, error) {
	if handlerErr == nil {
		return domain.NewSucceededOutcome(result)
	}

	var classified *HandlerError
	if errors.As(handlerErr, &classified) && classified != nil && !classified.outcome.IsZero() {
		return classified.outcome, nil
	}

	failure, err := domain.NewAttemptFailure(
		unclassifiedHandlerErrorCode,
		unclassifiedHandlerErrorMessage,
	)
	if err != nil {
		return domain.AttemptOutcome{}, fmt.Errorf("create unclassified handler failure: %w", err)
	}
	outcome, err := domain.NewPermanentFailureOutcome(failure)
	if err != nil {
		return domain.AttemptOutcome{}, fmt.Errorf("create unclassified handler outcome: %w", err)
	}
	return outcome, nil
}
