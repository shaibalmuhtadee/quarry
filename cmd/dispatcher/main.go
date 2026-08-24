package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
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
	shutdownTimeout          = 10 * time.Second
)

type config struct {
	databaseURL       string
	dispatcherAddress string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, loadConfig(), logger); err != nil {
		logger.Error("dispatcher stopped", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("dispatcher stopped")
}

func loadConfig() config {
	databaseURL := os.Getenv("QUARRY_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = defaultDatabaseURL
	}
	dispatcherAddress := os.Getenv("QUARRY_DISPATCHER_ADDR")
	if dispatcherAddress == "" {
		dispatcherAddress = defaultDispatcherAddress
	}

	return config{
		databaseURL:       databaseURL,
		dispatcherAddress: dispatcherAddress,
	}
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
	server := grpc.NewServer()
	dispatcherv1.RegisterDispatcherServiceServer(
		server,
		dispatcher.NewService(postgres.NewDispatcherStore(pool)),
	)
	logger.Info("dispatcher starting", slog.String("address", listener.Addr().String()))

	return serveDispatcher(ctx, server, listener, logger)
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
