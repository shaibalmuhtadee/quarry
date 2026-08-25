package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/dispatcher"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"google.golang.org/grpc"
)

const (
	defaultDatabaseURL       = "postgres://quarry:quarry@localhost:5432/quarry?sslmode=disable"
	defaultDispatcherAddress = "localhost:9090"
	defaultLeaseDuration     = 20 * time.Second
	defaultReaperInterval    = time.Second
	defaultReaperBatchSize   = int32(100)
	shutdownTimeout          = 10 * time.Second
)

type config struct {
	databaseURL       string
	dispatcherAddress string
	leaseDuration     time.Duration
	reaperInterval    time.Duration
	reaperBatchSize   int32
	workerLiveness    time.Duration
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

	return config{
		databaseURL:       databaseURL,
		dispatcherAddress: dispatcherAddress,
		leaseDuration:     leaseDuration,
		reaperInterval:    reaperInterval,
		reaperBatchSize:   reaperBatchSize,
		workerLiveness:    workerLiveness,
	}, nil
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	listener, err := net.Listen("tcp", cfg.dispatcherAddress)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", cfg.dispatcherAddress, err)
	}
	store := postgres.NewDispatcherStore(pool, cfg.leaseDuration)
	reaper, err := dispatcher.NewReaper(store, dispatcher.ReaperConfig{
		Interval:              cfg.reaperInterval,
		BatchSize:             cfg.reaperBatchSize,
		WorkerLivenessTimeout: cfg.workerLiveness,
	}, logger)
	if err != nil {
		return fmt.Errorf("configure lease reaper: %w", err)
	}

	server := grpc.NewServer()
	dispatcherv1.RegisterDispatcherServiceServer(
		server,
		dispatcher.NewService(store),
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
