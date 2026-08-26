package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/shaibalmuhtadee/quarry/internal/dispatcher"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
	"google.golang.org/grpc"
)

const (
	defaultDatabaseURL       = "postgres://quarry:quarry@localhost:5432/quarry?sslmode=disable"
	defaultDispatcherAddress = "localhost:9090"
	defaultLeaseDuration     = 20 * time.Second
	defaultReaperInterval    = time.Second
	defaultReaperBatchSize   = int32(100)
	defaultRetryBaseDelay    = domain.DefaultRetryBaseDelay
	defaultRetryMaxDelay     = domain.DefaultRetryMaxDelay
	shutdownTimeout          = 10 * time.Second
	defaultMetricsAddress    = ":9464"
	defaultServiceName       = "quarry-dispatcher"
)

type config struct {
	databaseURL       string
	dispatcherAddress string
	leaseDuration     time.Duration
	reaperInterval    time.Duration
	reaperBatchSize   int32
	workerLiveness    time.Duration
	retryBaseDelay    time.Duration
	retryMaxDelay     time.Duration
	telemetry         telemetry.Config
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
		logger.Error("dispatcher stopped", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("dispatcher stopped")
}

func loadConfig() (config, error) {
	databaseURL := os.Getenv("QUARRY_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = defaultDatabaseURL
	}
	dispatcherAddress := os.Getenv("QUARRY_DISPATCHER_ADDR")
	if dispatcherAddress == "" {
		dispatcherAddress = defaultDispatcherAddress
	}
	leaseDuration := defaultLeaseDuration
	if value := os.Getenv("QUARRY_LEASE_DURATION"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return config{}, fmt.Errorf("parse QUARRY_LEASE_DURATION: %w", err)
		}
		leaseDuration = parsed
	}
	if leaseDuration <= 0 || leaseDuration%time.Millisecond != 0 {
		return config{}, errors.New("QUARRY_LEASE_DURATION must be a positive whole number of milliseconds")
	}
	reaperInterval := defaultReaperInterval
	if value := os.Getenv("QUARRY_REAPER_INTERVAL"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return config{}, fmt.Errorf("parse QUARRY_REAPER_INTERVAL: %w", err)
		}
		reaperInterval = parsed
	}
	if reaperInterval <= 0 {
		return config{}, errors.New("QUARRY_REAPER_INTERVAL must be positive")
	}
	reaperBatchSize := defaultReaperBatchSize
	if value := os.Getenv("QUARRY_REAPER_BATCH_SIZE"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return config{}, fmt.Errorf("parse QUARRY_REAPER_BATCH_SIZE: %w", err)
		}
		reaperBatchSize = int32(parsed)
	}
	if reaperBatchSize <= 0 {
		return config{}, errors.New("QUARRY_REAPER_BATCH_SIZE must be positive")
	}
	workerLiveness := leaseDuration
	if value := os.Getenv("QUARRY_WORKER_LIVENESS_TIMEOUT"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return config{}, fmt.Errorf("parse QUARRY_WORKER_LIVENESS_TIMEOUT: %w", err)
		}
		workerLiveness = parsed
	}
	if workerLiveness <= 0 || workerLiveness%time.Millisecond != 0 {
		return config{}, errors.New("QUARRY_WORKER_LIVENESS_TIMEOUT must be a positive whole number of milliseconds")
	}
	retryBaseDelay := defaultRetryBaseDelay
	if value := os.Getenv("QUARRY_RETRY_BASE_DELAY"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return config{}, fmt.Errorf("parse QUARRY_RETRY_BASE_DELAY: %w", err)
		}
		retryBaseDelay = parsed
	}
	retryMaxDelay := defaultRetryMaxDelay
	if value := os.Getenv("QUARRY_RETRY_MAX_DELAY"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return config{}, fmt.Errorf("parse QUARRY_RETRY_MAX_DELAY: %w", err)
		}
		retryMaxDelay = parsed
	}
	if _, err := domain.NewRetryPolicy(retryBaseDelay, retryMaxDelay, rand.Int64N); err != nil {
		return config{}, fmt.Errorf("configure retry policy: %w", err)
	}
	telemetryConfig, err := telemetry.LoadConfig(
		defaultServiceName,
		"QUARRY_DISPATCHER_METRICS_ADDR",
		defaultMetricsAddress,
	)
	if err != nil {
		return config{}, err
	}

	return config{
		databaseURL:       databaseURL,
		dispatcherAddress: dispatcherAddress,
		leaseDuration:     leaseDuration,
		reaperInterval:    reaperInterval,
		reaperBatchSize:   reaperBatchSize,
		workerLiveness:    workerLiveness,
		retryBaseDelay:    retryBaseDelay,
		retryMaxDelay:     retryMaxDelay,
		telemetry:         telemetryConfig,
	}, nil
}

func run(ctx context.Context, cfg config, logger *slog.Logger) (runErr error) {
	telemetryRuntime, err := telemetry.New(ctx, cfg.telemetry)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		runErr = errors.Join(runErr, metricsServer.Shutdown(shutdownCtx))
	}()
	logger.Info("dispatcher metrics starting", slog.String("address", metricsServer.Address()))

	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := registerQueueHealthCollector(
		telemetryRuntime.Registry(),
		postgresQueueSnapshotSource{store: postgres.NewQueueSnapshotStore(pool)},
	); err != nil {
		return fmt.Errorf("register queue-health metrics: %w", err)
	}

	listener, err := net.Listen("tcp", cfg.dispatcherAddress)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", cfg.dispatcherAddress, err)
	}
	retryPolicy, err := domain.NewRetryPolicy(cfg.retryBaseDelay, cfg.retryMaxDelay, rand.Int64N)
	if err != nil {
		return fmt.Errorf("configure retry policy: %w", err)
	}
	store := postgres.NewDispatcherStoreWithTracer(
		pool,
		cfg.leaseDuration,
		retryPolicy,
		telemetryRuntime.Tracer("quarry/dispatcher/store"),
	)
	reaper, err := dispatcher.NewReaperWithMetrics(store, dispatcher.ReaperConfig{
		Interval:              cfg.reaperInterval,
		BatchSize:             cfg.reaperBatchSize,
		WorkerLivenessTimeout: cfg.workerLiveness,
	}, logger, telemetryRuntime.Metrics())
	if err != nil {
		return fmt.Errorf("configure lease reaper: %w", err)
	}

	server := grpc.NewServer(grpc.StatsHandler(telemetryRuntime.GRPCServerStatsHandler()))
	dispatcherv1.RegisterDispatcherServiceServer(
		server,
		dispatcher.NewServiceWithTelemetry(
			store,
			telemetryRuntime.Metrics(),
			telemetryRuntime.Tracer("quarry/dispatcher/service"),
			logger,
		),
	)
	logger.Info("dispatcher starting", slog.String("address", listener.Addr().String()))

	runCtx, cancel := context.WithCancel(ctx)
	reaperStopped := make(chan struct{})
	go func() {
		defer close(reaperStopped)
		reaper.Run(runCtx)
	}()

	err = serveDispatcher(runCtx, server, listener, logger)
	cancel()
	<-reaperStopped
	return err
}

func registerQueueHealthCollector(registerer prometheus.Registerer, source telemetry.QueueSnapshotSource) error {
	return registerer.Register(telemetry.NewQueueHealthCollector(source))
}

type postgresQueueSnapshotSource struct {
	store postgresQueueSnapshotReader
}

type postgresQueueSnapshotReader interface {
	QueueSnapshot(context.Context) (postgres.QueueSnapshot, error)
}

func (source postgresQueueSnapshotSource) QueueSnapshot(ctx context.Context) (telemetry.QueueSnapshot, error) {
	snapshot, err := source.store.QueueSnapshot(ctx)
	if err != nil {
		return telemetry.QueueSnapshot{}, err
	}
	return telemetry.QueueSnapshot{
		Queued:            snapshot.Queued,
		RetryWait:         snapshot.RetryWait,
		OldestEligibleAge: snapshot.OldestEligibleAge,
		ActiveJobs:        snapshot.ActiveJobs,
		ActiveWorkers:     snapshot.ActiveWorkers,
	}, nil
}

func serveDispatcher(
	ctx context.Context,
	server *grpc.Server,
	listener net.Listener,
	logger *slog.Logger,
) error {
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serverErrors:
		return unexpectedServeError(err)
	case <-ctx.Done():
		logger.Info("dispatcher shutting down")
	}

	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()

	timer := time.NewTimer(shutdownTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
	case <-timer.C:
		server.Stop()
		<-stopped
	}

	return unexpectedServeError(<-serverErrors)
}

func unexpectedServeError(err error) error {
	if err == nil || errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return fmt.Errorf("serve gRPC: %w", err)
}
