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
	cfg, err := parseConfig([]string{
		"-output", output,
		"-job-type", "demo.echo",
		"-payload", `{"message":"hello"}`,
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
	if cfg.outputPath != output || cfg.submission.JobType != "demo.echo" || cfg.run.MeasurementDuration != 2*time.Second ||
		cfg.run.DrainTimeout != 3*time.Second || cfg.run.PollInterval != 4*time.Millisecond ||
		cfg.run.MaxOutstanding != 5 || cfg.run.MaxHTTPConcurrency != 2 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestParseConfigRejectsBoundaryErrors(t *testing.T) {
	validOutput := filepath.Join(t.TempDir(), "samples.jsonl.gz")
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing output", args: []string{"-job-type", "demo.echo", "-payload", `{}`}},
		{name: "missing output parent", args: []string{"-output", filepath.Join(t.TempDir(), "missing", "samples.gz"), "-job-type", "demo.echo", "-payload", `{}`}},
		{name: "missing job type", args: []string{"-output", validOutput, "-payload", `{}`}},
		{name: "malformed payload", args: []string{"-output", validOutput, "-job-type", "demo.echo", "-payload", `{`}},
		{name: "fractional timeout", args: []string{"-output", validOutput, "-job-type", "demo.echo", "-payload", `{}`, "-job-timeout", "1500us"}},
		{name: "invalid runner config", args: []string{"-output", validOutput, "-job-type", "demo.echo", "-payload", `{}`, "-max-outstanding", "0"}},
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
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-api-url", server.URL,
		"-output", outputPath,
		"-run-id", "command-test",
		"-job-type", "demo.echo",
		"-payload", `{"message":"hello"}`,
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
}
