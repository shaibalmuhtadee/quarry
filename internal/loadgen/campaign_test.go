package loadgen

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCampaignManifestValidation(t *testing.T) {
	manifest := testCampaignManifest(3)
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate campaign: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*CampaignManifest)
	}{
		{name: "missing metadata", mutate: func(value *CampaignManifest) { value.Machine.CPUModel = "" }},
		{name: "dirty publishable campaign", mutate: func(value *CampaignManifest) { value.Publishable = true; value.Git.WorktreeState = "dirty" }},
		{name: "duplicate run", mutate: func(value *CampaignManifest) { value.Runs[1].RunID = value.Runs[0].RunID }},
		{name: "duplicate configuration repetition", mutate: func(value *CampaignManifest) { value.Runs[1].Repetition = value.Runs[0].Repetition }},
		{name: "mixed outstanding limit", mutate: func(value *CampaignManifest) { value.Runs[1].Config.MaxOutstanding++ }},
		{name: "invalid worker count", mutate: func(value *CampaignManifest) { value.Runs[0].Config.WorkerProcesses = 3 }},
		{name: "invalid concurrency", mutate: func(value *CampaignManifest) { value.Runs[0].Config.WorkerConcurrency = 4 }},
		{name: "missing invalid reason", mutate: func(value *CampaignManifest) { value.Runs[0].Status = RunStatusInvalid }},
		{name: "unsafe directory", mutate: func(value *CampaignManifest) { value.Runs[0].Directory = "../outside" }},
		{name: "unsafe Windows directory", mutate: func(value *CampaignManifest) { value.Runs[0].Directory = `..\outside` }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := testCampaignManifest(3)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("campaign validation accepted invalid input")
			}
		})
	}
}

func TestReadCampaignManifestRejectsMalformedData(t *testing.T) {
	manifest := testCampaignManifest(3)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal campaign: %v", err)
	}
	withUnknown := strings.TrimSuffix(string(encoded), "}") + `,"unknown":true}`
	if _, err := ReadCampaignManifest(strings.NewReader(withUnknown)); err == nil {
		t.Fatal("campaign reader accepted an unknown field")
	}
	if _, err := ReadCampaignManifest(strings.NewReader(string(encoded) + `{}`)); err == nil {
		t.Fatal("campaign reader accepted multiple JSON values")
	}
}

func TestResourceSamplesRoundTripAndSummary(t *testing.T) {
	start := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	samples := testResourceSamples("run-1", start, 1)
	var raw bytes.Buffer
	if err := WriteResourceJSONLines(&raw, samples); err != nil {
		t.Fatalf("write resource samples: %v", err)
	}
	decoded, err := ReadResourceJSONLines(&raw)
	if err != nil {
		t.Fatalf("read resource samples: %v", err)
	}
	summary, err := SummarizeResources(decoded, "run-1", 1, start, start.Add(10*time.Second))
	if err != nil {
		t.Fatalf("summarize resources: %v", err)
	}
	if summary.SampleCount != 3 || summary.QuarryCPUCoreAverage != 3 ||
		summary.QuarryResidentMemoryPeakBytes != 660 || summary.PostgreSQLCPUPercentAverage != 20 ||
		summary.PostgreSQLMemoryPeakBytes != 1200 || summary.DatabaseConnectionsPeak != 7 {
		t.Fatalf("resource summary = %#v", summary)
	}
}

func TestResourceSamplesRejectMalformedAndMixedData(t *testing.T) {
	if _, err := ReadResourceJSONLines(strings.NewReader(`{"schema_version":1,"unknown":true}`)); err == nil {
		t.Fatal("resource reader accepted malformed data")
	}
	start := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	samples := testResourceSamples("run-1", start, 1)
	samples[1].RunID = "other"
	if _, err := SummarizeResources(samples, "run-1", 1, start, start.Add(10*time.Second)); err == nil {
		t.Fatal("resource summary accepted mixed run IDs")
	}
	samples = testResourceSamples("run-1", start, 1)
	samples[1].Processes = samples[1].Processes[:2]
	if _, err := SummarizeResources(samples, "run-1", 1, start, start.Add(10*time.Second)); err == nil {
		t.Fatal("resource summary accepted a missing process")
	}
}

func TestAggregateCampaignUsesThreeRunMedians(t *testing.T) {
	manifest := testCampaignManifest(3)
	summaries := []RunSummary{
		testRunSummary(manifest.Runs[0], 1),
		testRunSummary(manifest.Runs[1], 3),
		testRunSummary(manifest.Runs[2], 2),
	}
	result, err := AggregateCampaign(manifest, summaries)
	if err != nil {
		t.Fatalf("aggregate campaign: %v", err)
	}
	if len(result.Configurations) != 1 {
		t.Fatalf("configuration count = %d", len(result.Configurations))
	}
	median := result.Configurations[0].Median
	if median.CompletedPerSecond != 2 || median.EndToEndP95 != 2*time.Millisecond ||
		median.QuarryResidentMemoryPeakBytes != 200 || median.DatabaseConnectionsPeak != 2 {
		t.Fatalf("median = %#v", median)
	}
}

func TestAggregateCampaignRejectsMissingDuplicateAndMixedRuns(t *testing.T) {
	manifest := testCampaignManifest(3)
	first := testRunSummary(manifest.Runs[0], 1)
	second := testRunSummary(manifest.Runs[1], 2)
	third := testRunSummary(manifest.Runs[2], 3)
	for _, test := range []struct {
		name      string
		summaries []RunSummary
	}{
		{name: "missing", summaries: []RunSummary{first, second}},
		{name: "duplicate", summaries: []RunSummary{first, second, second}},
		{name: "mixed configuration", summaries: []RunSummary{first, second, func() RunSummary { value := third; value.Config.WorkerProcesses = 2; return value }()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := AggregateCampaign(manifest, test.summaries); err == nil {
				t.Fatal("campaign aggregation accepted invalid run data")
			}
		})
	}
}

func testCampaignManifest(runCount int) CampaignManifest {
	config := BenchmarkRunConfig{
		Workload: WorkloadQueueOverhead, WorkerProcesses: 1, WorkerConcurrency: 8,
		MaxOutstanding: 8, HTTPConcurrency: 8, WarmupDuration: time.Second,
		MeasurementDuration: 10 * time.Second, DrainTimeout: 5 * time.Second,
		PollInterval: 10 * time.Millisecond, Seed: 41, MaxAttempts: 1, JobTimeout: time.Second,
	}
	runs := make([]RunManifest, runCount)
	for index := range runs {
		runs[index] = RunManifest{
			RunID: "run-" + string(rune('1'+index)), Directory: "runs/run-" + string(rune('1'+index)),
			Repetition: index + 1, Status: RunStatusValid, Config: config,
		}
	}
	return CampaignManifest{
		SchemaVersion: CampaignSchemaVersion, CampaignID: "campaign", CreatedAt: time.Now().UTC(),
		Git:      GitMetadata{Commit: strings.Repeat("a", 40), WorktreeState: "clean"},
		Machine:  MachineMetadata{OS: "test", Architecture: "amd64", CPUModel: "test cpu", LogicalCPUCount: 8, TotalMemoryBytes: 1024},
		Software: SoftwareMetadata{GoVersion: "go1.27.0", DockerVersion: "29.0", PostgresImage: "postgres:18.6"},
		Quarry:   QuarryConfig{LeaseDuration: 20 * time.Second, ReaperInterval: time.Second, ReaperBatchSize: 100, WorkerHeartbeatInterval: 5 * time.Second},
		Runs:     runs,
	}
}

func testRunSummary(run RunManifest, value int) RunSummary {
	duration := time.Duration(value) * time.Millisecond
	return RunSummary{
		SchemaVersion: RunSummarySchemaVersion, RunID: run.RunID, Config: run.Config,
		Jobs: Summary{
			RunID: run.RunID, CompletedPerSecond: float64(value), SubmittedPerSecond: float64(value),
			EndToEnd:        DurationPercentiles{P50: duration, P95: duration, P99: duration},
			Scheduling:      DurationPercentiles{P50: duration, P95: duration, P99: duration},
			AttemptDuration: DurationPercentiles{P50: duration, P95: duration, P99: duration},
			ClientObserved:  DurationPercentiles{P50: duration, P95: duration, P99: duration},
		},
		Resources: ResourceSummary{
			SampleCount: 2, QuarryCPUCoreAverage: float64(value), QuarryResidentMemoryPeakBytes: uint64(value * 100),
			PostgreSQLCPUPercentAverage: float64(value), PostgreSQLMemoryPeakBytes: uint64(value * 1000), DatabaseConnectionsPeak: value,
		},
	}
}

func testResourceSamples(runID string, start time.Time, workers int) []ResourceSample {
	samples := make([]ResourceSample, 3)
	for index := range samples {
		processes := []ProcessResourceSample{
			{Name: "api", ProcessID: 1, CPUSeconds: float64(index), ResidentMemoryBytes: uint64(100 + index*10)},
			{Name: "dispatcher", ProcessID: 2, CPUSeconds: float64(index), ResidentMemoryBytes: uint64(200 + index*10)},
		}
		for worker := 0; worker < workers; worker++ {
			processes = append(processes, ProcessResourceSample{
				Name: "worker-" + string(rune('1'+worker)), ProcessID: worker + 3,
				CPUSeconds: float64(index), ResidentMemoryBytes: uint64(300 + index*10),
			})
		}
		samples[index] = ResourceSample{
			SchemaVersion: ResourceSampleSchemaVersion, RunID: runID, ObservedAt: start.Add(time.Duration(index+1) * time.Second),
			Processes: processes, PostgreSQL: PostgreSQLResourceSample{CPUPercent: float64((index + 1) * 10), MemoryBytes: uint64(1000 + index*100)},
			DatabaseConnections: 5 + index,
		}
	}
	return samples
}
