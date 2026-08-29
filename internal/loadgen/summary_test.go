package loadgen

import (
	"bytes"
	"testing"
	"time"
)

func TestNearestRank(t *testing.T) {
	if _, ok := NearestRank(nil, 50); ok {
		t.Fatal("empty percentile reported a value")
	}
	if got, ok := NearestRank([]time.Duration{7 * time.Millisecond}, 99); !ok || got != 7*time.Millisecond {
		t.Fatalf("one-value p99 = %s, %t", got, ok)
	}
	values := make([]time.Duration, 100)
	for index := range values {
		values[index] = time.Duration(100-index) * time.Millisecond
	}
	for percentile, want := range map[int]time.Duration{
		1:   time.Millisecond,
		50:  50 * time.Millisecond,
		95:  95 * time.Millisecond,
		99:  99 * time.Millisecond,
		100: 100 * time.Millisecond,
	} {
		if got, ok := NearestRank(values, percentile); !ok || got != want {
			t.Fatalf("p%d = %s, %t, want %s", percentile, got, ok, want)
		}
	}
	if _, ok := NearestRank(values, 0); ok {
		t.Fatal("p0 reported a value")
	}
}

func TestSummaryUsesSeparateSubmissionAndCompletionCounters(t *testing.T) {
	start := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Second)
	createdAt := start.Add(time.Second)
	submittedAt := createdAt.Add(time.Millisecond)
	finishedAt := createdAt.Add(5 * time.Millisecond)
	observedAt := finishedAt.Add(time.Millisecond)
	attemptFinishedAt := finishedAt
	header := func(sequence uint64, phase Phase, submissionStartedAt time.Time) SampleHeader {
		return SampleHeader{
			RunID: "summary-test", Sequence: sequence, Phase: phase, JobType: "demo.echo", SubmissionStartedAt: submissionStartedAt,
			MeasurementStartedAt: start, MeasurementEndedAt: end,
		}
	}
	result := RunResult{
		RunID:                "summary-test",
		MeasurementStartedAt: start,
		MeasurementEndedAt:   end,
		Samples: []Sample{
			TerminalJobSample{
				Base:  header(1, PhaseMeasurement, createdAt),
				JobID: "one", CreatedAt: createdAt, SubmissionCompletedAt: submittedAt,
				Status: JobStatusSucceeded, FinishedAt: finishedAt, TerminalObservedAt: observedAt,
				Attempts: []AttemptSample{{Number: 1, WorkerID: "worker", Status: AttemptStatusSucceeded, StartedAt: createdAt.Add(2 * time.Millisecond), FinishedAt: &attemptFinishedAt}},
			},
			IncompleteJobSample{
				Base:  header(2, PhaseMeasurement, createdAt),
				JobID: "two", CreatedAt: createdAt, SubmissionCompletedAt: submittedAt,
				LastStatus: JobStatusRunning, DrainEndedAt: end.Add(time.Second),
			},
			TerminalJobSample{
				Base:  header(3, PhaseMeasurement, createdAt),
				JobID: "three", CreatedAt: createdAt, SubmissionCompletedAt: submittedAt,
				Status: JobStatusCancelled, FinishedAt: end.Add(time.Second), TerminalObservedAt: end.Add(2 * time.Second),
			},
			SubmissionFailureSample{
				Base:   header(4, PhaseMeasurement, createdAt),
				Errors: []RequestError{{Operation: OperationSubmit, ObservedAt: createdAt, Message: "bad request"}},
			},
			TerminalJobSample{
				Base:  header(5, PhaseWarmup, start.Add(-time.Second)),
				JobID: "warmup", CreatedAt: start.Add(-time.Second), SubmissionCompletedAt: start.Add(-time.Second),
				Status: JobStatusSucceeded, FinishedAt: createdAt, TerminalObservedAt: observedAt,
			},
		},
	}

	summary, err := Summarize(result)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if summary.SubmittedCount != 3 || summary.CompletedCount != 1 {
		t.Fatalf("counts = submitted %d, completed %d", summary.SubmittedCount, summary.CompletedCount)
	}
	if summary.SubmittedPerSecond != 0.3 || summary.CompletedPerSecond != 0.1 {
		t.Fatalf("rates = submitted %v, completed %v", summary.SubmittedPerSecond, summary.CompletedPerSecond)
	}
	if summary.SuccessfulCount != 1 || summary.IncompleteCount != 1 || summary.SubmissionFailureCount != 1 {
		t.Fatalf("summary counts = %#v", summary)
	}
	if summary.RunID != result.RunID || summary.WarmupSampleCount != 1 || summary.MeasurementSampleCount != 4 {
		t.Fatalf("run metadata = %#v", summary)
	}
	if summary.EndToEnd.P50 != 5*time.Millisecond || summary.Scheduling.P50 != 2*time.Millisecond ||
		summary.AttemptDuration.P50 != 3*time.Millisecond || summary.ClientObserved.P50 != 6*time.Millisecond {
		t.Fatalf("latencies = %#v", summary)
	}
}

func TestSummaryRegeneratesFromCompressedRawSamples(t *testing.T) {
	samples := testSamples()
	var raw bytes.Buffer
	if err := WriteGzipJSONLines(&raw, samples); err != nil {
		t.Fatalf("write raw samples: %v", err)
	}
	decoded, err := ReadGzipJSONLines(&raw)
	if err != nil {
		t.Fatalf("read raw samples: %v", err)
	}
	summary, err := SummarizeSamples(decoded)
	if err != nil {
		t.Fatalf("regenerate summary: %v", err)
	}
	if summary.RunID != "sample-test" || summary.WarmupSampleCount != 1 || summary.MeasurementSampleCount != 3 {
		t.Fatalf("regenerated summary = %#v", summary)
	}
}
