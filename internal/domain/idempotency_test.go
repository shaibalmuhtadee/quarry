package domain_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestIdempotentSubmissionHashCanonicalizesJSONObjects(t *testing.T) {
	first := idempotentSubmission(t, "email.send", ` { "count" : 1e0, "nested" : {"b":2,"a":1} } `, 3, 30*time.Second, "request-1")
	second := idempotentSubmission(t, "email.send", `{"nested":{"a":1,"b":2},"count":1e0}`, 3, 30*time.Second, "request-1")

	firstHash, ok := first.RequestHash()
	if !ok {
		t.Fatal("first submission has no request hash")
	}
	secondHash, ok := second.RequestHash()
	if !ok {
		t.Fatal("second submission has no request hash")
	}
	if !bytes.Equal(firstHash, secondHash) {
		t.Fatalf("canonical hashes differ: %x != %x", firstHash, secondHash)
	}
	firstHash[0] ^= 0xff
	stableHash, _ := first.RequestHash()
	if bytes.Equal(firstHash, stableHash) {
		t.Fatal("RequestHash exposed mutable submission state")
	}
}

func TestIdempotentSubmissionHashPreservesJSONNumberSpellingAndSubmissionFields(t *testing.T) {
	base := idempotentSubmission(t, "email.send", `{"count":1}`, 3, 30*time.Second, "request-1")
	tests := []struct {
		name     string
		jobType  string
		payload  string
		attempts int32
		timeout  time.Duration
	}{
		{name: "number spelling", jobType: "email.send", payload: `{"count":1.0}`, attempts: 3, timeout: 30 * time.Second},
		{name: "job type", jobType: "email.retry", payload: `{"count":1}`, attempts: 3, timeout: 30 * time.Second},
		{name: "maximum attempts", jobType: "email.send", payload: `{"count":1}`, attempts: 4, timeout: 30 * time.Second},
		{name: "timeout", jobType: "email.send", payload: `{"count":1}`, attempts: 3, timeout: 31 * time.Second},
	}
	baseHash, _ := base.RequestHash()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := idempotentSubmission(t, test.jobType, test.payload, test.attempts, test.timeout, "request-1")
			changedHash, _ := changed.RequestHash()
			if bytes.Equal(baseHash, changedHash) {
				t.Fatalf("changed submission hash = base hash %x", baseHash)
			}
		})
	}
}

func TestParseIdempotencyKey(t *testing.T) {
	key, err := domain.ParseIdempotencyKey("customer-order-42")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey returned an error: %v", err)
	}
	if key.String() != "customer-order-42" || key.IsZero() {
		t.Fatalf("parsed key = %#v", key)
	}

	for _, value := range []string{"", " \t", strings.Repeat("a", domain.MaxIdempotencyKeyLength+1), string([]byte{0xff})} {
		if _, err := domain.ParseIdempotencyKey(value); !errors.Is(err, domain.ErrInvalidIdempotencyKey) {
			t.Fatalf("ParseIdempotencyKey(%q) error = %v, want ErrInvalidIdempotencyKey", value, err)
		}
	}
}

func idempotentSubmission(
	t *testing.T,
	jobTypeValue string,
	payloadValue string,
	maxAttempts int32,
	timeout time.Duration,
	keyValue string,
) domain.JobSubmission {
	t.Helper()
	jobType, err := domain.ParseJobType(jobTypeValue)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := domain.ParsePayload(json.RawMessage(payloadValue))
	if err != nil {
		t.Fatal(err)
	}
	submission, err := domain.NewJobSubmission(jobType, payload, maxAttempts, timeout)
	if err != nil {
		t.Fatal(err)
	}
	key, err := domain.ParseIdempotencyKey(keyValue)
	if err != nil {
		t.Fatal(err)
	}
	submission, err = submission.WithIdempotencyKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return submission
}
