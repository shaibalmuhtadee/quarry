package main

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestDispatcherHealth(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		pinger := &postgresPingerStub{err: errors.New("PostgreSQL unavailable")}
		response, err := newDispatcherHealthServer(pinger).Check(
			context.Background(),
			&healthv1.HealthCheckRequest{Service: dispatcherLivenessService},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetStatus() != healthv1.HealthCheckResponse_SERVING {
			t.Fatalf("liveness status = %s, want SERVING", response.GetStatus())
		}
		if pinger.calls != 0 {
			t.Fatalf("liveness ping calls = %d, want 0", pinger.calls)
		}
	})

	t.Run("ready", func(t *testing.T) {
		pinger := &postgresPingerStub{}
		response, err := newDispatcherHealthServer(pinger).Check(
			context.Background(),
			&healthv1.HealthCheckRequest{Service: dispatcherReadinessService},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetStatus() != healthv1.HealthCheckResponse_SERVING {
			t.Fatalf("readiness status = %s, want SERVING", response.GetStatus())
		}
		if pinger.calls != 1 {
			t.Fatalf("readiness ping calls = %d, want 1", pinger.calls)
		}
	})

	t.Run("PostgreSQL unavailable", func(t *testing.T) {
		wantErr := errors.New("PostgreSQL unavailable")
		pinger := &postgresPingerStub{err: wantErr}
		response, err := newDispatcherHealthServer(pinger).Check(
			context.Background(),
			&healthv1.HealthCheckRequest{Service: dispatcherReadinessService},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetStatus() != healthv1.HealthCheckResponse_NOT_SERVING {
			t.Fatalf("readiness status = %s, want NOT_SERVING", response.GetStatus())
		}
	})

	t.Run("request context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		pinger := &postgresPingerStub{}
		_, err := newDispatcherHealthServer(pinger).Check(
			ctx,
			&healthv1.HealthCheckRequest{Service: dispatcherReadinessService},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(pinger.contextErr, context.Canceled) {
			t.Fatalf("ping context error = %v, want context.Canceled", pinger.contextErr)
		}
	})

	t.Run("unknown service", func(t *testing.T) {
		_, err := newDispatcherHealthServer(&postgresPingerStub{}).Check(
			context.Background(),
			&healthv1.HealthCheckRequest{Service: "unknown"},
		)
		if status.Code(err) != codes.NotFound {
			t.Fatalf("unknown service code = %s, want NotFound", status.Code(err))
		}
	})
}

func TestDispatcherHealthUsesStandardGRPCProtocol(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	healthv1.RegisterHealthServer(server, newDispatcherHealthServer(&postgresPingerStub{}))
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-done
	})

	connection, err := grpc.NewClient(
		"passthrough:///"+listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })

	response, err := healthv1.NewHealthClient(connection).Check(
		context.Background(),
		&healthv1.HealthCheckRequest{Service: dispatcherReadinessService},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetStatus() != healthv1.HealthCheckResponse_SERVING {
		t.Fatalf("readiness status = %s, want SERVING", response.GetStatus())
	}
}

type postgresPingerStub struct {
	err        error
	calls      int
	contextErr error
}

func (stub *postgresPingerStub) Ping(ctx context.Context) error {
	stub.calls++
	stub.contextErr = ctx.Err()
	return stub.err
}
