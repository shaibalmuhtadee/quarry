package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("QUARRY_DATABASE_URL", "")
		t.Setenv("QUARRY_HTTP_ADDR", "")
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

		got, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if got.databaseURL != defaultDatabaseURL {
			t.Fatalf("database URL = %q, want %q", got.databaseURL, defaultDatabaseURL)
		}
		if got.httpAddress != defaultHTTPAddress {
			t.Fatalf("HTTP address = %q, want %q", got.httpAddress, defaultHTTPAddress)
		}
		if got.telemetry.ServiceName != defaultServiceName {
			t.Fatalf("telemetry service name = %q, want %q", got.telemetry.ServiceName, defaultServiceName)
		}
	})

	t.Run("environment overrides", func(t *testing.T) {
		t.Setenv("QUARRY_DATABASE_URL", "postgres://example/test")
		t.Setenv("QUARRY_HTTP_ADDR", "127.0.0.1:9090")
		t.Setenv("OTEL_SERVICE_NAME", "custom-api")

		got, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if got.databaseURL != "postgres://example/test" {
			t.Fatalf("database URL = %q, want environment value", got.databaseURL)
		}
		if got.httpAddress != "127.0.0.1:9090" {
			t.Fatalf("HTTP address = %q, want environment value", got.httpAddress)
		}
		if got.telemetry.ServiceName != "custom-api" {
			t.Fatalf("telemetry service name = %q, want environment value", got.telemetry.ServiceName)
		}
	})

	t.Run("invalid telemetry", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "collector:4318")
		if _, err := loadConfig(); err == nil {
			t.Fatal("loadConfig accepted an invalid OTLP traces endpoint")
		}
	})
}

func TestHTTPServerHasBoundedTimeouts(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for name, timeout := range map[string]time.Duration{
		"read header": server.ReadHeaderTimeout,
		"read":        server.ReadTimeout,
		"write":       server.WriteTimeout,
		"idle":        server.IdleTimeout,
	} {
		if timeout <= 0 {
			t.Fatalf("%s timeout = %s, want a positive duration", name, timeout)
		}
	}
}

func TestServeStartsAndShutsDownWithoutLeakingListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	server := newHTTPServer(address, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, server, listener, logger)
	}()

	waitForServer(t, address)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after cancellation")
	}

	connection, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("listener still accepts connections after shutdown")
	}
}

func waitForServer(t *testing.T, address string) {
	t.Helper()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := client.Get("http://" + address)
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("server response status = %d, want %d", response.StatusCode, http.StatusNoContent)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnexpectedServeError(t *testing.T) {
	if err := unexpectedServeError(nil); err != nil {
		t.Fatalf("nil serve error became %v", err)
	}
	if err := unexpectedServeError(http.ErrServerClosed); err != nil {
		t.Fatalf("ErrServerClosed became %v", err)
	}
	want := errors.New("listener failed")
	if err := unexpectedServeError(want); !errors.Is(err, want) {
		t.Fatalf("unexpectedServeError = %v, want wrapped listener error", err)
	}
}
