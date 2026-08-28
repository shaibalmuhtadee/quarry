package loadgen

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

func TestJSONLinesRoundTrip(t *testing.T) {
	samples := testSamples()
	var output bytes.Buffer
	if err := WriteJSONLines(&output, samples); err != nil {
		t.Fatalf("write JSON Lines: %v", err)
	}
	decoded, err := ReadJSONLines(&output)
	if err != nil {
		t.Fatalf("read JSON Lines: %v", err)
	}
	if !reflect.DeepEqual(decoded, samples) {
		t.Fatalf("decoded samples = %#v, want %#v", decoded, samples)
	}
}

func TestCompressedJSONLinesRoundTrip(t *testing.T) {
	samples := testSamples()
	var output bytes.Buffer
	if err := WriteGzipJSONLines(&output, samples); err != nil {
		t.Fatalf("write compressed JSON Lines: %v", err)
	}
	if got := output.Bytes()[:2]; !bytes.Equal(got, []byte{0x1f, 0x8b}) {
		t.Fatalf("gzip header = %x", got)
	}
	decoded, err := ReadGzipJSONLines(&output)
	if err != nil {
		t.Fatalf("read compressed JSON Lines: %v", err)
	}
	if !reflect.DeepEqual(decoded, samples) {
		t.Fatalf("decoded samples = %#v, want %#v", decoded, samples)
	}
}

func TestReadJSONLinesRejectsUnknownSchemaAndContradictoryFields(t *testing.T) {
	for _, input := range []string{
		`{"schema_version":4,"kind":"submission_failed","run_id":"test","sequence":1,"phase":"measurement","job_type":"demo.echo","submission_started_at":"2026-08-27T12:00:00Z","measurement_started_at":"2026-08-27T12:00:00Z","measurement_ended_at":"2026-08-27T12:00:10Z","may_have_committed":false,"errors":[{"operation":"submit","observed_at":"2026-08-27T12:00:01Z","retryable":false,"ambiguous":false,"message":"bad"}]}` + "\n",
		`{"schema_version":2,"kind":"terminal","run_id":"test","sequence":1,"phase":"measurement","job_type":"demo.echo","submission_started_at":"2026-08-27T12:00:00Z","measurement_started_at":"2026-08-27T12:00:00Z","measurement_ended_at":"2026-08-27T12:00:10Z","submission_completed_at":"2026-08-27T12:00:00Z","job_id":"job","created_at":"2026-08-27T12:00:00Z","status":"running","finished_at":"2026-08-27T12:00:01Z","terminal_observed_at":"2026-08-27T12:00:01Z"}` + "\n",
		`{"schema_version":2,"kind":"submission_failed","run_id":"test","sequence":1,"phase":"measurement","job_type":"demo.echo","submission_started_at":"2026-08-27T12:00:00Z","measurement_started_at":"2026-08-27T12:00:00Z","measurement_ended_at":"2026-08-27T12:00:10Z","may_have_committed":false,"errors":[{"operation":"submit","observed_at":"2026-08-27T12:00:01Z","retryable":false,"ambiguous":false,"message":"bad"}],"unknown":true}` + "\n",
	} {
		if _, err := ReadJSONLines(bytes.NewBufferString(input)); err == nil {
			t.Fatalf("ReadJSONLines accepted %s", input)
		}
	}
}

func testSamples() []Sample {
	warmupStartedAt := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	measurementStartedAt := warmupStartedAt.Add(time.Second)
	measurementEndedAt := measurementStartedAt.Add(10 * time.Second)
	submittedAt := measurementStartedAt.Add(time.Millisecond)
	finishedAt := submittedAt.Add(2 * time.Millisecond)
	header := func(sequence uint64, phase Phase, startedAt time.Time) SampleHeader {
		return SampleHeader{
			RunID: "sample-test", Sequence: sequence, Phase: phase, JobType: "demo.echo", SubmissionStartedAt: startedAt,
			MeasurementStartedAt: measurementStartedAt, MeasurementEndedAt: measurementEndedAt,
		}
	}
	code := "invalid_input"
	message := "handler rejected input"
	return []Sample{
		SubmissionFailureSample{
			Base:             header(1, PhaseWarmup, warmupStartedAt),
			MayHaveCommitted: true,
			Errors:           []RequestError{{Operation: OperationSubmit, ObservedAt: submittedAt, Retryable: false, Ambiguous: true, Message: "malformed response"}},
		},
		TerminalJobSample{
			Base:  header(2, PhaseMeasurement, measurementStartedAt),
			JobID: "job-2", CreatedAt: measurementStartedAt, SubmissionCompletedAt: submittedAt,
			Status: JobStatusDeadLettered, FinishedAt: finishedAt, TerminalObservedAt: finishedAt.Add(time.Millisecond),
			Attempts: []AttemptSample{{
				Number: 1, WorkerID: "worker", Status: AttemptStatusPermanentFailed,
				ErrorCode: &code, ErrorMessage: &message, StartedAt: submittedAt, FinishedAt: &finishedAt,
			}},
		},
		TerminalJobSample{
			Base:      header(3, PhaseMeasurement, measurementStartedAt),
			JobID:     "job-3",
			CreatedAt: measurementStartedAt, SubmissionCompletedAt: submittedAt,
			Status: JobStatusCancelled, FinishedAt: finishedAt, TerminalObservedAt: finishedAt.Add(time.Millisecond),
			Attempts: []AttemptSample{},
		},
		IncompleteJobSample{
			Base:  header(4, PhaseMeasurement, measurementStartedAt),
			JobID: "job-4", CreatedAt: measurementStartedAt, SubmissionCompletedAt: submittedAt,
			LastStatus: JobStatusRunning, DrainEndedAt: finishedAt,
			Errors: []RequestError{{Operation: OperationPoll, ObservedAt: finishedAt, Message: "context deadline exceeded"}},
		},
	}
}
