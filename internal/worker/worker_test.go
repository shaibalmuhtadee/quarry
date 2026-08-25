package worker

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeDispatcher struct {
	register  func(context.Context, Registration) error
	acquire   func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error)
	heartbeat func(context.Context, domain.WorkerID, []HeartbeatAttempt) ([]HeartbeatResult, error)
	report    func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.Result) error
}

func (dispatcher *fakeDispatcher) Heartbeat(
	ctx context.Context,
	workerID domain.WorkerID,
	attempts []HeartbeatAttempt,
) ([]HeartbeatResult, error) {
	if dispatcher.heartbeat != nil {
		return dispatcher.heartbeat(ctx, workerID, attempts)
	}
	results := make([]HeartbeatResult, len(attempts))
	for i, attempt := range attempts {
		results[i] = HeartbeatResult{Attempt: attempt, Valid: true}
	}
	return results, nil
}

func (dispatcher *fakeDispatcher) Register(ctx context.Context, registration Registration) error {
	if dispatcher.register == nil {
		return nil
	}
	return dispatcher.register(ctx, registration)
}

func (dispatcher *fakeDispatcher) Acquire(
	ctx context.Context,
	workerID domain.WorkerID,
	capacity uint32,
	supportedTypes []domain.JobType,
) ([]Job, error) {
	return dispatcher.acquire(ctx, workerID, capacity, supportedTypes)
}

func (dispatcher *fakeDispatcher) ReportSuccess(
	ctx context.Context,
	workerID domain.WorkerID,
	jobID domain.JobID,
	attemptNumber domain.AttemptNumber,
	result domain.Result,
) error {
	return dispatcher.report(ctx, workerID, jobID, attemptNumber, result)
}

func TestWorkerBoundsExecutionAndAdvertisesFreeCapacity(t *testing.T) {
	t.Parallel()

	const concurrency = uint32(3)
	jobs := makeJobs(t, 6, "demo.test")
	var acquireMu sync.Mutex
	remaining := append([]Job(nil), jobs...)
	var capacities []uint32
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(
		_ context.Context,
		_ domain.WorkerID,
		capacity uint32,
		_ []domain.JobType,
	) ([]Job, error) {
		acquireMu.Lock()
		defer acquireMu.Unlock()
		capacities = append(capacities, capacity)
		count := min(int(capacity), len(remaining))
		acquired := append([]Job(nil), remaining[:count]...)
		remaining = remaining[count:]
		return acquired, nil
	}
	reported := make(chan struct{}, len(jobs))
	dispatcher.report = func(
		context.Context,
		domain.WorkerID,
		domain.JobID,
		domain.AttemptNumber,
		domain.Result,
	) error {
		reported <- struct{}{}
		return nil
	}

	gate := make(chan struct{})
	started := make(chan struct{}, len(jobs))
	var active atomic.Int32
	var maximum atomic.Int32
	handler := func(context.Context, domain.Payload) (domain.Result, error) {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		<-gate
		active.Add(-1)
		return mustResult(t, `{"ok":true}`), nil
	}
	runtime := newTestWorker(t, dispatcher, concurrency, map[string]Handler{"demo.test": handler})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	for range concurrency {
		awaitSignal(t, started)
	}
	select {
	case <-started:
		t.Fatal("worker started more jobs than configured concurrency")
	case <-time.After(25 * time.Millisecond):
	}
	close(gate)
	for range jobs {
		awaitSignal(t, reported)
	}
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}

	if got := maximum.Load(); got != int32(concurrency) {
		t.Fatalf("maximum concurrent handlers = %d, want %d", got, concurrency)
	}
	acquireMu.Lock()
	defer acquireMu.Unlock()
	if len(capacities) < 2 || capacities[0] != concurrency {
		t.Fatalf("advertised capacities = %v", capacities)
	}
	for _, capacity := range capacities {
		if capacity == 0 || capacity > concurrency {
			t.Fatalf("advertised capacity %d outside 1..%d", capacity, concurrency)
		}
	}
}

func TestWorkerHoldsSlotUntilSuccessReportIsAcknowledged(t *testing.T) {
	t.Parallel()

	jobs := makeJobs(t, 2, "demo.test")
	var acquireCalls atomic.Int32
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(
		_ context.Context,
		_ domain.WorkerID,
		capacity uint32,
		_ []domain.JobType,
	) ([]Job, error) {
		call := acquireCalls.Add(1)
		if capacity != 1 {
			t.Errorf("capacity = %d, want 1", capacity)
		}
		if call <= 2 {
			return []Job{jobs[call-1]}, nil
		}
		return nil, nil
	}
	firstReport := make(chan struct{})
	releaseFirstReport := make(chan struct{})
	allReported := make(chan struct{})
	var reports atomic.Int32
	dispatcher.report = func(
		_ context.Context,
		_ domain.WorkerID,
		_ domain.JobID,
		_ domain.AttemptNumber,
		_ domain.Result,
	) error {
		if reports.Add(1) == 1 {
			close(firstReport)
			<-releaseFirstReport
		} else {
			close(allReported)
		}
		return nil
	}
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.test": func(context.Context, domain.Payload) (domain.Result, error) {
			return mustResult(t, `{"ok":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, firstReport)
	time.Sleep(25 * time.Millisecond)
	if got := acquireCalls.Load(); got != 1 {
		t.Fatalf("acquisition calls while report was unacknowledged = %d, want 1", got)
	}
	close(releaseFirstReport)
	awaitSignal(t, allReported)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRetriesTransientReportWithSameIdentity(t *testing.T) {
	t.Parallel()

	job := makeJobs(t, 1, "demo.test")[0]
	workerID := domain.NewWorkerID()
	type reportCall struct {
		workerID domain.WorkerID
		jobID    domain.JobID
		attempt  domain.AttemptNumber
		result   string
	}
	var mu sync.Mutex
	var calls []reportCall
	reported := make(chan struct{})
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(
		context.Context,
		domain.WorkerID,
		uint32,
		[]domain.JobType,
	) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{job}, nil
		}
		return nil, nil
	}
	dispatcher.report = func(
		_ context.Context,
		gotWorkerID domain.WorkerID,
		gotJobID domain.JobID,
		gotAttempt domain.AttemptNumber,
		result domain.Result,
	) error {
		mu.Lock()
		calls = append(calls, reportCall{gotWorkerID, gotJobID, gotAttempt, string(result.JSON())})
		callCount := len(calls)
		mu.Unlock()
		if callCount == 1 {
			return status.Error(codes.Unavailable, "temporary")
		}
		close(reported)
		return nil
	}
	runtime := newTestWorkerWithID(t, dispatcher, 1, workerID, map[string]Handler{
		"demo.test": func(context.Context, domain.Payload) (domain.Result, error) {
			return mustResult(t, `{"done":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, reported)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("report calls = %d, want 2", len(calls))
	}
	for i, call := range calls {
		if call.workerID != workerID || call.jobID != job.ID ||
			call.attempt != job.AttemptNumber || call.result != `{"done":true}` {
			t.Fatalf("report call %d = %#v", i, call)
		}
	}
}

func TestActiveAttemptsTrackIdentityAndCancelOnlyTheStaleAttempt(t *testing.T) {
	t.Parallel()

	jobs := makeJobs(t, 2, "demo.test")
	active := newActiveAttempts()
	first, err := active.add(context.Background(), jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := active.add(context.Background(), jobs[1])
	if err != nil {
		t.Fatal(err)
	}
	snapshot := active.snapshot()
	if len(snapshot) != 2 || !slices.Contains(snapshot, first.identity) || !slices.Contains(snapshot, second.identity) {
		t.Fatalf("active snapshot = %#v", snapshot)
	}
	if !active.cancel(first.identity, errLeaseStale) {
		t.Fatal("stale attempt was not removed")
	}
	if !errors.Is(context.Cause(first.ctx), errLeaseStale) {
		t.Fatalf("stale attempt cause = %v", context.Cause(first.ctx))
	}
	select {
	case <-second.ctx.Done():
		t.Fatalf("unrelated attempt was canceled: %v", context.Cause(second.ctx))
	default:
	}
	if active.len() != 1 || !active.remove(second, nil) || active.len() != 0 {
		t.Fatalf("active registry did not converge to empty")
	}
}

func TestWorkerHeartbeatsExecutingAndReportingAttemptUntilAcknowledged(t *testing.T) {
	t.Parallel()

	job := makeJobs(t, 1, "demo.test")[0]
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{job}, nil
		}
		return nil, nil
	}
	heartbeats := make(chan []HeartbeatAttempt, 32)
	executingHeartbeated := make(chan struct{})
	reportingHeartbeated := make(chan struct{})
	var executingOnce, reportingOnce sync.Once
	var reporting atomic.Bool
	dispatcher.heartbeat = func(
		_ context.Context,
		_ domain.WorkerID,
		attempts []HeartbeatAttempt,
	) ([]HeartbeatResult, error) {
		copyOfAttempts := append([]HeartbeatAttempt(nil), attempts...)
		select {
		case heartbeats <- copyOfAttempts:
		default:
		}
		results := make([]HeartbeatResult, len(attempts))
		for i, attempt := range attempts {
			if attempt.JobID == job.ID {
				if reporting.Load() {
					reportingOnce.Do(func() { close(reportingHeartbeated) })
				} else {
					executingOnce.Do(func() { close(executingHeartbeated) })
				}
			}
			results[i] = HeartbeatResult{Attempt: attempt, Valid: true}
		}
		return results, nil
	}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	reportStarted := make(chan struct{})
	releaseReport := make(chan struct{})
	reported := make(chan struct{})
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.Result) error {
		reporting.Store(true)
		close(reportStarted)
		<-releaseReport
		close(reported)
		return nil
	}
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.test": func(context.Context, domain.Payload) (domain.Result, error) {
			close(handlerStarted)
			<-releaseHandler
			return mustResult(t, `{"ok":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, handlerStarted)
	awaitSignal(t, executingHeartbeated)
	close(releaseHandler)
	awaitSignal(t, reportStarted)
	awaitSignal(t, reportingHeartbeated)
	close(releaseReport)
	awaitSignal(t, reported)
	awaitHeartbeatWithoutAttempts(t, heartbeats)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerHeartbeatsAttemptBufferedAfterStaleExecutionCancellation(t *testing.T) {
	t.Parallel()

	jobs := makeJobs(t, 2, "demo.test")
	var acquireCalls atomic.Int32
	replacementAcquisition := make(chan struct{})
	var replacementOnce sync.Once
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		call := acquireCalls.Add(1)
		if call <= 2 {
			return []Job{jobs[call-1]}, nil
		}
		replacementOnce.Do(func() { close(replacementAcquisition) })
		return nil, nil
	}
	staleCanceled := make(chan struct{})
	bufferedHeartbeated := make(chan struct{})
	var staleOnce, bufferedOnce sync.Once
	dispatcher.heartbeat = func(
		_ context.Context,
		_ domain.WorkerID,
		attempts []HeartbeatAttempt,
	) ([]HeartbeatResult, error) {
		results := make([]HeartbeatResult, len(attempts))
		for i, attempt := range attempts {
			valid := false
			if !valid {
				staleOnce.Do(func() { close(staleCanceled) })
			}
			if attempt.JobID == jobs[1].ID {
				bufferedOnce.Do(func() { close(bufferedHeartbeated) })
			}
			results[i] = HeartbeatResult{Attempt: attempt, Valid: valid}
		}
		return results, nil
	}
	releaseStaleHandler := make(chan struct{})
	handlerStarted := make(chan struct{})
	unexpectedSecondHandler := make(chan struct{})
	var handlerCalls atomic.Int32
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.Result) error {
		return nil
	}
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.test": func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			if handlerCalls.Add(1) > 1 {
				close(unexpectedSecondHandler)
				return domain.Result{}, errors.New("stale buffered attempt executed")
			}
			close(handlerStarted)
			<-ctx.Done()
			<-releaseStaleHandler
			return domain.Result{}, ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, handlerStarted)
	awaitSignal(t, staleCanceled)
	awaitSignal(t, bufferedHeartbeated)
	awaitSignal(t, replacementAcquisition)
	close(releaseStaleHandler)
	select {
	case <-unexpectedSecondHandler:
		t.Fatal("worker executed a buffered attempt after its lease became stale")
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRetriesTransientHeartbeatOnNextInterval(t *testing.T) {
	t.Parallel()

	job := makeJobs(t, 1, "demo.test")[0]
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{job}, nil
		}
		return nil, nil
	}
	secondHeartbeat := make(chan struct{})
	var calls atomic.Int32
	var secondOnce sync.Once
	dispatcher.heartbeat = func(
		_ context.Context,
		_ domain.WorkerID,
		attempts []HeartbeatAttempt,
	) ([]HeartbeatResult, error) {
		if len(attempts) == 0 {
			return []HeartbeatResult{}, nil
		}
		if calls.Add(1) == 1 {
			return nil, status.Error(codes.Unavailable, "temporary")
		}
		secondOnce.Do(func() { close(secondHeartbeat) })
		return []HeartbeatResult{{Attempt: attempts[0], Valid: true}}, nil
	}
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.Result) error {
		return nil
	}
	handlerStarted := make(chan struct{})
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.test": func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			close(handlerStarted)
			<-ctx.Done()
			return domain.Result{}, ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, handlerStarted)
	awaitSignal(t, secondHeartbeat)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatalf("non-empty heartbeat calls = %d, want at least 2", calls.Load())
	}
}

func TestWorkerTreatsFailedPreconditionReportAsLostAttempt(t *testing.T) {
	t.Parallel()

	jobs := makeJobs(t, 2, "demo.test")
	var acquireCalls atomic.Int32
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		call := acquireCalls.Add(1)
		if call <= 2 {
			return []Job{jobs[call-1]}, nil
		}
		return nil, nil
	}
	secondReported := make(chan struct{})
	dispatcher.report = func(
		_ context.Context,
		_ domain.WorkerID,
		jobID domain.JobID,
		_ domain.AttemptNumber,
		_ domain.Result,
	) error {
		if jobID == jobs[0].ID {
			return status.Error(codes.FailedPrecondition, "stale")
		}
		close(secondReported)
		return nil
	}
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.test": func(context.Context, domain.Payload) (domain.Result, error) {
			return mustResult(t, `{"ok":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, secondReported)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
}

func newTestWorker(
	t *testing.T,
	dispatcher Dispatcher,
	concurrency uint32,
	handlers map[string]Handler,
) *Worker {
	t.Helper()
	return newTestWorkerWithID(t, dispatcher, concurrency, domain.NewWorkerID(), handlers)
}

func newTestWorkerWithID(
	t *testing.T,
	dispatcher Dispatcher,
	concurrency uint32,
	workerID domain.WorkerID,
	handlers map[string]Handler,
) *Worker {
	t.Helper()
	runtime, err := New(dispatcher, handlers, Config{
		Registration: Registration{
			WorkerID:    workerID,
			Hostname:    "test-host",
			Version:     "test",
			Concurrency: concurrency,
			StartedAt:   time.Now(),
		},
		IdleBackoffMin:    time.Millisecond,
		IdleBackoffMax:    2 * time.Millisecond,
		ReportBackoffMin:  time.Millisecond,
		ReportBackoffMax:  2 * time.Millisecond,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func makeJobs(t *testing.T, count int, rawType string) []Job {
	t.Helper()
	jobType, err := domain.ParseJobType(rawType)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := domain.ParsePayload([]byte(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]Job, count)
	for i := range jobs {
		attempt, err := domain.NewAttemptNumber(1)
		if err != nil {
			t.Fatal(err)
		}
		jobs[i] = Job{
			ID:            domain.NewJobID(),
			AttemptNumber: attempt,
			Type:          jobType,
			Payload:       payload,
			Timeout:       time.Second,
		}
	}
	return jobs
}

func mustResult(t *testing.T, raw string) domain.Result {
	t.Helper()
	result, err := domain.ParseResult([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func runWorker(worker *Worker, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx)
	}()
	return done
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func awaitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for worker")
		return nil
	}
}

func awaitHeartbeatWithoutAttempts(t *testing.T, heartbeats <-chan []HeartbeatAttempt) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case attempts := <-heartbeats:
			if len(attempts) == 0 {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for empty heartbeat")
		}
	}
}
