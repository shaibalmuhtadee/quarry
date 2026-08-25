package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const MaxIdempotencyKeyLength = 255

var (
	ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")
	ErrIdempotencyConflict   = errors.New("idempotency key conflicts with an existing submission")
)

type IdempotencyKey struct {
	value string
}

func ParseIdempotencyKey(value string) (IdempotencyKey, error) {
	if value == "" || strings.TrimSpace(value) == "" {
		return IdempotencyKey{}, fmt.Errorf("%w: value is required", ErrInvalidIdempotencyKey)
	}
	if len(value) > MaxIdempotencyKeyLength {
		return IdempotencyKey{}, fmt.Errorf("%w: value exceeds %d bytes", ErrInvalidIdempotencyKey, MaxIdempotencyKeyLength)
	}
	if !utf8.ValidString(value) {
		return IdempotencyKey{}, fmt.Errorf("%w: value must be valid UTF-8", ErrInvalidIdempotencyKey)
	}
	return IdempotencyKey{value: value}, nil
}

func (key IdempotencyKey) String() string {
	return key.value
}

func (key IdempotencyKey) IsZero() bool {
	return key.value == ""
}

func (submission JobSubmission) WithIdempotencyKey(key IdempotencyKey) (JobSubmission, error) {
	if key.IsZero() {
		return JobSubmission{}, ErrInvalidIdempotencyKey
	}
	canonicalPayload, err := canonicalJSON(submission.payload.JSON())
	if err != nil {
		return JobSubmission{}, fmt.Errorf("canonicalize submission payload: %w", err)
	}
	hashInput, err := json.Marshal(struct {
		Type        string          `json:"type"`
		Payload     json.RawMessage `json:"payload"`
		MaxAttempts int32           `json:"max_attempts"`
		TimeoutMS   int64           `json:"timeout_ms"`
	}{
		Type:        submission.jobType.String(),
		Payload:     canonicalPayload,
		MaxAttempts: submission.maxAttempts,
		TimeoutMS:   submission.timeout.Milliseconds(),
	})
	if err != nil {
		return JobSubmission{}, fmt.Errorf("encode normalized submission: %w", err)
	}

	submission.idempotencyKey = key
	submission.requestHash = sha256.Sum256(hashInput)
	return submission, nil
}

func (submission JobSubmission) IdempotencyKey() (IdempotencyKey, bool) {
	return submission.idempotencyKey, !submission.idempotencyKey.IsZero()
}

func (submission JobSubmission) RequestHash() ([]byte, bool) {
	if submission.idempotencyKey.IsZero() {
		return nil, false
	}
	return append([]byte(nil), submission.requestHash[:]...), true
}

func canonicalJSON(value json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("payload contains more than one JSON value")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
