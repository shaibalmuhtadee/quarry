package dispatcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
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

type recoveryCall struct {
	batchSize             int32
	workerLivenessTimeout time.Duration
}

type recoveryStoreStub struct {
	mu    sync.Mutex
	calls chan recoveryCall
	errs  []error
}

func (store *recoveryStoreStub) RecoverExpiredAttempts(
	_ context.Context,
	batchSize int32,
	workerLivenessTimeout time.Duration,
) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.calls != nil {
		store.calls <- recoveryCall{
			batchSize:             batchSize,
			workerLivenessTimeout: workerLivenessTimeout,
		}
	}
	if len(store.errs) == 0 {
		return 1, nil
	}
	err := store.errs[0]
	store.errs = store.errs[1:]
	return 0, err
}
