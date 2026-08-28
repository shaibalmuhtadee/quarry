package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/loadgen"
)

func TestBenchmarkControllerRegeneratesCampaign(t *testing.T) {
	root, manifest := writeCampaignFixture(t)
	for _, runManifest := range manifest.Runs {
		if err := run([]string{"summarize-run", "-campaign-root", root, "-run-id", runManifest.RunID}, ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("summarize run %s: %v", runManifest.RunID, err)
		}
	}
	if err := run([]string{"verify-runs", "-campaign-root", root}, ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatalf("verify runs: %v", err)
	}
	if err := run([]string{"summarize-campaign", "-campaign-root", root}, ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatalf("summarize campaign: %v", err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"verify", "-campaign-root", root}, &stdout, ioDiscard{}); err != nil {
		t.Fatalf("verify campaign: %v", err)
	}
	if !strings.Contains(stdout.String(), "verified campaign fixture-campaign") {
		t.Fatalf("verify output = %q", stdout.String())
	}

	summaryPath := filepath.Join(root, filepath.FromSlash(manifest.Runs[0].Directory), runSummaryFileName)
	var summary loadgen.RunSummary
	readJSONFile(t, summaryPath, &summary)
	summary.Jobs.CompletedCount++
	writeJSONFile(t, summaryPath, summary)
	if err := run([]string{"verify", "-campaign-root", root}, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("verification accepted a modified run summary")
	}
}

func TestBenchmarkControllerRejectsMissingAndMalformedRunData(t *testing.T) {
	root, manifest := writeCampaignFixture(t)
	firstDirectory := filepath.Join(root, filepath.FromSlash(manifest.Runs[0].Directory))
	if err := os.Remove(filepath.Join(firstDirectory, resourceSamplesFileName)); err != nil {
		t.Fatalf("remove resource fixture: %v", err)
	}
	if err := run([]string{"summarize-run", "-campaign-root", root, "-run-id", manifest.Runs[0].RunID}, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("summarize-run accepted missing resource data")
	}

	root, manifest = writeCampaignFixture(t)
	manifestPath := filepath.Join(root, manifestFileName)
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	malformed := strings.TrimSuffix(strings.TrimSpace(string(encoded)), "}") + `,"unknown":true}`
	if err := os.WriteFile(manifestPath, []byte(malformed), 0o600); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	if err := run([]string{"verify-runs", "-campaign-root", root}, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("verify-runs accepted a malformed manifest")
	}
}

func TestBenchmarkControllerRequiresThreeValidRunsForCampaignMedian(t *testing.T) {
	root, manifest := writeCampaignFixture(t)
	manifest.Runs = manifest.Runs[:2]
	writeJSONFile(t, filepath.Join(root, manifestFileName), manifest)
	for _, runManifest := range manifest.Runs {
		if err := run([]string{"summarize-run", "-campaign-root", root, "-run-id", runManifest.RunID}, ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("summarize run: %v", err)
		}
	}
	if err := run([]string{"summarize-campaign", "-campaign-root", root}, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("campaign summary accepted fewer than three valid runs")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(buffer []byte) (int, error) { return len(buffer), nil }

func writeCampaignFixture(t *testing.T) (string, loadgen.CampaignManifest) {
	t.Helper()
	root := t.TempDir()
	measurementStartedAt := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	config := loadgen.BenchmarkRunConfig{
		Workload: loadgen.WorkloadQueueOverhead, WorkerProcesses: 1, WorkerConcurrency: 8,
		MaxOutstanding: 8, HTTPConcurrency: 8, WarmupDuration: time.Second,
		MeasurementDuration: 10 * time.Second, DrainTimeout: 5 * time.Second,
		PollInterval: 10 * time.Millisecond, Seed: 41, MaxAttempts: 1, JobTimeout: time.Second,
	}
	manifest := loadgen.CampaignManifest{
		SchemaVersion: loadgen.CampaignSchemaVersion, CampaignID: "fixture-campaign", CreatedAt: measurementStartedAt.Add(-time.Minute),
		Git:      loadgen.GitMetadata{Commit: strings.Repeat("a", 40), WorktreeState: "clean"},
		Machine:  loadgen.MachineMetadata{OS: "test", Architecture: "amd64", CPUModel: "fixture", LogicalCPUCount: 8, TotalMemoryBytes: 1024},
		Software: loadgen.SoftwareMetadata{GoVersion: "go1.27.0", DockerVersion: "29", PostgresImage: "postgres:18.6"},
		Quarry:   loadgen.QuarryConfig{LeaseDuration: 20 * time.Second, ReaperInterval: time.Second, ReaperBatchSize: 100, WorkerHeartbeatInterval: 5 * time.Second},
	}
	for repetition := 1; repetition <= 3; repetition++ {
		runID := "fixture-run-" + string(rune('0'+repetition))
		runManifest := loadgen.RunManifest{
			RunID: runID, Directory: "runs/" + runID, Repetition: repetition,
			Status: loadgen.RunStatusValid, Config: config,
		}
		manifest.Runs = append(manifest.Runs, runManifest)
		runDirectory := filepath.Join(root, filepath.FromSlash(runManifest.Directory))
		if err := os.MkdirAll(runDirectory, 0o700); err != nil {
			t.Fatalf("create run directory: %v", err)
		}
		writeJobSamples(t, filepath.Join(runDirectory, jobSamplesFileName), fixtureJobSamples(runID, measurementStartedAt, repetition))
		writeResourceSamples(t, filepath.Join(runDirectory, resourceSamplesFileName), fixtureResourceSamples(runID, measurementStartedAt, repetition))
	}
	writeJSONFile(t, filepath.Join(root, manifestFileName), manifest)
	return root, manifest
}

func fixtureJobSamples(runID string, start time.Time, repetition int) []loadgen.Sample {
	createdAt := start.Add(time.Second)
	finishedAt := createdAt.Add(time.Duration(repetition) * time.Millisecond)
	attemptStartedAt := createdAt.Add(time.Millisecond)
	attemptFinishedAt := finishedAt
	return []loadgen.Sample{loadgen.TerminalJobSample{
		Base: loadgen.SampleHeader{
			RunID: runID, Sequence: 1, Phase: loadgen.PhaseMeasurement, JobType: "demo.echo",
			SubmissionStartedAt: createdAt, MeasurementStartedAt: start, MeasurementEndedAt: start.Add(10 * time.Second),
		},
		JobID: runID + "-job", CreatedAt: createdAt, SubmissionCompletedAt: createdAt.Add(time.Microsecond),
		Status: loadgen.JobStatusSucceeded, FinishedAt: finishedAt, TerminalObservedAt: finishedAt.Add(time.Millisecond),
		Attempts: []loadgen.AttemptSample{{
			Number: 1, WorkerID: "worker", Status: loadgen.AttemptStatusSucceeded,
			StartedAt: attemptStartedAt, FinishedAt: &attemptFinishedAt,
		}},
	}}
}

func fixtureResourceSamples(runID string, start time.Time, repetition int) []loadgen.ResourceSample {
	var samples []loadgen.ResourceSample
	for index := 1; index <= 3; index++ {
		samples = append(samples, loadgen.ResourceSample{
			SchemaVersion: loadgen.ResourceSampleSchemaVersion, RunID: runID, ObservedAt: start.Add(time.Duration(index) * time.Second),
			Processes: []loadgen.ProcessResourceSample{
				{Name: "api", ProcessID: 1, CPUSeconds: float64(index * repetition), ResidentMemoryBytes: uint64(100 * repetition)},
				{Name: "dispatcher", ProcessID: 2, CPUSeconds: float64(index * repetition), ResidentMemoryBytes: uint64(200 * repetition)},
				{Name: "worker-01", ProcessID: 3, CPUSeconds: float64(index * repetition), ResidentMemoryBytes: uint64(300 * repetition)},
			},
			PostgreSQL:          loadgen.PostgreSQLResourceSample{CPUPercent: float64(10 * repetition), MemoryBytes: uint64(1000 * repetition)},
			DatabaseConnections: repetition,
		})
	}
	return samples
}

func writeJobSamples(t *testing.T, path string, samples []loadgen.Sample) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatalf("create job samples: %v", err)
	}
	if err := loadgen.WriteGzipJSONLines(output, samples); err != nil {
		output.Close()
		t.Fatalf("write job samples: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close job samples: %v", err)
	}
}

func writeResourceSamples(t *testing.T, path string, samples []loadgen.ResourceSample) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatalf("create resource samples: %v", err)
	}
	if err := loadgen.WriteResourceJSONLines(output, samples); err != nil {
		output.Close()
		t.Fatalf("write resource samples: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close resource samples: %v", err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatalf("create JSON file: %v", err)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		output.Close()
		t.Fatalf("write JSON file: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close JSON file: %v", err)
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read JSON file: %v", err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatalf("decode JSON file: %v", err)
	}
}
