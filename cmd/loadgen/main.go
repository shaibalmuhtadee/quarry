package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/loadgen"
)

const (
	defaultAPIURL              = "http://localhost:8080"
	defaultWarmupDuration      = 30 * time.Second
	defaultMeasurementDuration = 120 * time.Second
	defaultDrainTimeout        = 30 * time.Second
	defaultPollInterval        = 100 * time.Millisecond
	defaultJobTimeout          = 30 * time.Second
	defaultRequestTimeout      = 5 * time.Second
	defaultMaxAttempts         = int32(3)
	defaultMaxOutstanding      = 64
	defaultHTTPConcurrency     = 16
)

type config struct {
	apiURL         string
	outputPath     string
	requestTimeout time.Duration
	run            loadgen.Config
	submission     loadgen.Submission
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := parseConfig(args, stderr, time.Now().UTC())
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = cfg.run.MaxHTTPConcurrency
	transport.MaxIdleConnsPerHost = cfg.run.MaxHTTPConcurrency
	client, err := newHTTPAPIClient(cfg.apiURL, &http.Client{Transport: transport, Timeout: cfg.requestTimeout})
	if err != nil {
		return err
	}
	runner, err := loadgen.NewRunner(client, cfg.run, func(uint64) loadgen.Submission {
		return loadgen.Submission{
			JobType:     cfg.submission.JobType,
			Payload:     append(json.RawMessage(nil), cfg.submission.Payload...),
			MaxAttempts: cfg.submission.MaxAttempts,
			Timeout:     cfg.submission.Timeout,
		}
	})
	if err != nil {
		return err
	}

	result, runErr := runner.Run(ctx)
	writeErr := writeSamples(cfg.outputPath, result.Samples)
	if writeErr != nil {
		return errors.Join(runErr, writeErr)
	}
	summary, summaryErr := loadgen.Summarize(result)
	if summaryErr == nil {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		summaryErr = encoder.Encode(summary)
	}
	return errors.Join(runErr, summaryErr)
}

func parseConfig(args []string, stderr io.Writer, now time.Time) (config, error) {
	flags := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfg config
	var payload string
	var maxAttempts int
	flags.StringVar(&cfg.apiURL, "api-url", defaultAPIURL, "Quarry API base URL")
	flags.StringVar(&cfg.outputPath, "output", "", "compressed JSON Lines sample path")
	flags.DurationVar(&cfg.requestTimeout, "request-timeout", defaultRequestTimeout, "per-request timeout")
	flags.StringVar(&cfg.run.RunID, "run-id", "loadgen-"+now.Format("20060102T150405.000000000Z"), "stable run identifier")
	flags.DurationVar(&cfg.run.WarmupDuration, "warmup", defaultWarmupDuration, "warmup duration")
	flags.DurationVar(&cfg.run.MeasurementDuration, "measurement", defaultMeasurementDuration, "measurement duration")
	flags.DurationVar(&cfg.run.DrainTimeout, "drain-timeout", defaultDrainTimeout, "post-measurement drain timeout")
	flags.DurationVar(&cfg.run.PollInterval, "poll-interval", defaultPollInterval, "job polling interval")
	flags.IntVar(&cfg.run.MaxOutstanding, "max-outstanding", defaultMaxOutstanding, "maximum outstanding jobs")
	flags.IntVar(&cfg.run.MaxHTTPConcurrency, "http-concurrency", defaultHTTPConcurrency, "maximum concurrent HTTP requests")
	flags.StringVar(&cfg.submission.JobType, "job-type", "", "job type to submit")
	flags.StringVar(&payload, "payload", "", "job payload as one JSON value")
	flags.IntVar(&maxAttempts, "max-attempts", int(defaultMaxAttempts), "maximum attempts per job")
	flags.DurationVar(&cfg.submission.Timeout, "job-timeout", defaultJobTimeout, "job execution timeout")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("loadgen does not accept positional arguments")
	}
	if cfg.outputPath == "" {
		return config{}, errors.New("-output is required")
	}
	parent, err := os.Stat(filepath.Dir(cfg.outputPath))
	if err != nil || !parent.IsDir() {
		return config{}, errors.New("-output parent directory must exist")
	}
	if cfg.requestTimeout <= 0 {
		return config{}, errors.New("-request-timeout must be positive")
	}
	if cfg.submission.JobType == "" {
		return config{}, errors.New("-job-type is required")
	}
	if payload == "" || !json.Valid([]byte(payload)) {
		return config{}, errors.New("-payload must contain one JSON value")
	}
	if maxAttempts <= 0 || int64(maxAttempts) > int64(^uint32(0)>>1) {
		return config{}, errors.New("-max-attempts must be a positive int32")
	}
	if cfg.submission.Timeout <= 0 || cfg.submission.Timeout%time.Millisecond != 0 {
		return config{}, errors.New("-job-timeout must be a positive whole number of milliseconds")
	}
	cfg.submission.Payload = json.RawMessage(payload)
	cfg.submission.MaxAttempts = int32(maxAttempts)
	if err := loadgen.ValidateConfig(cfg.run); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func writeSamples(path string, samples []loadgen.Sample) (writeErr error) {
	output, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create sample output: %w", err)
	}
	defer func() {
		writeErr = errors.Join(writeErr, output.Close())
	}()
	if err := loadgen.WriteGzipJSONLines(output, samples); err != nil {
		return fmt.Errorf("write sample output: %w", err)
	}
	return nil
}
