package loadgen

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

type RunSummary struct {
	SchemaVersion int                `json:"schema_version"`
	RunID         string             `json:"run_id"`
	Config        BenchmarkRunConfig `json:"config"`
	Jobs          *Summary           `json:"jobs,omitempty"`
	Recovery      *RecoverySummary   `json:"recovery,omitempty"`
	Resources     ResourceSummary    `json:"resources"`
}

type MedianMetrics struct {
	SubmittedPerSecond            float64       `json:"submitted_per_second"`
	CompletedPerSecond            float64       `json:"completed_per_second"`
	EndToEndP50                   time.Duration `json:"end_to_end_p50"`
	EndToEndP95                   time.Duration `json:"end_to_end_p95"`
	EndToEndP99                   time.Duration `json:"end_to_end_p99"`
	SchedulingP50                 time.Duration `json:"scheduling_p50"`
	SchedulingP95                 time.Duration `json:"scheduling_p95"`
	SchedulingP99                 time.Duration `json:"scheduling_p99"`
	AttemptDurationP50            time.Duration `json:"attempt_duration_p50"`
	AttemptDurationP95            time.Duration `json:"attempt_duration_p95"`
	AttemptDurationP99            time.Duration `json:"attempt_duration_p99"`
	ClientObservedP50             time.Duration `json:"client_observed_p50"`
	ClientObservedP95             time.Duration `json:"client_observed_p95"`
	ClientObservedP99             time.Duration `json:"client_observed_p99"`
	QuarryCPUCoreAverage          float64       `json:"quarry_cpu_core_average"`
	QuarryResidentMemoryPeakBytes uint64        `json:"quarry_resident_memory_peak_bytes"`
	PostgreSQLCPUPercentAverage   float64       `json:"postgresql_cpu_percent_average"`
	PostgreSQLMemoryPeakBytes     uint64        `json:"postgresql_memory_peak_bytes"`
	DatabaseConnectionsPeak       int           `json:"database_connections_peak"`
}

type ConfigurationMedian struct {
	Config   BenchmarkRunConfig `json:"config"`
	RunIDs   []string           `json:"run_ids"`
	Median   *MedianMetrics     `json:"median,omitempty"`
	Recovery *RecoveryMedian    `json:"recovery,omitempty"`
}

type RecoveryMedian struct {
	KillToReplacementStartP50 time.Duration `json:"kill_to_replacement_start_p50"`
	KillToReplacementStartP95 time.Duration `json:"kill_to_replacement_start_p95"`
	KillToReplacementStartP99 time.Duration `json:"kill_to_replacement_start_p99"`
	KillToSuccessP50          time.Duration `json:"kill_to_success_p50"`
	KillToSuccessP95          time.Duration `json:"kill_to_success_p95"`
	KillToSuccessP99          time.Duration `json:"kill_to_success_p99"`
}

type CampaignSummary struct {
	SchemaVersion  int                   `json:"schema_version"`
	CampaignID     string                `json:"campaign_id"`
	Configurations []ConfigurationMedian `json:"configurations"`
}

func SummarizeRun(run RunManifest, jobSamples []Sample, resourceSamples []ResourceSample) (RunSummary, error) {
	if err := run.validate(); err != nil {
		return RunSummary{}, err
	}
	if run.Status != RunStatusValid {
		return RunSummary{}, errors.New("cannot summarize an invalid run")
	}
	expectedJobType := "demo.echo"
	if run.Config.Workload == WorkloadSimulatedIO || run.Config.Workload == WorkloadRecovery {
		expectedJobType = "demo.sleep"
	}
	for _, sample := range jobSamples {
		if sample.Header().JobType != expectedJobType {
			return RunSummary{}, errors.New("job samples contain a mixed workload")
		}
	}
	if len(jobSamples) == 0 {
		return RunSummary{}, errors.New("valid run contains no job samples")
	}
	header := jobSamples[0].Header()
	if header.RunID != run.RunID {
		return RunSummary{}, errors.New("job samples do not match the run manifest")
	}
	resources, err := SummarizeResources(
		resourceSamples,
		run.RunID,
		run.Config.WorkerProcesses,
		header.MeasurementStartedAt,
		header.MeasurementEndedAt,
	)
	if err != nil {
		return RunSummary{}, err
	}
	summary := RunSummary{
		SchemaVersion: RunSummarySchemaVersion,
		RunID:         run.RunID,
		Config:        run.Config,
		Resources:     resources,
	}
	if run.Config.Workload == WorkloadRecovery {
		recovery, err := SummarizeRecoverySamples(jobSamples)
		if err != nil {
			return RunSummary{}, err
		}
		summary.Recovery = &recovery
		return summary, nil
	}
	jobs, err := SummarizeSamples(jobSamples)
	if err != nil {
		return RunSummary{}, err
	}
	if jobs.SubmissionFailureCount != 0 || jobs.TerminalFailureCount != 0 || jobs.IncompleteCount != 0 ||
		jobs.SuccessfulCount == 0 || jobs.SuccessfulCount != jobs.CompletedCount {
		return RunSummary{}, errors.New("valid run contains failed, incomplete, or no successful measured jobs")
	}
	summary.Jobs = &jobs
	return summary, nil
}

func AggregateCampaign(manifest CampaignManifest, summaries []RunSummary) (CampaignSummary, error) {
	if err := manifest.Validate(); err != nil {
		return CampaignSummary{}, err
	}
	validRuns := make(map[string]RunManifest)
	for _, run := range manifest.Runs {
		if run.Status == RunStatusValid {
			validRuns[run.RunID] = run
		}
	}
	if len(summaries) != len(validRuns) {
		return CampaignSummary{}, errors.New("campaign summaries are missing or contain extra valid runs")
	}
	groups := make(map[string][]RunSummary)
	seen := make(map[string]struct{}, len(summaries))
	for _, summary := range summaries {
		run, exists := validRuns[summary.RunID]
		if !exists {
			return CampaignSummary{}, fmt.Errorf("summary %q has no valid campaign run", summary.RunID)
		}
		if _, exists := seen[summary.RunID]; exists {
			return CampaignSummary{}, fmt.Errorf("duplicate summary for run %q", summary.RunID)
		}
		seen[summary.RunID] = struct{}{}
		if err := validatePersistedRunSummary(summary, run); err != nil {
			return CampaignSummary{}, err
		}
		groups[summary.Config.key()] = append(groups[summary.Config.key()], summary)
	}

	result := CampaignSummary{SchemaVersion: RunSummarySchemaVersion, CampaignID: manifest.CampaignID}
	for _, group := range groups {
		if len(group) != 3 {
			return CampaignSummary{}, fmt.Errorf("configuration has %d valid runs, want 3", len(group))
		}
		sort.Slice(group, func(i, j int) bool { return group[i].RunID < group[j].RunID })
		configuration := ConfigurationMedian{
			Config: group[0].Config,
			RunIDs: []string{group[0].RunID, group[1].RunID, group[2].RunID},
		}
		if group[0].Config.Workload == WorkloadRecovery {
			recovery := medianRecovery(group)
			configuration.Recovery = &recovery
		} else {
			median := medianMetrics(group)
			configuration.Median = &median
		}
		result.Configurations = append(result.Configurations, configuration)
	}
	sort.Slice(result.Configurations, func(i, j int) bool {
		left, right := result.Configurations[i].Config, result.Configurations[j].Config
		if left.Workload != right.Workload {
			return left.Workload < right.Workload
		}
		return left.WorkerProcesses < right.WorkerProcesses
	})
	return result, nil
}

func validatePersistedRunSummary(summary RunSummary, run RunManifest) error {
	if summary.SchemaVersion != RunSummarySchemaVersion || summary.RunID != run.RunID || summary.Config != run.Config ||
		summary.Resources.SampleCount < 2 {
		return fmt.Errorf("persisted summary for run %q does not match its manifest", run.RunID)
	}
	if run.Config.Workload == WorkloadRecovery {
		if summary.Jobs != nil || summary.Recovery == nil || summary.Recovery.RunID != run.RunID || summary.Recovery.SampleCount == 0 {
			return fmt.Errorf("persisted recovery summary for run %q does not match its manifest", run.RunID)
		}
	} else if summary.Jobs == nil || summary.Recovery != nil || summary.Jobs.RunID != run.RunID {
		return fmt.Errorf("persisted job summary for run %q does not match its manifest", run.RunID)
	}
	return nil
}

func medianMetrics(group []RunSummary) MedianMetrics {
	return MedianMetrics{
		SubmittedPerSecond:            medianFloat(group, func(value RunSummary) float64 { return value.Jobs.SubmittedPerSecond }),
		CompletedPerSecond:            medianFloat(group, func(value RunSummary) float64 { return value.Jobs.CompletedPerSecond }),
		EndToEndP50:                   medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.EndToEnd.P50 }),
		EndToEndP95:                   medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.EndToEnd.P95 }),
		EndToEndP99:                   medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.EndToEnd.P99 }),
		SchedulingP50:                 medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.Scheduling.P50 }),
		SchedulingP95:                 medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.Scheduling.P95 }),
		SchedulingP99:                 medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.Scheduling.P99 }),
		AttemptDurationP50:            medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.AttemptDuration.P50 }),
		AttemptDurationP95:            medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.AttemptDuration.P95 }),
		AttemptDurationP99:            medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.AttemptDuration.P99 }),
		ClientObservedP50:             medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.ClientObserved.P50 }),
		ClientObservedP95:             medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.ClientObserved.P95 }),
		ClientObservedP99:             medianDuration(group, func(value RunSummary) time.Duration { return value.Jobs.ClientObserved.P99 }),
		QuarryCPUCoreAverage:          medianFloat(group, func(value RunSummary) float64 { return value.Resources.QuarryCPUCoreAverage }),
		QuarryResidentMemoryPeakBytes: medianUint64(group, func(value RunSummary) uint64 { return value.Resources.QuarryResidentMemoryPeakBytes }),
		PostgreSQLCPUPercentAverage:   medianFloat(group, func(value RunSummary) float64 { return value.Resources.PostgreSQLCPUPercentAverage }),
		PostgreSQLMemoryPeakBytes:     medianUint64(group, func(value RunSummary) uint64 { return value.Resources.PostgreSQLMemoryPeakBytes }),
		DatabaseConnectionsPeak:       medianInt(group, func(value RunSummary) int { return value.Resources.DatabaseConnectionsPeak }),
	}
}

func medianRecovery(group []RunSummary) RecoveryMedian {
	return RecoveryMedian{
		KillToReplacementStartP50: medianDuration(group, func(value RunSummary) time.Duration { return value.Recovery.KillToReplacementStart.P50 }),
		KillToReplacementStartP95: medianDuration(group, func(value RunSummary) time.Duration { return value.Recovery.KillToReplacementStart.P95 }),
		KillToReplacementStartP99: medianDuration(group, func(value RunSummary) time.Duration { return value.Recovery.KillToReplacementStart.P99 }),
		KillToSuccessP50:          medianDuration(group, func(value RunSummary) time.Duration { return value.Recovery.KillToSuccess.P50 }),
		KillToSuccessP95:          medianDuration(group, func(value RunSummary) time.Duration { return value.Recovery.KillToSuccess.P95 }),
		KillToSuccessP99:          medianDuration(group, func(value RunSummary) time.Duration { return value.Recovery.KillToSuccess.P99 }),
	}
}

func medianFloat(values []RunSummary, selectValue func(RunSummary) float64) float64 {
	ordered := make([]float64, len(values))
	for index, value := range values {
		ordered[index] = selectValue(value)
	}
	sort.Float64s(ordered)
	return ordered[len(ordered)/2]
}

func medianDuration(values []RunSummary, selectValue func(RunSummary) time.Duration) time.Duration {
	ordered := make([]time.Duration, len(values))
	for index, value := range values {
		ordered[index] = selectValue(value)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[len(ordered)/2]
}

func medianUint64(values []RunSummary, selectValue func(RunSummary) uint64) uint64 {
	ordered := make([]uint64, len(values))
	for index, value := range values {
		ordered[index] = selectValue(value)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[len(ordered)/2]
}

func medianInt(values []RunSummary, selectValue func(RunSummary) int) int {
	ordered := make([]int, len(values))
	for index, value := range values {
		ordered[index] = selectValue(value)
	}
	sort.Ints(ordered)
	return ordered[len(ordered)/2]
}
