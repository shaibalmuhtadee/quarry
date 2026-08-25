package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	t.Setenv("QUARRY_DISPATCHER_ADDR", "")
	t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
	t.Setenv("QUARRY_WORKER_VERSION", "")
	t.Setenv("QUARRY_WORKER_CONCURRENCY", "")
	t.Setenv("QUARRY_HEARTBEAT_INTERVAL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dispatcherAddress != defaultDispatcherAddress || cfg.hostname != "test-host" ||
		cfg.version != defaultVersion || cfg.concurrency != defaultConcurrency ||
		cfg.heartbeatInterval != defaultHeartbeatInterval {
		t.Fatalf("default config = %#v", cfg)
	}

	t.Setenv("QUARRY_DISPATCHER_ADDR", "127.0.0.1:19090")
	t.Setenv("QUARRY_WORKER_HOSTNAME", "worker-a")
	t.Setenv("QUARRY_WORKER_VERSION", "v2")
	t.Setenv("QUARRY_WORKER_CONCURRENCY", "7")
	t.Setenv("QUARRY_HEARTBEAT_INTERVAL", "3s")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dispatcherAddress != "127.0.0.1:19090" || cfg.hostname != "worker-a" ||
		cfg.version != "v2" || cfg.concurrency != 7 || cfg.heartbeatInterval != 3*time.Second {
		t.Fatalf("overridden config = %#v", cfg)
	}
}

func TestLoadConfigRejectsInvalidHeartbeatInterval(t *testing.T) {
	for _, value := range []string{"invalid", "0s", "-1s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
			t.Setenv("QUARRY_HEARTBEAT_INTERVAL", value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig accepted heartbeat interval %q", value)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidConcurrency(t *testing.T) {
	for _, value := range []string{"0", "-1", "1.5", "4294967296"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
			t.Setenv("QUARRY_WORKER_CONCURRENCY", value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig accepted concurrency %q", value)
			}
		})
	}
}

func TestRunRegistersFreshIdentityAndStopsOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	service := &workerLifecycleService{acquired: make(chan string, 4)}
	dispatcherv1.RegisterDispatcherServiceServer(server, service)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-serveDone
	})

	cfg := config{
		dispatcherAddress: listener.Addr().String(),
		hostname:          "test-host",
		version:           "test-version",
		concurrency:       2,
		heartbeatInterval: 10 * time.Millisecond,
	}
	first := runUntilAcquisition(t, cfg, service.acquired)
	second := runUntilAcquisition(t, cfg, service.acquired)
	if first == second {
		t.Fatalf("two worker process starts reused ID %s", first)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.registrations) != 2 {
		t.Fatalf("registrations = %d, want 2", len(service.registrations))
	}
	for _, registration := range service.registrations {
		if _, err := domain.ParseWorkerID(registration.GetWorkerId()); err != nil {
			t.Fatalf("worker ID %q: %v", registration.GetWorkerId(), err)
		}
		if registration.GetHostname() != cfg.hostname || registration.GetVersion() != cfg.version ||
			registration.GetConcurrency() != cfg.concurrency || registration.GetStartedAt() == nil {
			t.Fatalf("registration = %#v", registration)
		}
	}
}

type workerLifecycleService struct {
	dispatcherv1.UnimplementedDispatcherServiceServer
	mu            sync.Mutex
	registrations []*dispatcherv1.RegisterWorkerRequest
	acquired      chan string
}

func (service *workerLifecycleService) Heartbeat(
	_ context.Context,
	request *dispatcherv1.HeartbeatRequest,
) (*dispatcherv1.HeartbeatResponse, error) {
	results := make([]*dispatcherv1.HeartbeatAttemptResult, len(request.GetActiveAttempts()))
	for i, attempt := range request.GetActiveAttempts() {
		results[i] = &dispatcherv1.HeartbeatAttemptResult{
			JobId:     attempt.GetJobId(),
			AttemptNo: attempt.GetAttemptNo(),
			State:     dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID,
		}
	}
	return &dispatcherv1.HeartbeatResponse{Attempts: results}, nil
}

func (service *workerLifecycleService) RegisterWorker(
	_ context.Context,
	request *dispatcherv1.RegisterWorkerRequest,
) (*dispatcherv1.RegisterWorkerResponse, error) {
	service.mu.Lock()
	service.registrations = append(service.registrations, request)
	service.mu.Unlock()
	return &dispatcherv1.RegisterWorkerResponse{}, nil
}

func (service *workerLifecycleService) AcquireJobs(
	_ context.Context,
	request *dispatcherv1.AcquireJobsRequest,
) (*dispatcherv1.AcquireJobsResponse, error) {
	if request.GetAvailableCapacity() != 2 {
		return nil, status.Errorf(codes.Internal, "capacity = %d", request.GetAvailableCapacity())
	}
	wantTypes := []string{"demo.echo", "demo.payload_size", "demo.sleep"}
	if !slices.Equal(request.GetSupportedJobTypes(), wantTypes) {
		return nil, status.Errorf(codes.Internal, "types = %v", request.GetSupportedJobTypes())
	}
	select {
	case service.acquired <- request.GetWorkerId():
	default:
	}
	return &dispatcherv1.AcquireJobsResponse{}, nil
}

func runUntilAcquisition(t *testing.T, cfg config, acquired <-chan string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	var workerID string
	select {
	case workerID = <-acquired:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for acquisition")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for worker shutdown")
	}
	return workerID
}
