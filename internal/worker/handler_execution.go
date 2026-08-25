package worker

import (
	"context"
	"runtime/debug"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

const (
	executionTimeoutCode         = "execution_timeout"
	executionTimeoutMessage      = "handler execution timed out"
	handlerPanickedCode          = "handler_panicked"
	handlerPanickedMessage       = "handler panicked"
	cancellationRequestedCode    = "cancellation_requested"
	cancellationRequestedMessage = "job cancellation was requested"
)

type handlerExecution struct {
	result     domain.Result
	err        error
	panicked   bool
	panicValue any
	stack      []byte
}

func invokeHandler(ctx context.Context, handler Handler, payload domain.Payload) (execution handlerExecution) {
	returned := false
	defer func() {
		if returned {
			return
		}
		execution.panicked = true
		execution.panicValue = recover()
		execution.stack = debug.Stack()
	}()

	execution.result, execution.err = handler(ctx, payload)
	returned = true
	return execution
}

func timedOutOutcome() (domain.AttemptOutcome, error) {
	failure, err := domain.NewAttemptFailure(executionTimeoutCode, executionTimeoutMessage)
	if err != nil {
		return domain.AttemptOutcome{}, err
	}
	return domain.NewTimedOutOutcome(failure)
}

func panickedOutcome() (domain.AttemptOutcome, error) {
	failure, err := domain.NewAttemptFailure(handlerPanickedCode, handlerPanickedMessage)
	if err != nil {
		return domain.AttemptOutcome{}, err
	}
	return domain.NewPanickedOutcome(failure)
}

func cancelledOutcome() (domain.AttemptOutcome, error) {
	failure, err := domain.NewAttemptFailure(cancellationRequestedCode, cancellationRequestedMessage)
	if err != nil {
		return domain.AttemptOutcome{}, err
	}
	return domain.NewCancelledOutcome(failure)
}
