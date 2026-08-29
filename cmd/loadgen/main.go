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
	"runtime"
	"strings"
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
	apiURL            string
	outputPath        string
	summaryPath       string
	recoveryEventPath string
	requestTimeout    time.Duration
	run               loadgen.Config
	workload          loadgen.Workload
	seed              int64
	maxAttempts       int32
	jobTimeout        time.Duration
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
	factory, err := loadgen.NewWorkloadFactory(cfg.workload, cfg.seed, cfg.maxAttempts, cfg.jobTimeout)
	if err != nil {
		return err
	}
	runner, err := loadgen.NewRunner(client, cfg.run, factory)
	if err != nil {
		return err
	}

	result, runErr := runner.Run(ctx)
	if cfg.workload == loadgen.WorkloadRecovery {
		// Preserve the unfiltered samples before recovery validation so an invalid
		// process run still has enough evidence to diagnose and reproduce.
		if err := writeSamples(cfg.outputPath, result.Samples); err != nil {
			return errors.Join(runErr, err)
		}
		event, err := readRecoveryEvent(cfg.recoveryEventPath)
		if err != nil {
			return errors.Join(runErr, err)
		}
		result.Samples, err = loadgen.AttachRecoveryEvent(result.Samples, event)
		if err != nil {
			return errors.Join(runErr, err)
		}
	}
	writeErr := writeSamples(cfg.outputPath, result.Samples)
	if writeErr != nil {
		return errors.Join(runErr, writeErr)
	}
	persistedSamples, readErr := readSamples(cfg.outputPath)
	if readErr != nil {
		return errors.Join(runErr, readErr)
	}
	var summary any
	var summaryErr error
	if cfg.workload == loadgen.WorkloadRecovery {
		summary, summaryErr = loadgen.SummarizeRecoverySamples(persistedSamples)
	} else {
		summary, summaryErr = loadgen.SummarizeSamples(persistedSamples)
	}
	if summaryErr != nil {
		return errors.Join(runErr, summaryErr)
	}
	if summaryErr = writeSummary(cfg.summaryPath, summary); summaryErr != nil {
		return errors.Join(runErr, summaryErr)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return errors.Join(runErr, encoder.Encode(summary))
}

func parseConfig(args []string, stderr io.Writer, now time.Time) (config, error) {
	flags := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfg config
	var workload string
	var maxAttempts int
	flags.StringVar(&cfg.apiURL, "api-url", defaultAPIURL, "Quarry API base URL")
	flags.StringVar(&cfg.outputPath, "output", "", "compressed JSON Lines sample path")
	flags.StringVar(&cfg.summaryPath, "summary", "", "generated JSON summary path")
	flags.StringVar(&cfg.recoveryEventPath, "recovery-event", "", "Workload C worker-termination event path")
	flags.DurationVar(&cfg.requestTimeout, "request-timeout", defaultRequestTimeout, "per-request timeout")
	flags.StringVar(&cfg.run.RunID, "run-id", "loadgen-"+now.Format("20060102T150405.000000000Z"), "stable run identifier")
	flags.DurationVar(&cfg.run.WarmupDuration, "warmup", defaultWarmupDuration, "warmup duration")
	flags.DurationVar(&cfg.run.MeasurementDuration, "measurement", defaultMeasurementDuration, "measurement duration")
	flags.DurationVar(&cfg.run.DrainTimeout, "drain-timeout", defaultDrainTimeout, "post-measurement drain timeout")
	flags.DurationVar(&cfg.run.PollInterval, "poll-interval", defaultPollInterval, "job polling interval")
	flags.IntVar(&cfg.run.MaxOutstanding, "max-outstanding", defaultMaxOutstanding, "maximum outstanding jobs")
	flags.IntVar(&cfg.run.MaxHTTPConcurrency, "http-concurrency", defaultHTTPConcurrency, "maximum concurrent HTTP requests")
	flags.StringVar(&workload, "workload", "", "benchmark workload: a, b, or c")
	flags.Int64Var(&cfg.seed, "seed", 1, "deterministic payload seed")
	flags.IntVar(&maxAttempts, "max-attempts", int(defaultMaxAttempts), "maximum attempts per job")
	flags.DurationVar(&cfg.jobTimeout, "job-timeout", defaultJobTimeout, "job execution timeout")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("loadgen does not accept positional arguments")
	}
	if cfg.outputPath == "" {
		return config{}, errors.New("-output is required")
	}
	if cfg.summaryPath == "" {
		return config{}, errors.New("-summary is required")
	}
	for name, path := range map[string]string{"output": cfg.outputPath, "summary": cfg.summaryPath} {
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil || !parent.IsDir() {
			return config{}, fmt.Errorf("-%s parent directory must exist", name)
		}
	}
	outputAbsolute, err := filepath.Abs(cfg.outputPath)
	if err != nil {
		return config{}, fmt.Errorf("resolve -output: %w", err)
	}
	summaryAbsolute, err := filepath.Abs(cfg.summaryPath)
	if err != nil {
		return config{}, fmt.Errorf("resolve -summary: %w", err)
	}
	samePath := filepath.Clean(outputAbsolute) == filepath.Clean(summaryAbsolute)
	if runtime.GOOS == "windows" {
		samePath = strings.EqualFold(filepath.Clean(outputAbsolute), filepath.Clean(summaryAbsolute))
	}
	if samePath {
		return config{}, errors.New("-output and -summary must be different paths")
	}
	if cfg.requestTimeout <= 0 {
		return config{}, errors.New("-request-timeout must be positive")
	}
	cfg.workload, err = loadgen.ParseWorkload(workload)
	if err != nil {
		return config{}, err
	}
	if maxAttempts <= 0 || int64(maxAttempts) > int64(^uint32(0)>>1) {
		return config{}, errors.New("-max-attempts must be a positive int32")
	}
	if cfg.jobTimeout <= 0 || cfg.jobTimeout%time.Millisecond != 0 {
		return config{}, errors.New("-job-timeout must be a positive whole number of milliseconds")
	}
	cfg.maxAttempts = int32(maxAttempts)
	if cfg.workload == loadgen.WorkloadRecovery {
		if cfg.recoveryEventPath == "" {
			return config{}, errors.New("-recovery-event is required for Workload C")
		}
		if cfg.maxAttempts < 2 {
			return config{}, errors.New("Workload C requires at least two attempts")
		}
		parent, statErr := os.Stat(filepath.Dir(cfg.recoveryEventPath))
		if statErr != nil || !parent.IsDir() {
			return config{}, errors.New("-recovery-event parent directory must exist")
		}
		eventAbsolute, absoluteErr := filepath.Abs(cfg.recoveryEventPath)
		if absoluteErr != nil {
			return config{}, fmt.Errorf("resolve -recovery-event: %w", absoluteErr)
		}
		if equalPaths(eventAbsolute, outputAbsolute) || equalPaths(eventAbsolute, summaryAbsolute) {
			return config{}, errors.New("-recovery-event must differ from -output and -summary")
		}
	} else if cfg.recoveryEventPath != "" {
		return config{}, errors.New("-recovery-event is only valid for Workload C")
	}
	if err := loadgen.ValidateConfig(cfg.run); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func equalPaths(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func readSamples(path string) (samples []loadgen.Sample, readErr error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sample output: %w", err)
	}
	defer func() {
		readErr = errors.Join(readErr, input.Close())
	}()
	samples, err = loadgen.ReadGzipJSONLines(input)
	if err != nil {
		return nil, fmt.Errorf("read sample output: %w", err)
	}
	return samples, nil
}

func readRecoveryEvent(path string) (event loadgen.RecoveryEvent, readErr error) {
	input, err := os.Open(path)
	if err != nil {
		return loadgen.RecoveryEvent{}, fmt.Errorf("open recovery event: %w", err)
	}
	defer func() {
		readErr = errors.Join(readErr, input.Close())
	}()
	event, err = loadgen.ReadRecoveryEvent(input)
	if err != nil {
		return loadgen.RecoveryEvent{}, err
	}
	return event, nil
}

func writeSummary(path string, summary any) (writeErr error) {
	output, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create summary output: %w", err)
	}
	defer func() {
		writeErr = errors.Join(writeErr, output.Close())
	}()
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return fmt.Errorf("write summary output: %w", err)
	}
	return nil
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
