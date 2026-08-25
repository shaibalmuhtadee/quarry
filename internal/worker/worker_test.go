package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	report    func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.AttemptOutcome) error
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

func (dispatcher *fakeDispatcher) ReportAttempt(
	ctx context.Context,
	workerID domain.WorkerID,
	jobID domain.JobID,
	attemptNumber domain.AttemptNumber,
	outcome domain.AttemptOutcome,
) error {
	return dispatcher.report(ctx, workerID, jobID, attemptNumber, outcome)
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
		domain.AttemptOutcome,
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
		_ domain.AttemptOutcome,
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
		outcome domain.AttemptOutcome,
	) error {
		result, ok := outcome.Result()
		if !ok {
			t.Fatalf("reported outcome = %q, want succeeded", outcome.Kind())
		}
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

func TestWorkerReportsHandlerFailuresAndContinues(t *testing.T) {
	t.Parallel()

	retryableJob := makeJobs(t, 1, "demo.retryable")[0]
	permanentJob := makeJobs(t, 1, "demo.permanent")[0]
	unknownJob := makeJobs(t, 1, "demo.unknown")[0]
	successJob := makeJobs(t, 1, "demo.success")[0]
	jobs := []Job{retryableJob, permanentJob, unknownJob, successJob}

	retryableErr, err := NewRetryableHandlerError("dependency_timeout", "dependency timed out", errors.New("dial tcp secret-host"))
	if err != nil {
		t.Fatal(err)
	}
	permanentErr, err := NewPermanentHandlerError("invalid_input", "input is invalid", errors.New("unsafe parser detail"))
	if err != nil {
		t.Fatal(err)
	}

	var next atomic.Int32
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		index := int(next.Add(1)) - 1
		if index < len(jobs) {
			return []Job{jobs[index]}, nil
		}
		return nil, nil
	}
	type reportCall struct {
		jobID   domain.JobID
		attempt domain.AttemptNumber
		outcome domain.AttemptOutcome
	}
	var reportMu sync.Mutex
	var reports []reportCall
	acknowledged := make(chan struct{}, len(jobs))
	dispatcher.report = func(
		_ context.Context,
		_ domain.WorkerID,
		jobID domain.JobID,
		attempt domain.AttemptNumber,
		outcome domain.AttemptOutcome,
	) error {
		reportMu.Lock()
		reports = append(reports, reportCall{jobID: jobID, attempt: attempt, outcome: outcome})
		jobReports := 0
		for _, call := range reports {
			if call.jobID == jobID {
				jobReports++
			}
		}
		reportMu.Unlock()
		if jobID == retryableJob.ID && jobReports == 1 {
			return status.Error(codes.Unavailable, "temporary")
		}
		acknowledged <- struct{}{}
		return nil
	}
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.retryable": func(context.Context, domain.Payload) (domain.Result, error) {
			return domain.Result{}, fmt.Errorf("retryable wrapper: %w", retryableErr)
		},
		"demo.permanent": func(context.Context, domain.Payload) (domain.Result, error) {
			return domain.Result{}, permanentErr
		},
		"demo.unknown": func(context.Context, domain.Payload) (domain.Result, error) {
			return domain.Result{}, errors.New("database password secret")
		},
		"demo.success": func(context.Context, domain.Payload) (domain.Result, error) {
			return mustResult(t, `{"ok":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	for range jobs {
		awaitSignal(t, acknowledged)
	}
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatalf("worker stopped after handler failure: %v", err)
	}

	reportMu.Lock()
	defer reportMu.Unlock()
	if len(reports) != 5 {
		t.Fatalf("report calls = %d, want 5", len(reports))
	}
	wantKinds := []domain.AttemptOutcomeKind{
		domain.AttemptOutcomeKindRetryableFailure,
		domain.AttemptOutcomeKindRetryableFailure,
		domain.AttemptOutcomeKindPermanentFailure,
		domain.AttemptOutcomeKindPermanentFailure,
		domain.AttemptOutcomeKindSucceeded,
	}
	for i, report := range reports {
		if report.outcome.Kind() != wantKinds[i] {
			t.Fatalf("report %d kind = %q, want %q", i, report.outcome.Kind(), wantKinds[i])
		}
	}
	firstFailure, firstOK := reports[0].outcome.Failure()
	secondFailure, secondOK := reports[1].outcome.Failure()
	if reports[0].jobID != retryableJob.ID || reports[1].jobID != retryableJob.ID ||
		reports[0].attempt != reports[1].attempt || reports[0].outcome.Kind() != reports[1].outcome.Kind() ||
		!firstOK || !secondOK || firstFailure.Code() != secondFailure.Code() || firstFailure.Message() != secondFailure.Message() {
		t.Fatalf("retried failure reports changed identity or outcome: %#v", reports[:2])
	}
	unknownFailure, ok := reports[3].outcome.Failure()
	if !ok || unknownFailure.Code() != unclassifiedHandlerErrorCode || unknownFailure.Message() != unclassifiedHandlerErrorMessage {
		t.Fatalf("unclassified report = %#v", reports[3].outcome)
	}
}

func TestWorkerHandlerFailureDoesNotCancelConcurrentAttempt(t *testing.T) {
	t.Parallel()

	failedJob := makeJobs(t, 1, "demo.failed")[0]
	successJob := makeJobs(t, 1, "demo.concurrent-success")[0]
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{failedJob, successJob}, nil
		}
		return nil, nil
	}
	failureReported := make(chan struct{})
	successReported := make(chan struct{})
	dispatcher.report = func(
		_ context.Context,
		_ domain.WorkerID,
		jobID domain.JobID,
		_ domain.AttemptNumber,
		outcome domain.AttemptOutcome,
	) error {
		switch jobID {
		case failedJob.ID:
			if outcome.Kind() != domain.AttemptOutcomeKindPermanentFailure {
				t.Errorf("failed job outcome = %q", outcome.Kind())
			}
			close(failureReported)
		case successJob.ID:
			if outcome.Kind() != domain.AttemptOutcomeKindSucceeded {
				t.Errorf("successful job outcome = %q", outcome.Kind())
			}
			close(successReported)
		}
		return nil
	}
	concurrentStarted := make(chan struct{})
	releaseConcurrent := make(chan struct{})
	permanentErr, err := NewPermanentHandlerError("invalid_input", "input is invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newTestWorker(t, dispatcher, 2, map[string]Handler{
		"demo.failed": func(context.Context, domain.Payload) (domain.Result, error) {
			<-concurrentStarted
			return domain.Result{}, permanentErr
		},
		"demo.concurrent-success": func(context.Context, domain.Payload) (domain.Result, error) {
			close(concurrentStarted)
			<-releaseConcurrent
			return mustResult(t, `{"ok":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, failureReported)
	select {
	case err := <-done:
		t.Fatalf("worker stopped while unrelated attempt was active: %v", err)
	default:
	}
	close(releaseConcurrent)
	awaitSignal(t, successReported)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
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
	if !active.signal(first.identity, errCancellationRequested) {
		t.Fatal("active attempt did not receive cancellation signal")
	}
	if !errors.Is(context.Cause(first.handlerCtx), errCancellationRequested) || context.Cause(first.ctx) != nil || active.len() != 2 {
		t.Fatalf("cancellation signal changed attempt lifetime: handler cause %v, attempt cause %v, active %d", context.Cause(first.handlerCtx), context.Cause(first.ctx), active.len())
	}
	select {
	case <-second.handlerCtx.Done():
		t.Fatalf("unrelated handler was canceled: %v", context.Cause(second.handlerCtx))
	default:
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

func TestWorkerCancelsOnlyMatchingHandlerAndRetriesCancelledReport(t *testing.T) {
	t.Parallel()

	jobs := makeJobs(t, 2, "demo.cancel")
	keepType, err := domain.ParseJobType("demo.keep")
	if err != nil {
		t.Fatal(err)
	}
	jobs[1].Type = keepType
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return jobs, nil
		}
		return nil, nil
	}

	cancelHandlerStarted := make(chan struct{})
	cancelHandlerStopped := make(chan error, 1)
	keepHandlerStarted := make(chan struct{})
	releaseKeepHandler := make(chan struct{})
	keepHandlerCancelled := make(chan error, 1)
	handlers := map[string]Handler{
		"demo.cancel": func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			close(cancelHandlerStarted)
			<-ctx.Done()
			cancelHandlerStopped <- context.Cause(ctx)
			return domain.Result{}, ctx.Err()
		},
		"demo.keep": func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			close(keepHandlerStarted)
			select {
			case <-ctx.Done():
				keepHandlerCancelled <- context.Cause(ctx)
				return domain.Result{}, ctx.Err()
			case <-releaseKeepHandler:
				return mustResult(t, `{"ok":true}`), nil
			}
		},
	}

	cancellationSent := make(chan struct{})
	reportingHeartbeat := make(chan struct{})
	var cancellationOnce, reportingHeartbeatOnce sync.Once
	var reportCalls atomic.Int32
	dispatcher.heartbeat = func(
		_ context.Context,
		_ domain.WorkerID,
		attempts []HeartbeatAttempt,
	) ([]HeartbeatResult, error) {
		results := make([]HeartbeatResult, len(attempts))
		for i, attempt := range attempts {
			requestCancellation := attempt.JobID == jobs[0].ID
			results[i] = HeartbeatResult{Attempt: attempt, Valid: true, CancelRequested: requestCancellation}
			if requestCancellation {
				cancellationOnce.Do(func() { close(cancellationSent) })
				if reportCalls.Load() > 0 {
					reportingHeartbeatOnce.Do(func() { close(reportingHeartbeat) })
				}
			}
		}
		return results, nil
	}
	cancelledReported := make(chan struct{})
	keepReported := make(chan struct{})
	var cancelledReportedOnce, keepReportedOnce sync.Once
	dispatcher.report = func(
		ctx context.Context,
		_ domain.WorkerID,
		jobID domain.JobID,
		_ domain.AttemptNumber,
		outcome domain.AttemptOutcome,
	) error {
		if jobID == jobs[0].ID {
			if ctx.Err() != nil {
				t.Errorf("cancelled report used a cancelled attempt context: %v", ctx.Err())
			}
			assertExecutionFailure(t, outcome, domain.AttemptOutcomeKindCancelled, cancellationRequestedCode, cancellationRequestedMessage)
			if reportCalls.Add(1) == 1 {
				return status.Error(codes.Unavailable, "temporary")
			}
			select {
			case <-reportingHeartbeat:
			case <-time.After(3 * time.Second):
				return errors.New("cancelled attempt was not heartbeated while its report retried")
			}
			cancelledReportedOnce.Do(func() { close(cancelledReported) })
			return nil
		}
		if outcome.Kind() != domain.AttemptOutcomeKindSucceeded {
			t.Errorf("unrelated outcome = %q, want succeeded", outcome.Kind())
		}
		keepReportedOnce.Do(func() { close(keepReported) })
		return nil
	}

	runtime := newTestWorker(t, dispatcher, 2, handlers)
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, cancelHandlerStarted)
	awaitSignal(t, keepHandlerStarted)
	awaitSignal(t, cancellationSent)
	if cause := <-cancelHandlerStopped; !errors.Is(cause, errCancellationRequested) {
		t.Fatalf("cancelled handler cause = %v", cause)
	}
	awaitSignal(t, reportingHeartbeat)
	awaitSignal(t, cancelledReported)
	select {
	case cause := <-keepHandlerCancelled:
		t.Fatalf("unrelated handler was cancelled: %v", cause)
	default:
	}
	close(releaseKeepHandler)
	awaitSignal(t, keepReported)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	if reportCalls.Load() < 2 {
		t.Fatalf("cancelled report calls = %d, want at least 2", reportCalls.Load())
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
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.AttemptOutcome) error {
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
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.AttemptOutcome) error {
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
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.AttemptOutcome) error {
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
		_ domain.AttemptOutcome,
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

func TestWorkerDrainsActiveAttemptWithoutFurtherAcquisition(t *testing.T) {
	job := makeJobs(t, 1, "demo.test")[0]
	secondAcquireStarted := make(chan struct{})
	var acquireCalls atomic.Int32
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(ctx context.Context, _ domain.WorkerID, _ uint32, _ []domain.JobType) ([]Job, error) {
		switch acquireCalls.Add(1) {
		case 1:
			return []Job{job}, nil
		case 2:
			close(secondAcquireStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			t.Errorf("acquisition continued after shutdown signal")
			return nil, nil
		}
	}
	heartbeatedDuringDrain := make(chan struct{}, 1)
	dispatcher.heartbeat = func(
		_ context.Context,
		_ domain.WorkerID,
		attempts []HeartbeatAttempt,
	) ([]HeartbeatResult, error) {
		if len(attempts) == 1 {
			select {
			case heartbeatedDuringDrain <- struct{}{}:
			default:
			}
		}
		results := make([]HeartbeatResult, len(attempts))
		for i, attempt := range attempts {
			results[i] = HeartbeatResult{Attempt: attempt, Valid: true}
		}
		return results, nil
	}
	reported := make(chan struct{})
	dispatcher.report = func(
		context.Context,
		domain.WorkerID,
		domain.JobID,
		domain.AttemptNumber,
		domain.AttemptOutcome,
	) error {
		close(reported)
		return nil
	}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	runtime := newTestWorker(t, dispatcher, 2, map[string]Handler{
		"demo.test": func(context.Context, domain.Payload) (domain.Result, error) {
			close(handlerStarted)
			<-releaseHandler
			return mustResult(t, `{"ok":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, handlerStarted)
	awaitSignal(t, secondAcquireStarted)
	cancel()
	awaitSignal(t, heartbeatedDuringDrain)
	close(releaseHandler)
	awaitSignal(t, reported)
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	if calls := acquireCalls.Load(); calls != 2 {
		t.Fatalf("acquisition calls = %d, want 2", calls)
	}
}

func TestWorkerShutdownDeadlineCancelsAttemptWithoutReporting(t *testing.T) {
	job := makeJobs(t, 1, "demo.test")[0]
	var acquired atomic.Bool
	var reports atomic.Int32
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{job}, nil
		}
		return nil, nil
	}
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.AttemptOutcome) error {
		reports.Add(1)
		return nil
	}
	handlerStarted := make(chan struct{})
	shutdownCause := make(chan error, 1)
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.test": func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			close(handlerStarted)
			<-ctx.Done()
			shutdownCause <- context.Cause(ctx)
			return domain.Result{}, ctx.Err()
		},
	})
	runtime.shutdownTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, handlerStarted)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	select {
	case cause := <-shutdownCause:
		if !errors.Is(cause, errWorkerShutdown) {
			t.Fatalf("handler cancellation cause = %v, want worker shutdown", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler context was not cancelled at shutdown deadline")
	}
	if got := reports.Load(); got != 0 {
		t.Fatalf("attempt reports = %d, want 0", got)
	}
}

func TestWorkerShutdownDeadlineDoesNotWaitForHandlerIgnoringContext(t *testing.T) {
	job := makeJobs(t, 1, "demo.test")[0]
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{job}, nil
		}
		return nil, nil
	}
	dispatcher.report = func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.AttemptOutcome) error {
		t.Error("worker reported an unfinished attempt during forced shutdown")
		return nil
	}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.test": func(context.Context, domain.Payload) (domain.Result, error) {
			close(handlerStarted)
			<-releaseHandler
			return mustResult(t, `{"ok":true}`), nil
		},
	})
	runtime.shutdownTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	awaitSignal(t, handlerStarted)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	close(releaseHandler)
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
	return newTestWorkerWithIDAndLogger(t, dispatcher, concurrency, workerID, nil, handlers)
}

func newTestWorkerWithLogger(
	t *testing.T,
	dispatcher Dispatcher,
	concurrency uint32,
	logger *slog.Logger,
	handlers map[string]Handler,
) *Worker {
	t.Helper()
	return newTestWorkerWithIDAndLogger(t, dispatcher, concurrency, domain.NewWorkerID(), logger, handlers)
}

func newTestWorkerWithIDAndLogger(
	t *testing.T,
	dispatcher Dispatcher,
	concurrency uint32,
	workerID domain.WorkerID,
	logger *slog.Logger,
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
		ShutdownTimeout:   time.Second,
		Logger:            logger,
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
