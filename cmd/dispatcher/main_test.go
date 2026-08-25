package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestLoadConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("QUARRY_DATABASE_URL", "")
		t.Setenv("QUARRY_DISPATCHER_ADDR", "")
		t.Setenv("QUARRY_LEASE_DURATION", "")
		t.Setenv("QUARRY_REAPER_INTERVAL", "")
		t.Setenv("QUARRY_REAPER_BATCH_SIZE", "")
		t.Setenv("QUARRY_WORKER_LIVENESS_TIMEOUT", "")

		got, err := loadConfig()
		if err != nil {
			t.Fatalf("load default config: %v", err)
		}
		if got.databaseURL != defaultDatabaseURL {
			t.Fatalf("database URL = %q, want %q", got.databaseURL, defaultDatabaseURL)
		}
		if got.dispatcherAddress != defaultDispatcherAddress {
			t.Fatalf("dispatcher address = %q, want %q", got.dispatcherAddress, defaultDispatcherAddress)
		}
		if got.leaseDuration != defaultLeaseDuration {
			t.Fatalf("lease duration = %s, want %s", got.leaseDuration, defaultLeaseDuration)
		}
		if got.reaperInterval != defaultReaperInterval || got.reaperBatchSize != defaultReaperBatchSize {
			t.Fatalf("reaper config = (%s, %d), want (%s, %d)", got.reaperInterval, got.reaperBatchSize, defaultReaperInterval, defaultReaperBatchSize)
		}
		if got.workerLiveness != defaultLeaseDuration {
			t.Fatalf("worker liveness timeout = %s, want default lease duration %s", got.workerLiveness, defaultLeaseDuration)
		}
	})

	t.Run("environment overrides", func(t *testing.T) {
		t.Setenv("QUARRY_DATABASE_URL", "postgres://example/test")
		t.Setenv("QUARRY_DISPATCHER_ADDR", "127.0.0.1:19090")
		t.Setenv("QUARRY_LEASE_DURATION", "45s")
		t.Setenv("QUARRY_REAPER_INTERVAL", "250ms")
		t.Setenv("QUARRY_REAPER_BATCH_SIZE", "25")
		t.Setenv("QUARRY_WORKER_LIVENESS_TIMEOUT", "1m")

		got, err := loadConfig()
		if err != nil {
			t.Fatalf("load overridden config: %v", err)
		}
		if got.databaseURL != "postgres://example/test" {
			t.Fatalf("database URL = %q, want environment value", got.databaseURL)
		}
		if got.dispatcherAddress != "127.0.0.1:19090" {
			t.Fatalf("dispatcher address = %q, want environment value", got.dispatcherAddress)
		}
		if got.leaseDuration != 45*time.Second {
			t.Fatalf("lease duration = %s, want 45s", got.leaseDuration)
		}
		if got.reaperInterval != 250*time.Millisecond || got.reaperBatchSize != 25 {
			t.Fatalf("reaper config = (%s, %d), want (250ms, 25)", got.reaperInterval, got.reaperBatchSize)
		}
		if got.workerLiveness != time.Minute {
			t.Fatalf("worker liveness timeout = %s, want 1m", got.workerLiveness)
		}
	})

	for _, value := range []string{"invalid", "0s", "-1s", "500us"} {
		t.Run("invalid lease duration "+value, func(t *testing.T) {
			t.Setenv("QUARRY_LEASE_DURATION", value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig accepted lease duration %q", value)
			}
		})
	}

	for name, test := range map[string]struct {
		variable string
		value    string
	}{
		"reaper interval syntax": {variable: "QUARRY_REAPER_INTERVAL", value: "invalid"},
		"reaper interval zero":   {variable: "QUARRY_REAPER_INTERVAL", value: "0s"},
		"reaper batch syntax":    {variable: "QUARRY_REAPER_BATCH_SIZE", value: "invalid"},
		"reaper batch zero":      {variable: "QUARRY_REAPER_BATCH_SIZE", value: "0"},
		"worker liveness syntax": {variable: "QUARRY_WORKER_LIVENESS_TIMEOUT", value: "invalid"},
		"worker liveness zero":   {variable: "QUARRY_WORKER_LIVENESS_TIMEOUT", value: "0s"},
		"worker liveness sub-ms": {variable: "QUARRY_WORKER_LIVENESS_TIMEOUT", value: "500us"},
	} {
		t.Run("invalid "+name, func(t *testing.T) {
			t.Setenv("QUARRY_REAPER_INTERVAL", "")
			t.Setenv("QUARRY_REAPER_BATCH_SIZE", "")
			t.Setenv("QUARRY_WORKER_LIVENESS_TIMEOUT", "")
			t.Setenv(test.variable, test.value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig accepted %s=%q", test.variable, test.value)
			}
		})
	}
}

func TestServeDispatcherStartsAndShutsDownWithoutLeakingListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	server := grpc.NewServer()
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() {
		done <- serveDispatcher(ctx, server, listener, logger)
	}()

	waitForGRPCServer(t, address)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveDispatcher returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveDispatcher did not return after cancellation")
	}

	connection, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("dispatcher listener still accepts connections after shutdown")
	}
}

func waitForGRPCServer(t *testing.T, address string) {
	t.Helper()
	connection, err := grpc.NewClient(
		"passthrough:///"+address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("create gRPC client: %v", err)
	}
	defer connection.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		requestCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := connection.Invoke(requestCtx, "/test.Service/Ping", &emptypb.Empty{}, &emptypb.Empty{})
		cancel()
		if status.Code(err) == codes.Unimplemented {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gRPC server did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnexpectedServeError(t *testing.T) {
	if err := unexpectedServeError(nil); err != nil {
		t.Fatalf("nil serve error became %v", err)
	}
	if err := unexpectedServeError(grpc.ErrServerStopped); err != nil {
		t.Fatalf("ErrServerStopped became %v", err)
	}
	want := errors.New("listener failed")
	if err := unexpectedServeError(want); !errors.Is(err, want) {
		t.Fatalf("unexpectedServeError = %v, want wrapped listener error", err)
	}
}
