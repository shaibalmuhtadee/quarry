package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	clearTestFaultEnvironment(t)
	t.Setenv("QUARRY_DISPATCHER_ADDR", "")
	t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
	t.Setenv("QUARRY_WORKER_VERSION", "")
	t.Setenv("QUARRY_WORKER_CONCURRENCY", "")
	t.Setenv("QUARRY_HEARTBEAT_INTERVAL", "")
	t.Setenv("QUARRY_WORKER_SHUTDOWN_TIMEOUT", "")
	t.Setenv("QUARRY_WORKER_METRICS_ADDR", "")
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dispatcherAddress != defaultDispatcherAddress || cfg.hostname != "test-host" ||
		cfg.version != defaultVersion || cfg.concurrency != defaultConcurrency ||
		cfg.heartbeatInterval != defaultHeartbeatInterval || cfg.shutdownTimeout != defaultShutdownTimeout {
		t.Fatalf("default config = %#v", cfg)
	}
	if cfg.telemetry.ServiceName != defaultServiceName || cfg.telemetry.MetricsAddress != defaultMetricsAddress {
		t.Fatalf("default telemetry config = %#v", cfg.telemetry)
	}
	if cfg.testFault.handlerEnabled() || cfg.testFault.exitEnabled() {
		t.Fatalf("default test fault config = %#v", cfg.testFault)
	}

	t.Setenv("QUARRY_DISPATCHER_ADDR", "127.0.0.1:19090")
	t.Setenv("QUARRY_WORKER_HOSTNAME", "worker-a")
	t.Setenv("QUARRY_WORKER_VERSION", "v2")
	t.Setenv("QUARRY_WORKER_CONCURRENCY", "7")
	t.Setenv("QUARRY_HEARTBEAT_INTERVAL", "3s")
	t.Setenv("QUARRY_WORKER_SHUTDOWN_TIMEOUT", "7s")
	t.Setenv("QUARRY_WORKER_METRICS_ADDR", "127.0.0.1:19465")
	t.Setenv("OTEL_SERVICE_NAME", "custom-worker")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dispatcherAddress != "127.0.0.1:19090" || cfg.hostname != "worker-a" ||
		cfg.version != "v2" || cfg.concurrency != 7 || cfg.heartbeatInterval != 3*time.Second ||
		cfg.shutdownTimeout != 7*time.Second {
		t.Fatalf("overridden config = %#v", cfg)
	}
	if cfg.telemetry.ServiceName != "custom-worker" || cfg.telemetry.MetricsAddress != "127.0.0.1:19465" {
		t.Fatalf("overridden telemetry config = %#v", cfg.telemetry)
	}
}

func TestLoadConfigRejectsInvalidHeartbeatInterval(t *testing.T) {
	clearTestFaultEnvironment(t)
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
	clearTestFaultEnvironment(t)
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

func TestLoadConfigRejectsInvalidShutdownTimeout(t *testing.T) {
	clearTestFaultEnvironment(t)
	for _, value := range []string{"invalid", "0s", "-1s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
			t.Setenv("QUARRY_WORKER_SHUTDOWN_TIMEOUT", value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig accepted shutdown timeout %q", value)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidMetricsAddress(t *testing.T) {
	clearTestFaultEnvironment(t)
	t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
	t.Setenv("QUARRY_WORKER_METRICS_ADDR", "invalid address")
	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig accepted an invalid metrics address")
	}
}

func TestLoadConfigAcceptsCompleteTestFaultConfig(t *testing.T) {
	clearTestFaultEnvironment(t)
	t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
	markerPath := filepath.Join(t.TempDir(), "side-effects.log")
	t.Setenv(testSideEffectFileEnv, markerPath)
	t.Setenv(testExitAfterSuccessEnv, "true")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.testFault.handlerEnabled() || !cfg.testFault.exitEnabled() ||
		cfg.testFault.sideEffectFile != filepath.Clean(markerPath) {
		t.Fatalf("test fault config = %#v", cfg.testFault)
	}
}

func TestLoadConfigAcceptsTestSideEffectHandlerWithoutExit(t *testing.T) {
	clearTestFaultEnvironment(t)
	t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
	markerPath := filepath.Join(t.TempDir(), "side-effects.log")
	t.Setenv(testSideEffectFileEnv, markerPath)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.testFault.handlerEnabled() || cfg.testFault.exitEnabled() ||
		cfg.testFault.sideEffectFile != filepath.Clean(markerPath) {
		t.Fatalf("test handler config = %#v", cfg.testFault)
	}
}

func TestLoadConfigRejectsInvalidTestFaultConfig(t *testing.T) {
	absoluteMarker := filepath.Join(t.TempDir(), "side-effects.log")
	missingParentMarker := filepath.Join(t.TempDir(), "missing", "side-effects.log")
	directoryMarker := t.TempDir()
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		markerPath string
		exitValue  string
	}{
		{name: "exit only", exitValue: "true"},
		{name: "false exit", markerPath: absoluteMarker, exitValue: "false"},
		{name: "numeric exit", markerPath: absoluteMarker, exitValue: "1"},
		{name: "relative marker", markerPath: "side-effects.log", exitValue: "true"},
		{name: "missing parent", markerPath: missingParentMarker, exitValue: "true"},
		{name: "parent is file", markerPath: filepath.Join(parentFile, "side-effects.log"), exitValue: "true"},
		{name: "marker is directory", markerPath: directoryMarker, exitValue: "true"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("QUARRY_WORKER_HOSTNAME", "test-host")
			t.Setenv(testSideEffectFileEnv, test.markerPath)
			t.Setenv(testExitAfterSuccessEnv, test.exitValue)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig accepted marker %q and exit value %q", test.markerPath, test.exitValue)
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
		shutdownTimeout:   time.Second,
		telemetry: telemetry.Config{
			ServiceName:    defaultServiceName,
			MetricsAddress: "127.0.0.1:0",
		},
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

func TestRunTestFaultWritesMarkerAndStopsBeforeReport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	service := &testFaultLifecycleService{jobID: domain.NewJobID().String()}
	dispatcherv1.RegisterDispatcherServiceServer(server, service)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		<-serveDone
	})

	markerPath := filepath.Join(t.TempDir(), "side-effects.log")
	cfg := config{
		dispatcherAddress: listener.Addr().String(),
		hostname:          "fault-test-host",
		version:           "fault-test",
		concurrency:       1,
		heartbeatInterval: 10 * time.Millisecond,
		shutdownTimeout:   time.Second,
		telemetry: telemetry.Config{
			ServiceName:    defaultServiceName,
			MetricsAddress: "127.0.0.1:0",
		},
		testFault: testFaultConfig{
			sideEffectFile:          markerPath,
			exitAfterHandlerSuccess: true,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, errTestExitAfterHandlerSuccess) {
		t.Fatalf("run error = %v, want injected test fault", err)
	}
	contents, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "completed\n"; got != want {
		t.Fatalf("marker contents = %q, want %q", got, want)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	wantTypes := []string{"demo.echo", "demo.payload_size", "demo.sleep", "test.side_effect"}
	if !slices.Equal(service.supportedTypes, wantTypes) {
		t.Fatalf("supported job types = %v, want %v", service.supportedTypes, wantTypes)
	}
	if service.reportCalls != 0 {
		t.Fatalf("attempt report calls = %d, want 0", service.reportCalls)
	}
}

type testFaultLifecycleService struct {
	dispatcherv1.UnimplementedDispatcherServiceServer
	mu             sync.Mutex
	jobID          string
	jobSent        bool
	supportedTypes []string
	reportCalls    int
}

func (service *testFaultLifecycleService) RegisterWorker(
	context.Context,
	*dispatcherv1.RegisterWorkerRequest,
) (*dispatcherv1.RegisterWorkerResponse, error) {
	return &dispatcherv1.RegisterWorkerResponse{}, nil
}

func (service *testFaultLifecycleService) AcquireJobs(
	_ context.Context,
	request *dispatcherv1.AcquireJobsRequest,
) (*dispatcherv1.AcquireJobsResponse, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.supportedTypes = append([]string(nil), request.GetSupportedJobTypes()...)
	if service.jobSent {
		return &dispatcherv1.AcquireJobsResponse{}, nil
	}
	service.jobSent = true
	return &dispatcherv1.AcquireJobsResponse{Jobs: []*dispatcherv1.AcquiredJob{{
		JobId:       service.jobID,
		AttemptNo:   1,
		JobType:     "test.side_effect",
		PayloadJson: []byte(`{}`),
		TimeoutMs:   30_000,
	}}}, nil
}

func (service *testFaultLifecycleService) Heartbeat(
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

func (service *testFaultLifecycleService) ReportAttempt(
	context.Context,
	*dispatcherv1.ReportAttemptRequest,
) (*dispatcherv1.ReportAttemptResponse, error) {
	service.mu.Lock()
	service.reportCalls++
	service.mu.Unlock()
	return &dispatcherv1.ReportAttemptResponse{}, nil
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

func clearTestFaultEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv(testSideEffectFileEnv, "")
	t.Setenv(testExitAfterSuccessEnv, "")
}
