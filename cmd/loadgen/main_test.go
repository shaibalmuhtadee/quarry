package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shaibalmuhtadee/quarry/internal/loadgen"
)

func TestParseConfig(t *testing.T) {
	output := filepath.Join(t.TempDir(), "samples.jsonl.gz")
	summary := filepath.Join(filepath.Dir(output), "summary.json")
	cfg, err := parseConfig([]string{
		"-output", output,
		"-summary", summary,
		"-workload", "b",
		"-seed", "41",
		"-warmup", "0s",
		"-measurement", "2s",
		"-drain-timeout", "3s",
		"-poll-interval", "4ms",
		"-max-outstanding", "5",
		"-http-concurrency", "2",
	}, io.Discard, time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.outputPath != output || cfg.summaryPath != summary || cfg.workload != loadgen.WorkloadSimulatedIO || cfg.seed != 41 ||
		cfg.run.MeasurementDuration != 2*time.Second ||
		cfg.run.DrainTimeout != 3*time.Second || cfg.run.PollInterval != 4*time.Millisecond ||
		cfg.run.MaxOutstanding != 5 || cfg.run.MaxHTTPConcurrency != 2 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestParseRecoveryConfig(t *testing.T) {
	directory := t.TempDir()
	cfg, err := parseConfig([]string{
		"-output", filepath.Join(directory, "samples.jsonl.gz"),
		"-summary", filepath.Join(directory, "summary.json"),
		"-recovery-event", filepath.Join(directory, "recovery-event.json"),
		"-workload", "c",
		"-max-attempts", "3",
		"-warmup", "0s",
	}, io.Discard, time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parse recovery config: %v", err)
	}
	if cfg.workload != loadgen.WorkloadRecovery || cfg.maxAttempts != 3 || cfg.recoveryEventPath == "" {
		t.Fatalf("recovery config = %#v", cfg)
	}
}

func TestParseConfigRejectsBoundaryErrors(t *testing.T) {
	directory := t.TempDir()
	validOutput := filepath.Join(directory, "samples.jsonl.gz")
	validSummary := filepath.Join(directory, "summary.json")
	valid := []string{"-output", validOutput, "-summary", validSummary, "-workload", "a"}
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing output", args: []string{"-summary", validSummary, "-workload", "a"}},
		{name: "missing summary", args: []string{"-output", validOutput, "-workload", "a"}},
		{name: "missing output parent", args: []string{"-output", filepath.Join(directory, "missing", "samples.gz"), "-summary", validSummary, "-workload", "a"}},
		{name: "missing summary parent", args: []string{"-output", validOutput, "-summary", filepath.Join(directory, "missing", "summary.json"), "-workload", "a"}},
		{name: "same output paths", args: []string{"-output", validOutput, "-summary", validOutput, "-workload", "a"}},
		{name: "missing workload", args: []string{"-output", validOutput, "-summary", validSummary}},
		{name: "unknown workload", args: append(append([]string{}, valid...), "-workload", "d")},
		{name: "recovery event on throughput workload", args: append(append([]string{}, valid...), "-recovery-event", filepath.Join(directory, "event.json"))},
		{name: "recovery without event", args: []string{"-output", validOutput, "-summary", validSummary, "-workload", "c", "-max-attempts", "3"}},
		{name: "recovery with one attempt", args: []string{"-output", validOutput, "-summary", validSummary, "-workload", "c", "-recovery-event", filepath.Join(directory, "event.json"), "-max-attempts", "1"}},
		{name: "recovery event overwrites samples", args: []string{"-output", validOutput, "-summary", validSummary, "-workload", "c", "-recovery-event", validOutput, "-max-attempts", "3"}},
		{name: "fractional timeout", args: append(append([]string{}, valid...), "-job-timeout", "1500us")},
		{name: "invalid runner config", args: append(append([]string{}, valid...), "-max-outstanding", "0")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseConfig(test.args, &bytes.Buffer{}, time.Now().UTC()); err == nil {
				t.Fatal("parseConfig accepted invalid arguments")
			}
		})
	}
}

func TestRunWritesCompressedSamplesFromPublicHTTPFlow(t *testing.T) {
	jobID := uuid.NewString()
	workerID := uuid.NewString()
	createdAt := time.Now().UTC()
	finishedAt := createdAt.Add(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/jobs":
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(submitJobResponse{ID: jobID, Status: "queued", CreatedAt: createdAt})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/jobs/"+jobID:
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": jobID, "type": "demo.echo", "status": "succeeded", "attempt_count": 1,
				"max_attempts": 3, "timeout_ms": 1000, "result": map[string]any{"ok": true},
				"created_at": createdAt, "updated_at": finishedAt, "finished_at": finishedAt,
				"cancel_requested_at": nil,
			})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/jobs/"+jobID+"/attempts":
			_ = json.NewEncoder(writer).Encode(map[string]any{"attempts": []map[string]any{{
				"attempt_no": 1, "worker_id": workerID, "status": "succeeded",
				"error_code": nil, "error_message": nil, "started_at": createdAt, "finished_at": finishedAt,
			}}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	outputPath := filepath.Join(t.TempDir(), "samples.jsonl.gz")
	summaryPath := filepath.Join(filepath.Dir(outputPath), "summary.json")
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-api-url", server.URL,
		"-output", outputPath,
		"-summary", summaryPath,
		"-run-id", "command-test",
		"-workload", "a",
		"-seed", "41",
		"-warmup", "0s",
		"-measurement", "5ms",
		"-drain-timeout", "50ms",
		"-poll-interval", "1ms",
		"-max-outstanding", "1",
		"-http-concurrency", "1",
		"-job-timeout", "1s",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run command: %v, stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"submitted_count"`) || !strings.Contains(stdout.String(), `"completed_count"`) {
		t.Fatalf("summary output = %s", stdout.String())
	}
	output, err := os.Open(outputPath)
	if err != nil {
		t.Fatalf("open sample output: %v", err)
	}
	defer output.Close()
	samples, err := loadgen.ReadGzipJSONLines(output)
	if err != nil {
		t.Fatalf("read sample output: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("command wrote no samples")
	}
	if terminal, ok := samples[0].(loadgen.TerminalJobSample); !ok || terminal.JobID != jobID || len(terminal.Attempts) != 1 {
		t.Fatalf("first sample = %#v", samples[0])
	}
	regenerated, err := loadgen.SummarizeSamples(samples)
	if err != nil {
		t.Fatalf("regenerate summary: %v", err)
	}
	encodedSummary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary output: %v", err)
	}
	var persisted loadgen.Summary
	if err := json.Unmarshal(encodedSummary, &persisted); err != nil {
		t.Fatalf("decode summary output: %v", err)
	}
	if persisted.RunID != regenerated.RunID || persisted.SubmittedCount != regenerated.SubmittedCount ||
		persisted.CompletedCount != regenerated.CompletedCount || persisted.EndToEnd != regenerated.EndToEnd {
		t.Fatalf("persisted summary = %#v, regenerated = %#v", persisted, regenerated)
	}
}
