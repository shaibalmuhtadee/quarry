package loadgen

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/google/uuid"
)

type RecoveryEvent struct {
	KilledWorkerID     string    `json:"killed_worker_id"`
	WorkerTerminatedAt time.Time `json:"worker_terminated_at"`
}

type RecoverySummary struct {
	RunID                  string              `json:"run_id"`
	KilledWorkerID         string              `json:"killed_worker_id"`
	ReplacementWorkerIDs   []string            `json:"replacement_worker_ids"`
	WorkerTerminatedAt     time.Time           `json:"worker_terminated_at"`
	SampleCount            int                 `json:"sample_count"`
	KillToReplacementStart DurationPercentiles `json:"kill_to_replacement_start"`
	KillToSuccess          DurationPercentiles `json:"kill_to_success"`
}

func ReadRecoveryEvent(reader io.Reader) (RecoveryEvent, error) {
	var event RecoveryEvent
	if err := decodeStrictJSON(reader, &event); err != nil {
		return RecoveryEvent{}, fmt.Errorf("decode recovery event: %w", err)
	}
	if err := event.validate(); err != nil {
		return RecoveryEvent{}, err
	}
	return event, nil
}

func AttachRecoveryEvent(samples []Sample, event RecoveryEvent) ([]Sample, error) {
	if err := event.validate(); err != nil {
		return nil, err
	}
	affected := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		terminal, ok := sample.(TerminalJobSample)
		if !ok || terminal.Base.Phase != PhaseMeasurement || len(terminal.Attempts) == 0 ||
			terminal.Attempts[0].WorkerID != event.KilledWorkerID || terminal.Attempts[0].Status != AttemptStatusAbandoned {
			continue
		}
		recovery := event
		terminal.Recovery = &recovery
		if err := validateRecoveryTerminal(terminal); err != nil {
			return nil, fmt.Errorf("recovery job %q: %w", terminal.JobID, err)
		}
		affected = append(affected, terminal)
	}
	if len(affected) == 0 {
		return nil, errors.New("recovery run has no terminal jobs from the killed worker")
	}
	return affected, nil
}

func SummarizeRecoverySamples(samples []Sample) (RecoverySummary, error) {
	if len(samples) == 0 {
		return RecoverySummary{}, errors.New("cannot summarize an empty recovery sample set")
	}
	var summary RecoverySummary
	firstHeader := samples[0].Header()
	sequences := make(map[uint64]struct{}, len(samples))
	jobIDs := make(map[string]struct{}, len(samples))
	replacementWorkers := make(map[string]struct{})
	var replacementStarts, successes []time.Duration
	for index, sample := range samples {
		terminal, ok := sample.(TerminalJobSample)
		if !ok {
			return RecoverySummary{}, fmt.Errorf("recovery sample %d is not terminal", index)
		}
		if err := validateRecoveryTerminal(terminal); err != nil {
			return RecoverySummary{}, fmt.Errorf("recovery sample %d: %w", index, err)
		}
		header := terminal.Header()
		if header.RunID != firstHeader.RunID || !header.MeasurementStartedAt.Equal(firstHeader.MeasurementStartedAt) ||
			!header.MeasurementEndedAt.Equal(firstHeader.MeasurementEndedAt) {
			return RecoverySummary{}, errors.New("recovery samples contain inconsistent run metadata")
		}
		if _, exists := sequences[header.Sequence]; exists {
			return RecoverySummary{}, errors.New("recovery samples contain a duplicate sequence")
		}
		sequences[header.Sequence] = struct{}{}
		if _, exists := jobIDs[terminal.JobID]; exists {
			return RecoverySummary{}, errors.New("recovery samples contain a duplicate job")
		}
		jobIDs[terminal.JobID] = struct{}{}
		if index == 0 {
			summary.RunID = terminal.Base.RunID
			summary.KilledWorkerID = terminal.Recovery.KilledWorkerID
			summary.WorkerTerminatedAt = terminal.Recovery.WorkerTerminatedAt
		} else if terminal.Base.RunID != summary.RunID || terminal.Recovery.KilledWorkerID != summary.KilledWorkerID ||
			!terminal.Recovery.WorkerTerminatedAt.Equal(summary.WorkerTerminatedAt) {
			return RecoverySummary{}, errors.New("recovery samples contain inconsistent run or termination metadata")
		}
		replacement := terminal.Attempts[1]
		replacementWorkers[replacement.WorkerID] = struct{}{}
		replacementStarts = append(replacementStarts, replacement.StartedAt.Sub(summary.WorkerTerminatedAt))
		successes = append(successes, terminal.FinishedAt.Sub(summary.WorkerTerminatedAt))
	}
	for workerID := range replacementWorkers {
		summary.ReplacementWorkerIDs = append(summary.ReplacementWorkerIDs, workerID)
	}
	sort.Strings(summary.ReplacementWorkerIDs)
	summary.SampleCount = len(samples)
	summary.KillToReplacementStart = Percentiles(replacementStarts)
	summary.KillToSuccess = Percentiles(successes)
	return summary, nil
}

func (event RecoveryEvent) validate() error {
	if _, err := uuid.Parse(event.KilledWorkerID); err != nil {
		return errors.New("recovery event killed worker ID must be a UUID")
	}
	if event.WorkerTerminatedAt.IsZero() {
		return errors.New("recovery event termination time is required")
	}
	return nil
}

func validateRecoveryTerminal(sample TerminalJobSample) error {
	if sample.Recovery == nil {
		return errors.New("recovery metadata is required")
	}
	if err := sample.Recovery.validate(); err != nil {
		return err
	}
	if sample.Base.Phase != PhaseMeasurement || sample.Base.JobType != "demo.sleep" || sample.Status != JobStatusSucceeded || len(sample.Attempts) != 2 {
		return errors.New("recovery sample must be a measured successful demo.sleep job with two attempts")
	}
	if sample.JobID == "" || sample.CreatedAt.IsZero() || sample.SubmissionCompletedAt.IsZero() ||
		sample.FinishedAt.Before(sample.CreatedAt) || sample.TerminalObservedAt.Before(sample.FinishedAt) {
		return errors.New("recovery sample contains invalid job timestamps")
	}
	if err := validateAttemptSamples(sample.Attempts); err != nil {
		return err
	}
	first, replacement := sample.Attempts[0], sample.Attempts[1]
	if first.Number != 1 || first.WorkerID != sample.Recovery.KilledWorkerID || first.Status != AttemptStatusAbandoned ||
		first.ErrorCode == nil || *first.ErrorCode != "lease_expired" || first.FinishedAt == nil {
		return errors.New("attempt 1 must be lease-expired and abandoned on the killed worker")
	}
	if replacement.Number != 2 || replacement.WorkerID == sample.Recovery.KilledWorkerID ||
		replacement.Status != AttemptStatusSucceeded || replacement.ErrorCode != nil || replacement.FinishedAt == nil {
		return errors.New("attempt 2 must succeed on a distinct replacement worker")
	}
	if !sample.Recovery.WorkerTerminatedAt.After(first.StartedAt) ||
		!first.FinishedAt.After(sample.Recovery.WorkerTerminatedAt) ||
		!replacement.StartedAt.After(sample.Recovery.WorkerTerminatedAt) ||
		replacement.StartedAt.Before(*first.FinishedAt) ||
		!sample.FinishedAt.After(sample.Recovery.WorkerTerminatedAt) ||
		!sample.FinishedAt.Equal(*replacement.FinishedAt) {
		return errors.New("recovery timestamps do not prove positive kill-to-replacement and kill-to-success durations")
	}
	return nil
}
