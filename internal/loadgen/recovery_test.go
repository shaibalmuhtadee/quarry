package loadgen

import (
	"bytes"
	"testing"
	"time"
)

const (
	testKilledWorker      = "11111111-1111-4111-8111-111111111111"
	testReplacementWorker = "22222222-2222-4222-8222-222222222222"
)

func TestRecoverySamplesRoundTripAndRecomputeDurations(t *testing.T) {
	sample := testRecoverySample("recovery-run", testKilledWorker, testReplacementWorker)
	var raw bytes.Buffer
	if err := WriteGzipJSONLines(&raw, []Sample{sample}); err != nil {
		t.Fatalf("write recovery sample: %v", err)
	}
	decoded, err := ReadGzipJSONLines(&raw)
	if err != nil {
		t.Fatalf("read recovery sample: %v", err)
	}
	summary, err := SummarizeRecoverySamples(decoded)
	if err != nil {
		t.Fatalf("summarize recovery samples: %v", err)
	}
	if summary.RunID != "recovery-run" || summary.KilledWorkerID != testKilledWorker ||
		len(summary.ReplacementWorkerIDs) != 1 || summary.ReplacementWorkerIDs[0] != testReplacementWorker ||
		summary.KillToReplacementStart.P50 != 2*time.Second || summary.KillToSuccess.P50 != 8*time.Second {
		t.Fatalf("recovery summary = %#v", summary)
	}
}

func TestAttachRecoveryEventKeepsOnlyAffectedJobs(t *testing.T) {
	affected := testRecoverySample("recovery-run", testKilledWorker, testReplacementWorker)
	affected.Recovery = nil
	unaffected := affected
	unaffected.Attempts = append([]AttemptSample(nil), affected.Attempts...)
	unaffected.JobID = "unaffected"
	unaffected.Base.Sequence = 2
	unaffected.Attempts[0].WorkerID = testReplacementWorker
	event := RecoveryEvent{KilledWorkerID: testKilledWorker, WorkerTerminatedAt: affected.Attempts[0].StartedAt.Add(time.Second)}

	samples, err := AttachRecoveryEvent([]Sample{affected, unaffected}, event)
	if err != nil {
		t.Fatalf("attach recovery event: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("affected sample count = %d, want 1", len(samples))
	}
	terminal := samples[0].(TerminalJobSample)
	if terminal.JobID != affected.JobID || terminal.Recovery == nil || terminal.Recovery.KilledWorkerID != testKilledWorker {
		t.Fatalf("attached recovery sample = %#v", terminal)
	}
}

func TestRecoverySamplesRejectContradictoryEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*TerminalJobSample)
	}{
		{name: "wrong killed worker", mutate: func(value *TerminalJobSample) { value.Recovery.KilledWorkerID = testReplacementWorker }},
		{name: "first attempt succeeded", mutate: func(value *TerminalJobSample) { value.Attempts[0].Status = AttemptStatusSucceeded }},
		{name: "missing lease error", mutate: func(value *TerminalJobSample) {
			value.Attempts[0].ErrorCode = nil
			value.Attempts[0].ErrorMessage = nil
		}},
		{name: "same replacement", mutate: func(value *TerminalJobSample) { value.Attempts[1].WorkerID = testKilledWorker }},
		{name: "nonpositive replacement delay", mutate: func(value *TerminalJobSample) { value.Attempts[1].StartedAt = value.Recovery.WorkerTerminatedAt }},
		{name: "stale finish", mutate: func(value *TerminalJobSample) { value.FinishedAt = *value.Attempts[0].FinishedAt }},
	} {
		t.Run(test.name, func(t *testing.T) {
			sample := testRecoverySample("recovery-run", testKilledWorker, testReplacementWorker)
			test.mutate(&sample)
			if _, err := SummarizeRecoverySamples([]Sample{sample}); err == nil {
				t.Fatal("recovery summary accepted contradictory evidence")
			}
		})
	}
}

func TestReadRecoveryEventValidatesBoundary(t *testing.T) {
	input := `{"killed_worker_id":"11111111-1111-4111-8111-111111111111","worker_terminated_at":"2026-08-27T12:00:01Z"}`
	event, err := ReadRecoveryEvent(bytes.NewBufferString(input))
	if err != nil || event.KilledWorkerID != testKilledWorker {
		t.Fatalf("read recovery event = %#v, %v", event, err)
	}
	for _, invalid := range []string{
		`{"killed_worker_id":"bad","worker_terminated_at":"2026-08-27T12:00:01Z"}`,
		`{"killed_worker_id":"11111111-1111-4111-8111-111111111111","worker_terminated_at":"2026-08-27T12:00:01Z","unknown":true}`,
	} {
		if _, err := ReadRecoveryEvent(bytes.NewBufferString(invalid)); err == nil {
			t.Fatalf("accepted invalid recovery event %s", invalid)
		}
	}
}

func testRecoverySample(runID, killedWorkerID, replacementWorkerID string) TerminalJobSample {
	measurementStartedAt := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	createdAt := measurementStartedAt.Add(100 * time.Millisecond)
	firstStartedAt := createdAt.Add(100 * time.Millisecond)
	terminatedAt := firstStartedAt.Add(time.Second)
	firstFinishedAt := terminatedAt.Add(time.Second)
	replacementStartedAt := terminatedAt.Add(2 * time.Second)
	replacementFinishedAt := terminatedAt.Add(8 * time.Second)
	errorCode, errorMessage := "lease_expired", "worker lease expired"
	return TerminalJobSample{
		Base: SampleHeader{
			RunID: runID, Sequence: 1, Phase: PhaseMeasurement, JobType: "demo.sleep",
			SubmissionStartedAt: createdAt, MeasurementStartedAt: measurementStartedAt,
			MeasurementEndedAt: measurementStartedAt.Add(3 * time.Second),
		},
		JobID: "job-1", CreatedAt: createdAt, SubmissionCompletedAt: createdAt.Add(time.Millisecond),
		Status: JobStatusSucceeded, FinishedAt: replacementFinishedAt, TerminalObservedAt: replacementFinishedAt.Add(time.Millisecond),
		Attempts: []AttemptSample{
			{Number: 1, WorkerID: killedWorkerID, Status: AttemptStatusAbandoned, ErrorCode: &errorCode, ErrorMessage: &errorMessage, StartedAt: firstStartedAt, FinishedAt: &firstFinishedAt},
			{Number: 2, WorkerID: replacementWorkerID, Status: AttemptStatusSucceeded, StartedAt: replacementStartedAt, FinishedAt: &replacementFinishedAt},
		},
		Recovery: &RecoveryEvent{KilledWorkerID: killedWorkerID, WorkerTerminatedAt: terminatedAt},
	}
}
