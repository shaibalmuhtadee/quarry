package domain_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestAttemptNumber(t *testing.T) {
	t.Parallel()

	number, err := domain.NewAttemptNumber(3)
	if err != nil {
		t.Fatalf("NewAttemptNumber() error = %v", err)
	}
	if number.Int32() != 3 {
		t.Fatalf("Int32() = %d, want 3", number.Int32())
	}
	if number.IsZero() {
		t.Fatal("positive attempt number reported zero")
	}
}

func TestAttemptNumberRejectsNonpositiveValues(t *testing.T) {
	t.Parallel()

	for _, value := range []int32{-1, 0} {
		value := value
		t.Run(strconv.FormatInt(int64(value), 10), func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewAttemptNumber(value)
			if !errors.Is(err, domain.ErrInvalidAttemptNumber) {
				t.Fatalf("NewAttemptNumber(%d) error = %v, want ErrInvalidAttemptNumber", value, err)
			}
		})
	}
}

func TestAttemptStatus(t *testing.T) {
	t.Parallel()

	for _, value := range []domain.AttemptStatus{
		domain.AttemptStatusRunning,
		domain.AttemptStatusSucceeded,
		domain.AttemptStatusAbandoned,
	} {
		value := value
		t.Run(string(value), func(t *testing.T) {
			t.Parallel()

			status, err := domain.ParseAttemptStatus(string(value))
			if err != nil {
				t.Fatalf("ParseAttemptStatus(%q) error = %v", value, err)
			}
			if status != value {
				t.Fatalf("ParseAttemptStatus(%q) = %q, want %q", value, status, value)
			}
		})
	}
}

func TestAttemptStatusRejectsUnknownValue(t *testing.T) {
	t.Parallel()

	_, err := domain.ParseAttemptStatus("failed")
	if !errors.Is(err, domain.ErrInvalidAttemptStatus) {
		t.Fatalf("ParseAttemptStatus() error = %v, want ErrInvalidAttemptStatus", err)
	}
}
