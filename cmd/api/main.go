package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/api"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

const (
	defaultDatabaseURL   = "postgres://quarry:quarry@localhost:5432/quarry?sslmode=disable"
	defaultHTTPAddress   = ":8080"
	readHeaderTimeout    = 5 * time.Second
	requestReadTimeout   = 15 * time.Second
	responseWriteTimeout = 15 * time.Second
	idleTimeout          = 60 * time.Second
	shutdownTimeout      = 10 * time.Second
)

type config struct {
	databaseURL string
	httpAddress string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, loadConfig(), logger); err != nil {
		logger.Error("api stopped", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("api stopped")
}

func loadConfig() config {
	databaseURL := os.Getenv("QUARRY_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = defaultDatabaseURL
	}
	httpAddress := os.Getenv("QUARRY_HTTP_ADDR")
	if httpAddress == "" {
		httpAddress = defaultHTTPAddress
	}

	return config{
		databaseURL: databaseURL,
		httpAddress: httpAddress,
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	listener, err := net.Listen("tcp", cfg.httpAddress)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", cfg.httpAddress, err)
	}
	server := newHTTPServer(cfg.httpAddress, api.NewHandler(postgres.NewJobStore(pool), pool, logger))
	logger.Info("api starting", slog.String("address", listener.Addr().String()))

	if err := serve(ctx, server, listener, logger); err != nil {
		return err
	}

	return nil
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       requestReadTimeout,
		WriteTimeout:      responseWriteTimeout,
		IdleTimeout:       idleTimeout,
	}
}

func serve(ctx context.Context, server *http.Server, listener net.Listener, logger *slog.Logger) error {
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serverErrors:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		logger.Info("api shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		closeErr := server.Close()
		serveErr := <-serverErrors
		return errors.Join(
			fmt.Errorf("shut down HTTP server: %w", err),
			closeErr,
			unexpectedServeError(serveErr),
		)
	}

	return unexpectedServeError(<-serverErrors)
}

func unexpectedServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err)
}
