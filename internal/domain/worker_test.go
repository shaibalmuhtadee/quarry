package domain_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestWorkerIDRoundTrip(t *testing.T) {
	t.Parallel()

	id := domain.NewWorkerID()
	if id.IsZero() {
		t.Fatal("NewWorkerID returned a zero ID")
	}

	parsed, err := domain.ParseWorkerID(id.String())
	if err != nil {
		t.Fatalf("ParseWorkerID() error = %v", err)
	}
	if parsed != id {
		t.Fatalf("ParseWorkerID() = %v, want %v", parsed, id)
	}
	if parsed.UUID() != id.UUID() {
		t.Fatalf("UUID() = %v, want %v", parsed.UUID(), id.UUID())
	}

	text, err := parsed.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if string(text) != id.String() {
		t.Fatalf("MarshalText() = %q, want %q", text, id.String())
	}
}

func TestParseWorkerIDRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "not-a-uuid", uuid.Nil.String()} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			_, err := domain.ParseWorkerID(value)
			if !errors.Is(err, domain.ErrInvalidWorkerID) {
				t.Fatalf("ParseWorkerID(%q) error = %v, want ErrInvalidWorkerID", value, err)
			}
		})
	}
}

func TestZeroWorkerIDCannotMarshal(t *testing.T) {
	t.Parallel()

	var id domain.WorkerID
	if !id.IsZero() {
		t.Fatal("zero WorkerID did not report zero")
	}
	if _, err := id.MarshalText(); !errors.Is(err, domain.ErrInvalidWorkerID) {
		t.Fatalf("MarshalText() error = %v, want ErrInvalidWorkerID", err)
	}
}
