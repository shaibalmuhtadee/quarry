package domain

import (
	"errors"
	"fmt"
	"time"
)

const (
	DefaultRetryBaseDelay = time.Second
	DefaultRetryMaxDelay  = 60 * time.Second
)

var (
	ErrInvalidRetryPolicy = errors.New("invalid retry policy")
	ErrInvalidJitterValue = errors.New("invalid jitter value")
)

type RandomInt64N func(upperExclusive int64) int64

type RetryPolicy struct {
	baseDelay    time.Duration
	maxDelay     time.Duration
	randomInt64N RandomInt64N
}

func NewRetryPolicy(
	baseDelay time.Duration,
	maxDelay time.Duration,
	randomInt64N RandomInt64N,
) (RetryPolicy, error) {
	if baseDelay <= 0 || baseDelay%time.Millisecond != 0 {
		return RetryPolicy{}, fmt.Errorf("%w: base delay must be a positive whole number of milliseconds", ErrInvalidRetryPolicy)
	}
	if maxDelay < baseDelay || maxDelay%time.Millisecond != 0 {
		return RetryPolicy{}, fmt.Errorf("%w: maximum delay must be a whole number of milliseconds no less than the base delay", ErrInvalidRetryPolicy)
	}
	if randomInt64N == nil {
		return RetryPolicy{}, fmt.Errorf("%w: random source is required", ErrInvalidRetryPolicy)
	}

	return RetryPolicy{
		baseDelay:    baseDelay,
		maxDelay:     maxDelay,
		randomInt64N: randomInt64N,
	}, nil
}

func (policy RetryPolicy) BackoffCap(attemptNumber AttemptNumber) (time.Duration, error) {
	if err := policy.validate(); err != nil {
		return 0, err
	}
	if attemptNumber.IsZero() {
		return 0, ErrInvalidAttemptNumber
	}

	cap := policy.baseDelay
	for remaining := attemptNumber.Int32() - 1; remaining > 0 && cap < policy.maxDelay; remaining-- {
		if cap > policy.maxDelay/2 {
			cap = policy.maxDelay
			continue
		}
		cap *= 2
		if cap > policy.maxDelay {
			cap = policy.maxDelay
		}
	}

	return cap, nil
}

func (policy RetryPolicy) Delay(attemptNumber AttemptNumber) (time.Duration, error) {
	cap, err := policy.BackoffCap(attemptNumber)
	if err != nil {
		return 0, err
	}

	upperExclusive := cap.Milliseconds() + 1
	drawnMilliseconds := policy.randomInt64N(upperExclusive)
	if drawnMilliseconds < 0 || drawnMilliseconds >= upperExclusive {
		return 0, fmt.Errorf(
			"%w: source returned %d for upper bound %d",
			ErrInvalidJitterValue,
			drawnMilliseconds,
			upperExclusive,
		)
	}

	return time.Duration(drawnMilliseconds) * time.Millisecond, nil
}

func (policy RetryPolicy) validate() error {
	if policy.baseDelay <= 0 ||
		policy.baseDelay%time.Millisecond != 0 ||
		policy.maxDelay < policy.baseDelay ||
		policy.maxDelay%time.Millisecond != 0 ||
		policy.randomInt64N == nil {
		return ErrInvalidRetryPolicy
	}
	return nil
}
