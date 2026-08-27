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
	duration := result.MeasurementEndedAt.Sub(result.MeasurementStartedAt)
	if duration <= 0 {
		return Summary{}, errors.New("measurement window must be positive")
	}
	var summary Summary
	var endToEnd, scheduling, attemptDuration, clientObserved []time.Duration
	for _, sample := range result.Samples {
		header := sample.Header()
		if header.Phase != PhaseMeasurement {
			continue
		}
		switch value := sample.(type) {
		case SubmissionFailureSample:
			summary.SubmissionFailureCount++
		case IncompleteJobSample:
			summary.IncompleteCount++
			if withinMeasurement(value.SubmissionCompletedAt, result) {
				summary.SubmittedCount++
			}
		case TerminalJobSample:
			if withinMeasurement(value.SubmissionCompletedAt, result) {
				summary.SubmittedCount++
			}
			if !withinMeasurement(value.FinishedAt, result) {
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

func withinMeasurement(value time.Time, result RunResult) bool {
	return !value.Before(result.MeasurementStartedAt) && !value.After(result.MeasurementEndedAt)
}
