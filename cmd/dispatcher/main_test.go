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

		got := loadConfig()
		if got.databaseURL != defaultDatabaseURL {
			t.Fatalf("database URL = %q, want %q", got.databaseURL, defaultDatabaseURL)
		}
		if got.dispatcherAddress != defaultDispatcherAddress {
			t.Fatalf("dispatcher address = %q, want %q", got.dispatcherAddress, defaultDispatcherAddress)
		}
	})

	t.Run("environment overrides", func(t *testing.T) {
		t.Setenv("QUARRY_DATABASE_URL", "postgres://example/test")
		t.Setenv("QUARRY_DISPATCHER_ADDR", "127.0.0.1:19090")

		got := loadConfig()
		if got.databaseURL != "postgres://example/test" {
			t.Fatalf("database URL = %q, want environment value", got.databaseURL)
		}
		if got.dispatcherAddress != "127.0.0.1:19090" {
			t.Fatalf("dispatcher address = %q, want environment value", got.dispatcherAddress)
		}
	})
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
