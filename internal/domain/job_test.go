package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestJobIDRoundTrip(t *testing.T) {
	id := domain.NewJobID()
	if id.IsZero() {
		t.Fatal("NewJobID returned the zero UUID")
	}
	if got := id.UUID().Version(); got != 4 {
		t.Fatalf("NewJobID UUID version = %d, want 4", got)
	}

	parsed, err := domain.ParseJobID(id.String())
	if err != nil {
		t.Fatalf("ParseJobID returned an error: %v", err)
	}
	if parsed != id {
		t.Fatalf("ParseJobID result = %q, want %q", parsed, id)
	}
}

func TestParseJobIDRejectsInvalidValue(t *testing.T) {
	_, err := domain.ParseJobID("not-a-uuid")
	if !errors.Is(err, domain.ErrInvalidJobID) {
		t.Fatalf("ParseJobID error = %v, want ErrInvalidJobID", err)
	}
}

func TestZeroJobIDCannotMarshal(t *testing.T) {
	_, err := (domain.JobID{}).MarshalText()
	if !errors.Is(err, domain.ErrInvalidJobID) {
		t.Fatalf("MarshalText error = %v, want ErrInvalidJobID", err)
	}
}

func TestParseJobType(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "single segment", value: "example"},
		{name: "dot separator", value: "email.send"},
		{name: "underscore separator", value: "image_resize"},
		{name: "hyphen separator", value: "report-2026"},
		{name: "maximum length", value: "a" + strings.Repeat("b", domain.MaxJobTypeLength-1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jobType, err := domain.ParseJobType(test.value)
			if err != nil {
				t.Fatalf("ParseJobType returned an error: %v", err)
			}
			if got := jobType.String(); got != test.value {
				t.Fatalf("JobType.String() = %q, want %q", got, test.value)
			}
		})
	}
}

func TestParseJobTypeRejectsInvalidValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty"},
		{name: "starts with digit", value: "1example"},
		{name: "uppercase", value: "Example"},
		{name: "space", value: "email send"},
		{name: "leading separator", value: ".email"},
		{name: "trailing separator", value: "email."},
		{name: "consecutive separators", value: "email..send"},
		{name: "too long", value: "a" + strings.Repeat("b", domain.MaxJobTypeLength)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := domain.ParseJobType(test.value)
			if !errors.Is(err, domain.ErrInvalidJobType) {
				t.Fatalf("ParseJobType error = %v, want ErrInvalidJobType", err)
			}
		})
	}
}

func TestParsePayloadAcceptsOneJSONValue(t *testing.T) {
	for _, value := range []string{`{"message":"hello"}`, `[1,2,3]`, `"text"`, `42`, `true`, `null`} {
		t.Run(value, func(t *testing.T) {
			payload, err := domain.ParsePayload(json.RawMessage(value))
			if err != nil {
				t.Fatalf("ParsePayload returned an error: %v", err)
			}
			if got := string(payload.JSON()); got != value {
				t.Fatalf("Payload.JSON() = %q, want %q", got, value)
			}
		})
	}
}

func TestParsePayloadRejectsMissingOrMalformedValue(t *testing.T) {
	for _, value := range []json.RawMessage{nil, {}, []byte(`{"message":`), []byte(`{} []`)} {
		_, err := domain.ParsePayload(value)
		if !errors.Is(err, domain.ErrInvalidPayload) {
			t.Fatalf("ParsePayload(%q) error = %v, want ErrInvalidPayload", value, err)
		}
	}
}

func TestPayloadOwnsItsBytes(t *testing.T) {
	source := json.RawMessage(`{"message":"hello"}`)
	payload, err := domain.ParsePayload(source)
	if err != nil {
		t.Fatalf("ParsePayload returned an error: %v", err)
	}

	source[0] = '['
	firstCopy := payload.JSON()
	firstCopy[0] = '['

	if got, want := string(payload.JSON()), `{"message":"hello"}`; got != want {
		t.Fatalf("Payload.JSON() after caller mutation = %q, want %q", got, want)
	}
}

func TestParseJobStatus(t *testing.T) {
	statuses := []domain.JobStatus{
		domain.JobStatusQueued,
		domain.JobStatusRunning,
		domain.JobStatusRetryWait,
		domain.JobStatusSucceeded,
		domain.JobStatusDeadLettered,
		domain.JobStatusCancelled,
	}

	for _, want := range statuses {
		got, err := domain.ParseJobStatus(string(want))
		if err != nil {
			t.Fatalf("ParseJobStatus(%q) returned an error: %v", want, err)
		}
		if got != want {
			t.Fatalf("ParseJobStatus(%q) = %q, want %q", want, got, want)
		}
	}

	_, err := domain.ParseJobStatus("unknown")
	if !errors.Is(err, domain.ErrInvalidJobStatus) {
		t.Fatalf("ParseJobStatus error = %v, want ErrInvalidJobStatus", err)
	}
}

func TestNewJobSubmission(t *testing.T) {
	jobType, err := domain.ParseJobType("email.send")
	if err != nil {
		t.Fatalf("ParseJobType returned an error: %v", err)
	}
	payload, err := domain.ParsePayload(json.RawMessage(`{"recipient":"user@example.com"}`))
	if err != nil {
		t.Fatalf("ParsePayload returned an error: %v", err)
	}

	submission, err := domain.NewJobSubmission(jobType, payload, domain.DefaultMaxAttempts, 30*time.Second)
	if err != nil {
		t.Fatalf("NewJobSubmission returned an error: %v", err)
	}
	if submission.ID().IsZero() {
		t.Fatal("NewJobSubmission generated the zero job ID")
	}
	if submission.ID().UUID().Version() != uuid.Version(4) {
		t.Fatalf("job ID UUID version = %d, want 4", submission.ID().UUID().Version())
	}
	if got := submission.Type(); got != jobType {
		t.Fatalf("submission type = %q, want %q", got, jobType)
	}
	if got := string(submission.Payload().JSON()); got != `{"recipient":"user@example.com"}` {
		t.Fatalf("submission payload = %q", got)
	}
	if got := submission.MaxAttempts(); got != domain.DefaultMaxAttempts {
		t.Fatalf("submission maximum attempts = %d, want %d", got, domain.DefaultMaxAttempts)
	}
	if got := submission.Timeout(); got != 30*time.Second {
		t.Fatalf("submission timeout = %s, want 30s", got)
	}
}

func TestNewJobSubmissionRejectsInvalidValues(t *testing.T) {
	jobType, err := domain.ParseJobType("example")
	if err != nil {
		t.Fatalf("ParseJobType returned an error: %v", err)
	}
	payload, err := domain.ParsePayload(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ParsePayload returned an error: %v", err)
	}

	tests := []struct {
		name        string
		jobType     domain.JobType
		payload     domain.Payload
		maxAttempts int32
		timeout     time.Duration
		wantError   error
	}{
		{
			name:        "zero job type",
			payload:     payload,
			maxAttempts: 1,
			timeout:     time.Second,
			wantError:   domain.ErrInvalidJobType,
		},
		{
			name:        "zero payload",
			jobType:     jobType,
			maxAttempts: 1,
			timeout:     time.Second,
			wantError:   domain.ErrInvalidPayload,
		},
		{
			name:      "zero maximum attempts",
			jobType:   jobType,
			payload:   payload,
			timeout:   time.Second,
			wantError: domain.ErrInvalidMaxAttempts,
		},
		{
			name:        "negative maximum attempts",
			jobType:     jobType,
			payload:     payload,
			maxAttempts: -1,
			timeout:     time.Second,
			wantError:   domain.ErrInvalidMaxAttempts,
		},
		{
			name:        "zero timeout",
			jobType:     jobType,
			payload:     payload,
			maxAttempts: 1,
			wantError:   domain.ErrInvalidTimeout,
		},
		{
			name:        "negative timeout",
			jobType:     jobType,
			payload:     payload,
			maxAttempts: 1,
			timeout:     -time.Second,
			wantError:   domain.ErrInvalidTimeout,
		},
		{
			name:        "sub-millisecond timeout",
			jobType:     jobType,
			payload:     payload,
			maxAttempts: 1,
			timeout:     time.Nanosecond,
			wantError:   domain.ErrInvalidTimeout,
		},
		{
			name:        "fractional-millisecond timeout",
			jobType:     jobType,
			payload:     payload,
			maxAttempts: 1,
			timeout:     time.Millisecond + time.Nanosecond,
			wantError:   domain.ErrInvalidTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := domain.NewJobSubmission(
				test.jobType,
				test.payload,
				test.maxAttempts,
				test.timeout,
			)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("NewJobSubmission error = %v, want %v", err, test.wantError)
			}
		})
	}
}
