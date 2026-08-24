package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/worker"
	"github.com/shaibalmuhtadee/quarry/internal/worker/handlers"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultDispatcherAddress = "localhost:9090"
	defaultConcurrency       = uint32(4)
	defaultVersion           = "dev"
	rpcTimeout               = 5 * time.Second
	idleBackoffMin           = 50 * time.Millisecond
	idleBackoffMax           = time.Second
	reportBackoffMin         = 100 * time.Millisecond
	reportBackoffMax         = 500 * time.Millisecond
)

type config struct {
	dispatcherAddress string
	hostname          string
	version           string
	concurrency       uint32
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

	return config{
		dispatcherAddress: dispatcherAddress,
		hostname:          hostname,
		version:           version,
		concurrency:       concurrency,
	}, nil
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	connection, err := grpc.NewClient(
		cfg.dispatcherAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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

	workerID := domain.NewWorkerID()
	startedAt := time.Now().UTC()
	runtime, err := worker.New(dispatcherClient, handlers.Registry(), worker.Config{
		Registration: worker.Registration{
			WorkerID:    workerID,
			Hostname:    cfg.hostname,
			Version:     cfg.version,
			Concurrency: cfg.concurrency,
			StartedAt:   startedAt,
		},
		IdleBackoffMin:   idleBackoffMin,
		IdleBackoffMax:   idleBackoffMax,
		ReportBackoffMin: reportBackoffMin,
		ReportBackoffMax: reportBackoffMax,
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
