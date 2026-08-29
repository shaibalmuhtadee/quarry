package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
	"github.com/shaibalmuhtadee/quarry/internal/worker"
	"github.com/shaibalmuhtadee/quarry/internal/worker/handlers"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultDispatcherAddress = "localhost:9090"
	defaultConcurrency       = uint32(4)
	defaultVersion           = "dev"
	defaultHeartbeatInterval = 5 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	rpcTimeout               = 5 * time.Second
	idleBackoffMin           = 50 * time.Millisecond
	idleBackoffMax           = time.Second
	reportBackoffMin         = 100 * time.Millisecond
	reportBackoffMax         = 500 * time.Millisecond
	defaultMetricsAddress    = ":0"
	defaultServiceName       = "quarry-worker"
	testSideEffectFileEnv    = "QUARRY_TEST_SIDE_EFFECT_FILE"
	testExitAfterSuccessEnv  = "QUARRY_TEST_EXIT_AFTER_HANDLER_SUCCESS"
)

var errTestExitAfterHandlerSuccess = errors.New("test fault injected after successful handler")

type testFaultConfig struct {
	sideEffectFile          string
	exitAfterHandlerSuccess bool
}

func (cfg testFaultConfig) handlerEnabled() bool {
	return cfg.sideEffectFile != ""
}

func (cfg testFaultConfig) exitEnabled() bool {
	return cfg.exitAfterHandlerSuccess
}

type config struct {
	dispatcherAddress string
	hostname          string
	version           string
	concurrency       uint32
	heartbeatInterval time.Duration
	shutdownTimeout   time.Duration
	telemetry         telemetry.Config
	testFault         testFaultConfig
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err == nil {
		err = run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("worker stopped", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("worker stopped")
}

func loadConfig() (config, error) {
	dispatcherAddress := os.Getenv("QUARRY_DISPATCHER_ADDR")
	if dispatcherAddress == "" {
		dispatcherAddress = defaultDispatcherAddress
	}
	version := os.Getenv("QUARRY_WORKER_VERSION")
	if version == "" {
		version = defaultVersion
	}
	hostname := os.Getenv("QUARRY_WORKER_HOSTNAME")
	if hostname == "" {
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			return config{}, fmt.Errorf("read hostname: %w", err)
		}
	}

	concurrency := defaultConcurrency
	if rawConcurrency := os.Getenv("QUARRY_WORKER_CONCURRENCY"); rawConcurrency != "" {
		parsed, err := strconv.ParseUint(rawConcurrency, 10, 32)
		if err != nil || parsed == 0 || parsed > math.MaxUint32 {
			return config{}, fmt.Errorf("QUARRY_WORKER_CONCURRENCY must be a positive uint32")
		}
		concurrency = uint32(parsed)
	}
	heartbeatInterval := defaultHeartbeatInterval
	if rawInterval := os.Getenv("QUARRY_HEARTBEAT_INTERVAL"); rawInterval != "" {
		parsed, err := time.ParseDuration(rawInterval)
		if err != nil || parsed <= 0 {
			return config{}, fmt.Errorf("QUARRY_HEARTBEAT_INTERVAL must be a positive duration")
		}
		heartbeatInterval = parsed
	}
	shutdownTimeout := defaultShutdownTimeout
	if rawTimeout := os.Getenv("QUARRY_WORKER_SHUTDOWN_TIMEOUT"); rawTimeout != "" {
		parsed, err := time.ParseDuration(rawTimeout)
		if err != nil || parsed <= 0 {
			return config{}, fmt.Errorf("QUARRY_WORKER_SHUTDOWN_TIMEOUT must be a positive duration")
		}
		shutdownTimeout = parsed
	}
	testFault, err := loadTestFaultConfig()
	if err != nil {
		return config{}, err
	}
	telemetryConfig, err := telemetry.LoadConfig(
		defaultServiceName,
		"QUARRY_WORKER_METRICS_ADDR",
		defaultMetricsAddress,
	)
	if err != nil {
		return config{}, err
	}

	return config{
		dispatcherAddress: dispatcherAddress,
		hostname:          hostname,
		version:           version,
		concurrency:       concurrency,
		heartbeatInterval: heartbeatInterval,
		shutdownTimeout:   shutdownTimeout,
		telemetry:         telemetryConfig,
		testFault:         testFault,
	}, nil
}

func loadTestFaultConfig() (testFaultConfig, error) {
	markerPath := os.Getenv(testSideEffectFileEnv)
	exitAfterSuccess := os.Getenv(testExitAfterSuccessEnv)
	if markerPath == "" && exitAfterSuccess == "" {
		return testFaultConfig{}, nil
	}
	if markerPath == "" {
		return testFaultConfig{}, fmt.Errorf(
			"%s is required when %s is set",
			testSideEffectFileEnv,
			testExitAfterSuccessEnv,
		)
	}
	if exitAfterSuccess != "" && exitAfterSuccess != "true" {
		return testFaultConfig{}, fmt.Errorf("%s must be true when configured", testExitAfterSuccessEnv)
	}
	if !filepath.IsAbs(markerPath) {
		return testFaultConfig{}, fmt.Errorf("%s must be an absolute path", testSideEffectFileEnv)
	}
	parent, err := os.Stat(filepath.Dir(markerPath))
	if err != nil {
		return testFaultConfig{}, fmt.Errorf("inspect %s parent directory: %w", testSideEffectFileEnv, err)
	}
	if !parent.IsDir() {
		return testFaultConfig{}, fmt.Errorf("%s parent must be a directory", testSideEffectFileEnv)
	}
	markerInfo, err := os.Stat(markerPath)
	if err == nil && markerInfo.IsDir() {
		return testFaultConfig{}, fmt.Errorf("%s must not be a directory", testSideEffectFileEnv)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return testFaultConfig{}, fmt.Errorf("inspect %s: %w", testSideEffectFileEnv, err)
	}
	return testFaultConfig{
		sideEffectFile:          filepath.Clean(markerPath),
		exitAfterHandlerSuccess: exitAfterSuccess == "true",
	}, nil
}

func run(ctx context.Context, cfg config, logger *slog.Logger) (runErr error) {
	telemetryRuntime, err := telemetry.New(ctx, cfg.telemetry)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		if err := telemetryRuntime.Shutdown(shutdownCtx); err != nil {
			logger.Warn("telemetry shutdown failed", slog.Any("error", err))
		}
	}()
	logger = slog.New(telemetry.NewTraceHandler(logger.Handler()))

	metricsServer, err := telemetry.ListenMetrics(cfg.telemetry.MetricsAddress, telemetryRuntime.MetricsHandler())
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		runErr = errors.Join(runErr, metricsServer.Shutdown(shutdownCtx))
	}()
	logger.Info("worker metrics starting", slog.String("address", metricsServer.Address()))

	connection, err := grpc.NewClient(
		cfg.dispatcherAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(telemetryRuntime.GRPCClientStatsHandler()),
	)
	if err != nil {
		return fmt.Errorf("create dispatcher client: %w", err)
	}
	defer connection.Close()

	dispatcherClient, err := worker.NewGRPCClient(
		dispatcherv1.NewDispatcherServiceClient(connection),
		rpcTimeout,
	)
	if err != nil {
		return err
	}

	handlerRegistry := handlers.Registry()
	var testAfterHandlerSuccess func(worker.Job) error
	if cfg.testFault.handlerEnabled() {
		handlerRegistry[handlers.TestSideEffectType] = handlers.NewTestSideEffectHandler(cfg.testFault.sideEffectFile)
	}
	if cfg.testFault.exitEnabled() {
		testAfterHandlerSuccess = func(worker.Job) error {
			return errTestExitAfterHandlerSuccess
		}
	}

	workerID := domain.NewWorkerID()
	startedAt := time.Now().UTC()
	runtime, err := worker.New(dispatcherClient, handlerRegistry, worker.Config{
		Registration: worker.Registration{
			WorkerID:    workerID,
			Hostname:    cfg.hostname,
			Version:     cfg.version,
			Concurrency: cfg.concurrency,
			StartedAt:   startedAt,
		},
		IdleBackoffMin:          idleBackoffMin,
		IdleBackoffMax:          idleBackoffMax,
		ReportBackoffMin:        reportBackoffMin,
		ReportBackoffMax:        reportBackoffMax,
		HeartbeatInterval:       cfg.heartbeatInterval,
		ShutdownTimeout:         cfg.shutdownTimeout,
		TestAfterHandlerSuccess: testAfterHandlerSuccess,
		Logger:                  logger,
		Metrics:                 telemetryRuntime.Metrics(),
		Tracer:                  telemetryRuntime.Tracer("quarry/worker"),
	})
	if err != nil {
		return err
	}

	logger.Info(
		"worker starting",
		slog.String("worker_id", workerID.String()),
		slog.String("dispatcher_address", cfg.dispatcherAddress),
		slog.Uint64("concurrency", uint64(cfg.concurrency)),
	)
	return runtime.Run(ctx)
}
