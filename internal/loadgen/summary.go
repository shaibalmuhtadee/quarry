package loadgen

import (
	"errors"
	"math"
	"sort"
	"time"
)

type DurationPercentiles struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"p50"`
	P95   time.Duration `json:"p95"`
	P99   time.Duration `json:"p99"`
}

type Summary struct {
	RunID                  string              `json:"run_id"`
	MeasurementStartedAt   time.Time           `json:"measurement_started_at"`
	MeasurementEndedAt     time.Time           `json:"measurement_ended_at"`
	WarmupSampleCount      int                 `json:"warmup_sample_count"`
	MeasurementSampleCount int                 `json:"measurement_sample_count"`
	SubmittedCount         int                 `json:"submitted_count"`
	CompletedCount         int                 `json:"completed_count"`
	SuccessfulCount        int                 `json:"successful_count"`
	TerminalFailureCount   int                 `json:"terminal_failure_count"`
	SubmissionFailureCount int                 `json:"submission_failure_count"`
	IncompleteCount        int                 `json:"incomplete_count"`
	SubmittedPerSecond     float64             `json:"submitted_per_second"`
	CompletedPerSecond     float64             `json:"completed_per_second"`
	EndToEnd               DurationPercentiles `json:"end_to_end"`
	Scheduling             DurationPercentiles `json:"scheduling"`
	AttemptDuration        DurationPercentiles `json:"attempt_duration"`
	ClientObserved         DurationPercentiles `json:"client_observed"`
}

func Summarize(result RunResult) (Summary, error) {
	summary, err := SummarizeSamples(result.Samples)
	if err != nil {
		return Summary{}, err
	}
	if result.RunID != summary.RunID || !result.MeasurementStartedAt.Equal(summary.MeasurementStartedAt) ||
		!result.MeasurementEndedAt.Equal(summary.MeasurementEndedAt) {
		return Summary{}, errors.New("run result metadata does not match its samples")
	}
	return summary, nil
}

func SummarizeSamples(samples []Sample) (Summary, error) {
	if len(samples) == 0 {
		return Summary{}, errors.New("cannot summarize an empty sample set")
	}
	first := samples[0].Header()
	if first.RunID == "" || first.MeasurementStartedAt.IsZero() || !first.MeasurementEndedAt.After(first.MeasurementStartedAt) {
		return Summary{}, errors.New("samples require a run ID and positive measurement window")
	}
	duration := first.MeasurementEndedAt.Sub(first.MeasurementStartedAt)
	var summary Summary
	summary.RunID = first.RunID
	summary.MeasurementStartedAt = first.MeasurementStartedAt
	summary.MeasurementEndedAt = first.MeasurementEndedAt
	var endToEnd, scheduling, attemptDuration, clientObserved []time.Duration
	sequences := make(map[uint64]struct{}, len(samples))
	for _, sample := range samples {
		header := sample.Header()
		if header.RunID != first.RunID || !header.MeasurementStartedAt.Equal(first.MeasurementStartedAt) ||
			!header.MeasurementEndedAt.Equal(first.MeasurementEndedAt) {
			return Summary{}, errors.New("samples contain inconsistent run metadata")
		}
		if _, exists := sequences[header.Sequence]; exists {
			return Summary{}, errors.New("samples contain a duplicate sequence")
		}
		sequences[header.Sequence] = struct{}{}
		if header.Phase == PhaseWarmup {
			summary.WarmupSampleCount++
			continue
		}
		if header.Phase != PhaseMeasurement {
			return Summary{}, errors.New("sample contains an invalid phase")
		}
		summary.MeasurementSampleCount++
		switch value := sample.(type) {
		case SubmissionFailureSample:
			summary.SubmissionFailureCount++
		case IncompleteJobSample:
			summary.IncompleteCount++
			if withinMeasurement(value.SubmissionCompletedAt, first.MeasurementStartedAt, first.MeasurementEndedAt) {
				summary.SubmittedCount++
			}
		case TerminalJobSample:
			if withinMeasurement(value.SubmissionCompletedAt, first.MeasurementStartedAt, first.MeasurementEndedAt) {
				summary.SubmittedCount++
			}
			if !withinMeasurement(value.FinishedAt, first.MeasurementStartedAt, first.MeasurementEndedAt) {
				continue
			}
			summary.CompletedCount++
			if value.Status != JobStatusSucceeded {
				summary.TerminalFailureCount++
				continue
			}
			summary.SuccessfulCount++
			endToEnd = append(endToEnd, value.FinishedAt.Sub(value.CreatedAt))
			clientObserved = append(clientObserved, value.TerminalObservedAt.Sub(header.SubmissionStartedAt))
			for _, attempt := range value.Attempts {
				if attempt.Number == 1 {
					scheduling = append(scheduling, attempt.StartedAt.Sub(value.CreatedAt))
				}
				if attempt.FinishedAt != nil {
					attemptDuration = append(attemptDuration, attempt.FinishedAt.Sub(attempt.StartedAt))
				}
			}
		default:
			return Summary{}, errors.New("samples contain an unsupported sample type")
		}
	}
	summary.SubmittedPerSecond = Rate(summary.SubmittedCount, duration)
	summary.CompletedPerSecond = Rate(summary.CompletedCount, duration)
	summary.EndToEnd = Percentiles(endToEnd)
	summary.Scheduling = Percentiles(scheduling)
	summary.AttemptDuration = Percentiles(attemptDuration)
	summary.ClientObserved = Percentiles(clientObserved)
	return summary, nil
}

func Rate(count int, duration time.Duration) float64 {
	if count <= 0 || duration <= 0 {
		return 0
	}
	return float64(count) / duration.Seconds()
}

func Percentiles(values []time.Duration) DurationPercentiles {
	p50, _ := NearestRank(values, 50)
	p95, _ := NearestRank(values, 95)
	p99, _ := NearestRank(values, 99)
	return DurationPercentiles{Count: len(values), P50: p50, P95: p95, P99: p99}
}

func NearestRank(values []time.Duration, percentile int) (time.Duration, bool) {
	if len(values) == 0 || percentile < 1 || percentile > 100 {
		return 0, false
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	rank := int(math.Ceil(float64(percentile) / 100 * float64(len(ordered))))
	return ordered[rank-1], true
}

func withinMeasurement(value, startedAt, endedAt time.Time) bool {
	return !value.Before(startedAt) && value.Before(endedAt)
}
