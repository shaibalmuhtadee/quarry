package loadgen

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const maximumSampleLineBytes = 4 << 20

type sampleEnvelope struct {
	SchemaVersion         int              `json:"schema_version"`
	Kind                  SampleKind       `json:"kind"`
	Sequence              uint64           `json:"sequence"`
	Phase                 Phase            `json:"phase"`
	JobType               string           `json:"job_type"`
	SubmissionStartedAt   time.Time        `json:"submission_started_at"`
	SubmissionCompletedAt *time.Time       `json:"submission_completed_at,omitempty"`
	MayHaveCommitted      *bool            `json:"may_have_committed,omitempty"`
	JobID                 string           `json:"job_id,omitempty"`
	CreatedAt             *time.Time       `json:"created_at,omitempty"`
	Status                JobStatus        `json:"status,omitempty"`
	FinishedAt            *time.Time       `json:"finished_at,omitempty"`
	TerminalObservedAt    *time.Time       `json:"terminal_observed_at,omitempty"`
	DrainEndedAt          *time.Time       `json:"drain_ended_at,omitempty"`
	Attempts              *[]AttemptSample `json:"attempts,omitempty"`
	Errors                []RequestError   `json:"errors,omitempty"`
}

func WriteJSONLines(writer io.Writer, samples []Sample) error {
	encoder := json.NewEncoder(writer)
	for index, sample := range samples {
		envelope, err := envelopeFromSample(sample)
		if err != nil {
			return fmt.Errorf("encode sample %d: %w", index, err)
		}
		if err := encoder.Encode(envelope); err != nil {
			return fmt.Errorf("write sample %d: %w", index, err)
		}
	}
	return nil
}

func ReadJSONLines(reader io.Reader) ([]Sample, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maximumSampleLineBytes)
	var samples []Sample
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Bytes()
		if len(line) == 0 {
			return nil, fmt.Errorf("decode sample line %d: empty line", lineNumber)
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var envelope sampleEnvelope
		if err := decoder.Decode(&envelope); err != nil {
			return nil, fmt.Errorf("decode sample line %d: %w", lineNumber, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("decode sample line %d: multiple JSON values", lineNumber)
		}
		sample, err := envelope.sample()
		if err != nil {
			return nil, fmt.Errorf("decode sample line %d: %w", lineNumber, err)
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read samples: %w", err)
	}
	return samples, nil
}

func WriteGzipJSONLines(writer io.Writer, samples []Sample) error {
	compressed := gzip.NewWriter(writer)
	if err := WriteJSONLines(compressed, samples); err != nil {
		_ = compressed.Close()
		return err
	}
	if err := compressed.Close(); err != nil {
		return fmt.Errorf("finish compressed samples: %w", err)
	}
	return nil
}

func ReadGzipJSONLines(reader io.Reader) ([]Sample, error) {
	compressed, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("open compressed samples: %w", err)
	}
	samples, readErr := ReadJSONLines(compressed)
	closeErr := compressed.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close compressed samples: %w", closeErr)
	}
	return samples, nil
}

func envelopeFromSample(sample Sample) (sampleEnvelope, error) {
	if sample == nil {
		return sampleEnvelope{}, errors.New("sample is nil")
	}
	header := sample.Header()
	envelope := sampleEnvelope{
		SchemaVersion:       SampleSchemaVersion,
		Kind:                sample.Kind(),
		Sequence:            header.Sequence,
		Phase:               header.Phase,
		JobType:             header.JobType,
		SubmissionStartedAt: header.SubmissionStartedAt,
	}
	switch value := sample.(type) {
	case SubmissionFailureSample:
		envelope.MayHaveCommitted = boolPointer(value.MayHaveCommitted)
		envelope.Errors = value.Errors
	case TerminalJobSample:
		envelope.SubmissionCompletedAt = timePointer(value.SubmissionCompletedAt)
		envelope.JobID = value.JobID
		envelope.CreatedAt = timePointer(value.CreatedAt)
		envelope.Status = value.Status
		envelope.FinishedAt = timePointer(value.FinishedAt)
		envelope.TerminalObservedAt = timePointer(value.TerminalObservedAt)
		envelope.Attempts = attemptSamplesPointer(value.Attempts)
		envelope.Errors = value.Errors
	case IncompleteJobSample:
		envelope.SubmissionCompletedAt = timePointer(value.SubmissionCompletedAt)
		envelope.JobID = value.JobID
		envelope.CreatedAt = timePointer(value.CreatedAt)
		envelope.Status = value.LastStatus
		envelope.DrainEndedAt = timePointer(value.DrainEndedAt)
		envelope.Errors = value.Errors
	default:
		return sampleEnvelope{}, fmt.Errorf("unsupported sample type %T", sample)
	}
	if err := envelope.validate(); err != nil {
		return sampleEnvelope{}, err
	}
	return envelope, nil
}

func (envelope sampleEnvelope) sample() (Sample, error) {
	if err := envelope.validate(); err != nil {
		return nil, err
	}
	header := SampleHeader{
		Sequence:            envelope.Sequence,
		Phase:               envelope.Phase,
		JobType:             envelope.JobType,
		SubmissionStartedAt: envelope.SubmissionStartedAt,
	}
	switch envelope.Kind {
	case SampleKindSubmissionFailed:
		return SubmissionFailureSample{
			Base:             header,
			MayHaveCommitted: *envelope.MayHaveCommitted,
			Errors:           envelope.Errors,
		}, nil
	case SampleKindTerminal:
		return TerminalJobSample{
			Base:                  header,
			JobID:                 envelope.JobID,
			CreatedAt:             *envelope.CreatedAt,
			SubmissionCompletedAt: *envelope.SubmissionCompletedAt,
			Status:                envelope.Status,
			FinishedAt:            *envelope.FinishedAt,
			TerminalObservedAt:    *envelope.TerminalObservedAt,
			Attempts:              *envelope.Attempts,
			Errors:                envelope.Errors,
		}, nil
	case SampleKindIncomplete:
		return IncompleteJobSample{
			Base:                  header,
			JobID:                 envelope.JobID,
			CreatedAt:             *envelope.CreatedAt,
			SubmissionCompletedAt: *envelope.SubmissionCompletedAt,
			LastStatus:            envelope.Status,
			DrainEndedAt:          *envelope.DrainEndedAt,
			Errors:                envelope.Errors,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported sample kind %q", envelope.Kind)
	}
}

func (envelope sampleEnvelope) validate() error {
	if envelope.SchemaVersion != SampleSchemaVersion {
		return fmt.Errorf("sample schema version = %d, want %d", envelope.SchemaVersion, SampleSchemaVersion)
	}
	if envelope.Sequence == 0 {
		return errors.New("sample sequence must be positive")
	}
	if _, err := parsePhase(string(envelope.Phase)); err != nil {
		return err
	}
	if envelope.JobType == "" {
		return errors.New("sample job type is required")
	}
	if envelope.SubmissionStartedAt.IsZero() {
		return errors.New("sample submission start is required")
	}
	for _, requestError := range envelope.Errors {
		if requestError.Operation != OperationSubmit && requestError.Operation != OperationPoll && requestError.Operation != OperationAttempts {
			return fmt.Errorf("invalid request operation %q", requestError.Operation)
		}
		if requestError.ObservedAt.IsZero() || requestError.Message == "" {
			return errors.New("request error requires an observation time and message")
		}
	}
	switch envelope.Kind {
	case SampleKindSubmissionFailed:
		if envelope.MayHaveCommitted == nil || len(envelope.Errors) == 0 {
			return errors.New("submission-failure sample requires commit ambiguity and at least one error")
		}
		if envelope.SubmissionCompletedAt != nil || envelope.JobID != "" || envelope.CreatedAt != nil || envelope.Status != "" ||
			envelope.FinishedAt != nil || envelope.TerminalObservedAt != nil || envelope.DrainEndedAt != nil || envelope.Attempts != nil {
			return errors.New("submission-failure sample contains job completion fields")
		}
	case SampleKindTerminal:
		if envelope.MayHaveCommitted != nil || envelope.SubmissionCompletedAt == nil || envelope.JobID == "" || envelope.CreatedAt == nil ||
			envelope.FinishedAt == nil || envelope.TerminalObservedAt == nil || !envelope.Status.Terminal() || envelope.DrainEndedAt != nil || envelope.Attempts == nil {
			return errors.New("terminal sample is incomplete or contradictory")
		}
		if envelope.CreatedAt.IsZero() || envelope.FinishedAt.Before(*envelope.CreatedAt) ||
			envelope.TerminalObservedAt.Before(envelope.SubmissionStartedAt) {
			return errors.New("terminal sample contains invalid timestamps")
		}
		if envelope.Status == JobStatusSucceeded && len(*envelope.Attempts) == 0 {
			return errors.New("successful terminal sample requires an attempt")
		}
		if err := validateAttemptSamples(*envelope.Attempts); err != nil {
			return err
		}
	case SampleKindIncomplete:
		if envelope.MayHaveCommitted != nil || envelope.SubmissionCompletedAt == nil || envelope.JobID == "" || envelope.CreatedAt == nil ||
			envelope.Status == "" || envelope.DrainEndedAt == nil || envelope.FinishedAt != nil || envelope.TerminalObservedAt != nil || envelope.Attempts != nil {
			return errors.New("incomplete sample is incomplete or contradictory")
		}
		if _, err := ParseJobStatus(string(envelope.Status)); err != nil {
			return err
		}
		if envelope.CreatedAt.IsZero() || envelope.DrainEndedAt.Before(envelope.SubmissionStartedAt) {
			return errors.New("incomplete sample contains invalid timestamps")
		}
	default:
		return fmt.Errorf("unsupported sample kind %q", envelope.Kind)
	}
	return nil
}

func validateAttemptSamples(attempts []AttemptSample) error {
	var previousNumber int32
	for index, attempt := range attempts {
		if attempt.Number <= previousNumber || attempt.WorkerID == "" || attempt.StartedAt.IsZero() {
			return fmt.Errorf("attempt sample %d has an invalid number, worker, or start time", index)
		}
		if _, err := ParseAttemptStatus(string(attempt.Status)); err != nil {
			return fmt.Errorf("attempt sample %d: %w", index, err)
		}
		if (attempt.ErrorCode == nil) != (attempt.ErrorMessage == nil) {
			return fmt.Errorf("attempt sample %d has an incomplete error", index)
		}
		if attempt.Status == AttemptStatusRunning && attempt.FinishedAt != nil {
			return fmt.Errorf("attempt sample %d is running with a finish time", index)
		}
		if attempt.Status != AttemptStatusRunning && (attempt.FinishedAt == nil || attempt.FinishedAt.Before(attempt.StartedAt)) {
			return fmt.Errorf("attempt sample %d has an invalid finish time", index)
		}
		previousNumber = attempt.Number
	}
	return nil
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func attemptSamplesPointer(value []AttemptSample) *[]AttemptSample {
	return &value
}
