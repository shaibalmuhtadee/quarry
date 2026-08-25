package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	metricsReadHeaderTimeout = 5 * time.Second
	metricsReadTimeout       = 10 * time.Second
	metricsWriteTimeout      = 10 * time.Second
	metricsIdleTimeout       = 30 * time.Second
)

type MetricsServer struct {
	server   *http.Server
	address  string
	done     chan struct{}
	mu       sync.Mutex
	serveErr error
}

func ListenMetrics(address string, handler http.Handler) (*MetricsServer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for metrics on %q: %w", address, err)
	}

	metricsServer := &MetricsServer{
		server: &http.Server{
			Addr:              address,
			Handler:           handler,
			ReadHeaderTimeout: metricsReadHeaderTimeout,
			ReadTimeout:       metricsReadTimeout,
			WriteTimeout:      metricsWriteTimeout,
			IdleTimeout:       metricsIdleTimeout,
		},
		address: listener.Addr().String(),
		done:    make(chan struct{}),
	}
	go func() {
		err := metricsServer.server.Serve(listener)
		metricsServer.mu.Lock()
		metricsServer.serveErr = err
		metricsServer.mu.Unlock()
		close(metricsServer.done)
	}()

	return metricsServer, nil
}

func (server *MetricsServer) Address() string {
	return server.address
}

func (server *MetricsServer) Shutdown(ctx context.Context) error {
	shutdownErr := server.server.Shutdown(ctx)
	select {
	case <-server.done:
	case <-ctx.Done():
		return errors.Join(shutdownErr, ctx.Err())
	}

	server.mu.Lock()
	serveErr := server.serveErr
	server.mu.Unlock()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(shutdownErr, serveErr)
}
