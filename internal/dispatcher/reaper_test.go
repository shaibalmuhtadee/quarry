package dispatcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
)

func TestReaperRetriesFailuresAndStopsWithContext(t *testing.T) {
	store := &recoveryStoreStub{
		calls: make(chan recoveryCall, 4),
		errs:  []error{errors.New("temporary database failure")},
	}
	config := ReaperConfig{
		Interval:              10 * time.Millisecond,
		BatchSize:             17,
		WorkerLivenessTimeout: 30 * time.Second,
	}
	reaper, err := NewReaper(store, config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("create reaper: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reaper.Run(ctx)
		close(done)
	}()

	for callNumber := 1; callNumber <= 2; callNumber++ {
		select {
		case call := <-store.calls:
			if call.batchSize != config.BatchSize || call.workerLivenessTimeout != config.WorkerLivenessTimeout {
				t.Fatalf("recovery call %d = %#v, want config %#v", callNumber, call, config)
			}
		case <-time.After(time.Second):
			t.Fatalf("recovery call %d did not arrive", callNumber)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reaper did not stop after cancellation")
	}
}

func TestNewReaperRejectsInvalidConfiguration(t *testing.T) {
	valid := ReaperConfig{
		Interval:              time.Second,
		BatchSize:             1,
		WorkerLivenessTimeout: time.Second,
	}
	tests := []struct {
		name   string
		change func(*ReaperConfig)
	}{
		{name: "interval", change: func(config *ReaperConfig) { config.Interval = 0 }},
		{name: "batch size", change: func(config *ReaperConfig) { config.BatchSize = 0 }},
		{name: "worker liveness timeout", change: func(config *ReaperConfig) { config.WorkerLivenessTimeout = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			_, err := NewReaper(&recoveryStoreStub{}, config, slog.Default())
			if !errors.Is(err, ErrInvalidReaperConfig) {
				t.Fatalf("NewReaper error = %v, want ErrInvalidReaperConfig", err)
			}
		})
	}
}

func TestReaperRecordsCommittedRecoveryOutcomes(t *testing.T) {
	jobType, err := domain.ParseJobType("demo.echo")
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryStoreStub{
		calls: make(chan recoveryCall, 1),
		transitions: []postgres.RecoveryTransition{
			{JobType: jobType, AttemptStatus: domain.AttemptStatusAbandoned, JobStatus: domain.JobStatusRetryWait, ErrorCode: "lease_expired"},
			{JobType: jobType, AttemptStatus: domain.AttemptStatusAbandoned, JobStatus: domain.JobStatusDeadLettered, ErrorCode: "lease_expired"},
			{JobType: jobType, AttemptStatus: domain.AttemptStatusCancelled, JobStatus: domain.JobStatusCancelled, ErrorCode: "cancellation_requested"},
		},
	}
	metrics := &recoveryMetricRecorder{}
	reaper, err := NewReaperWithMetrics(store, ReaperConfig{
		Interval:              time.Hour,
		BatchSize:             3,
		WorkerLivenessTimeout: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reaper.Run(ctx)
		close(done)
	}()
	<-store.calls
	deadline := time.Now().Add(time.Second)
	for metrics.leaseCount() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	if len(metrics.attempts) != 3 {
		t.Fatalf("attempt metrics = %d, want 3", len(metrics.attempts))
	}
	if len(metrics.leaseOutcomes) != 3 || metrics.leaseOutcomes[0] != domain.JobStatusRetryWait ||
		metrics.leaseOutcomes[1] != domain.JobStatusDeadLettered || metrics.leaseOutcomes[2] != domain.JobStatusCancelled {
		t.Fatalf("lease outcomes = %v", metrics.leaseOutcomes)
	}
	if len(metrics.retries) != 1 || metrics.retries[0] != telemetry.RetryReasonLeaseExpired {
		t.Fatalf("retry reasons = %v", metrics.retries)
	}
}

type recoveryMetricRecorder struct {
	mu            sync.Mutex
	attempts      []domain.AttemptStatus
	leaseOutcomes []domain.JobStatus
	retries       []telemetry.RetryReason
}

func (metrics *recoveryMetricRecorder) AttemptCompleted(_ domain.JobType, status domain.AttemptStatus, _ string) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.attempts = append(metrics.attempts, status)
}

func (metrics *recoveryMetricRecorder) LeaseExpired(status domain.JobStatus) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.leaseOutcomes = append(metrics.leaseOutcomes, status)
}

func (metrics *recoveryMetricRecorder) RetryScheduled(reason telemetry.RetryReason) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.retries = append(metrics.retries, reason)
}

func (metrics *recoveryMetricRecorder) leaseCount() int {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return len(metrics.leaseOutcomes)
}

type recoveryCall struct {
	batchSize             int32
	workerLivenessTimeout time.Duration
}

type recoveryStoreStub struct {
	mu          sync.Mutex
	calls       chan recoveryCall
	errs        []error
	transitions []postgres.RecoveryTransition
}

func (store *recoveryStoreStub) RecoverExpiredAttemptTransitions(
	_ context.Context,
	batchSize int32,
	workerLivenessTimeout time.Duration,
) ([]postgres.RecoveryTransition, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.calls != nil {
		store.calls <- recoveryCall{
			batchSize:             batchSize,
			workerLivenessTimeout: workerLivenessTimeout,
		}
	}
	if len(store.errs) == 0 {
		if store.transitions != nil {
			return append([]postgres.RecoveryTransition(nil), store.transitions...), nil
		}
		return []postgres.RecoveryTransition{{}}, nil
	}
	err := store.errs[0]
	store.errs = store.errs[1:]
	return nil, err
}
