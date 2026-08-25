package domain_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestRetryPolicyBackoffCap(t *testing.T) {
	t.Parallel()

	policy, err := domain.NewRetryPolicy(
		domain.DefaultRetryBaseDelay,
		domain.DefaultRetryMaxDelay,
		func(int64) int64 { return 0 },
	)
	if err != nil {
		t.Fatalf("NewRetryPolicy() error = %v", err)
	}

	tests := []struct {
		attempt int32
		want    time.Duration
	}{
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 5, want: 16 * time.Second},
		{attempt: 6, want: 32 * time.Second},
		{attempt: 7, want: 60 * time.Second},
		{attempt: 8, want: 60 * time.Second},
		{attempt: math.MaxInt32, want: 60 * time.Second},
	}

	for _, test := range tests {
		number, err := domain.NewAttemptNumber(test.attempt)
		if err != nil {
			t.Fatalf("NewAttemptNumber(%d) error = %v", test.attempt, err)
		}
		cap, err := policy.BackoffCap(number)
		if err != nil {
			t.Fatalf("BackoffCap(%d) error = %v", test.attempt, err)
		}
		if cap != test.want {
			t.Fatalf("BackoffCap(%d) = %v, want %v", test.attempt, cap, test.want)
		}
	}
}

func TestRetryPolicyDelayUsesInclusiveMillisecondBounds(t *testing.T) {
	t.Parallel()

	attempt, err := domain.NewAttemptNumber(3)
	if err != nil {
		t.Fatalf("NewAttemptNumber() error = %v", err)
	}

	tests := []struct {
		name string
		draw func(int64) int64
		want time.Duration
	}{
		{name: "zero", draw: func(int64) int64 { return 0 }, want: 0},
		{name: "middle", draw: func(int64) int64 { return 2_000 }, want: 2 * time.Second},
		{name: "cap", draw: func(upperExclusive int64) int64 { return upperExclusive - 1 }, want: 4 * time.Second},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var receivedUpperBound int64
			policy, err := domain.NewRetryPolicy(time.Second, 60*time.Second, func(upperExclusive int64) int64 {
				receivedUpperBound = upperExclusive
				return test.draw(upperExclusive)
			})
			if err != nil {
				t.Fatalf("NewRetryPolicy() error = %v", err)
			}
			delay, err := policy.Delay(attempt)
			if err != nil {
				t.Fatalf("Delay() error = %v", err)
			}
			if receivedUpperBound != 4_001 {
				t.Fatalf("random upper bound = %d, want 4001", receivedUpperBound)
			}
			if delay != test.want {
				t.Fatalf("Delay() = %v, want %v", delay, test.want)
			}
		})
	}
}

func TestRetryPolicyRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		base   time.Duration
		max    time.Duration
		random domain.RandomInt64N
	}{
		{name: "zero base", max: time.Second, random: func(int64) int64 { return 0 }},
		{name: "fractional base", base: time.Millisecond + time.Nanosecond, max: time.Second, random: func(int64) int64 { return 0 }},
		{name: "maximum below base", base: 2 * time.Second, max: time.Second, random: func(int64) int64 { return 0 }},
		{name: "fractional maximum", base: time.Millisecond, max: time.Second + time.Nanosecond, random: func(int64) int64 { return 0 }},
		{name: "missing random source", base: time.Second, max: time.Second},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewRetryPolicy(test.base, test.max, test.random)
			if !errors.Is(err, domain.ErrInvalidRetryPolicy) {
				t.Fatalf("NewRetryPolicy() error = %v, want ErrInvalidRetryPolicy", err)
			}
		})
	}
}

func TestRetryPolicyRejectsOutOfRangeJitter(t *testing.T) {
	t.Parallel()

	attempt, err := domain.NewAttemptNumber(1)
	if err != nil {
		t.Fatalf("NewAttemptNumber() error = %v", err)
	}

	for _, draw := range []domain.RandomInt64N{
		func(int64) int64 { return -1 },
		func(upperExclusive int64) int64 { return upperExclusive },
	} {
		policy, err := domain.NewRetryPolicy(time.Second, time.Second, draw)
		if err != nil {
			t.Fatalf("NewRetryPolicy() error = %v", err)
		}
		if _, err := policy.Delay(attempt); !errors.Is(err, domain.ErrInvalidJitterValue) {
			t.Fatalf("Delay() error = %v, want ErrInvalidJitterValue", err)
		}
	}
}

func TestZeroRetryPolicyAndAttemptAreRejected(t *testing.T) {
	t.Parallel()

	policy, err := domain.NewRetryPolicy(time.Second, time.Second, func(int64) int64 { return 0 })
	if err != nil {
		t.Fatalf("NewRetryPolicy() error = %v", err)
	}
	if _, err := policy.Delay(domain.AttemptNumber{}); !errors.Is(err, domain.ErrInvalidAttemptNumber) {
		t.Fatalf("Delay(zero attempt) error = %v, want ErrInvalidAttemptNumber", err)
	}
	if _, err := (domain.RetryPolicy{}).Delay(domain.AttemptNumber{}); !errors.Is(err, domain.ErrInvalidRetryPolicy) {
		t.Fatalf("zero RetryPolicy.Delay() error = %v, want ErrInvalidRetryPolicy", err)
	}
}
