package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestWorkerAppliesSubmittedTimeoutAndOverridesHandlerError(t *testing.T) {
	t.Parallel()

	job := makeJobs(t, 1, "demo.timeout")[0]
	job.Timeout = 30 * time.Millisecond
	observedDeadline := make(chan time.Duration, 1)
	observedCause := make(chan error, 1)
	outcome := runSingleJob(t, job, nil, func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("handler context has no deadline")
		} else {
			observedDeadline <- time.Until(deadline)
		}
		<-ctx.Done()
		observedCause <- context.Cause(ctx)
		return domain.Result{}, errors.New("handler returned an internal error after cancellation")
	})

	remaining := <-observedDeadline
	if remaining <= 0 || remaining > job.Timeout {
		t.Fatalf("handler deadline remaining = %s, want within (0, %s]", remaining, job.Timeout)
	}
	if cause := <-observedCause; !errors.Is(cause, errExecutionTimedOut) {
		t.Fatalf("handler context cause = %v, want execution timeout", cause)
	}
	assertExecutionFailure(t, outcome, domain.AttemptOutcomeKindTimedOut, executionTimeoutCode, executionTimeoutMessage)
}

func TestWorkerTimeoutBoundaryUsesFirstCancellationCause(t *testing.T) {
	t.Parallel()

	t.Run("handler completion wins", func(t *testing.T) {
		job := makeJobs(t, 1, "demo.success")[0]
		job.Timeout = time.Second
		outcome := runSingleJob(t, job, nil, func(context.Context, domain.Payload) (domain.Result, error) {
			return mustResult(t, `{"ok":true}`), nil
		})
		if outcome.Kind() != domain.AttemptOutcomeKindSucceeded {
			t.Fatalf("outcome = %q, want succeeded", outcome.Kind())
		}
	})

	t.Run("deadline wins over returned success", func(t *testing.T) {
		job := makeJobs(t, 1, "demo.timeout")[0]
		job.Timeout = 10 * time.Millisecond
		outcome := runSingleJob(t, job, nil, func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			<-ctx.Done()
			return mustResult(t, `{"too_late":true}`), nil
		})
		assertExecutionFailure(t, outcome, domain.AttemptOutcomeKindTimedOut, executionTimeoutCode, executionTimeoutMessage)
	})

	t.Run("deadline wins over later panic", func(t *testing.T) {
		job := makeJobs(t, 1, "demo.timeout-panic")[0]
		job.Timeout = 10 * time.Millisecond
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		outcome := runSingleJob(t, job, logger, func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			<-ctx.Done()
			panic("panic after deadline")
		})
		assertExecutionFailure(t, outcome, domain.AttemptOutcomeKindTimedOut, executionTimeoutCode, executionTimeoutMessage)
	})
}

func TestWorkerRecoversPanicLogsStackAndContinues(t *testing.T) {
	t.Parallel()

	panicJob := makeJobs(t, 1, "demo.panic")[0]
	successJob := makeJobs(t, 1, "demo.success")[0]
	jobs := []Job{panicJob, successJob}
	var next atomic.Int32
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		index := int(next.Add(1)) - 1
		if index < len(jobs) {
			return []Job{jobs[index]}, nil
		}
		return nil, nil
	}
	reports := make(chan domain.AttemptOutcome, len(jobs))
	dispatcher.report = func(
		_ context.Context,
		_ domain.WorkerID,
		_ domain.JobID,
		_ domain.AttemptNumber,
		outcome domain.AttemptOutcome,
	) error {
		reports <- outcome
		return nil
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	runtime := newTestWorkerWithLogger(t, dispatcher, 1, logger, map[string]Handler{
		"demo.panic": func(context.Context, domain.Payload) (domain.Result, error) {
			panic("secret panic value")
		},
		"demo.success": func(context.Context, domain.Payload) (domain.Result, error) {
			return mustResult(t, `{"ok":true}`), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runWorker(runtime, ctx)
	panicOutcome := awaitOutcome(t, reports)
	successOutcome := awaitOutcome(t, reports)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatalf("worker stopped after handler panic: %v", err)
	}

	assertExecutionFailure(t, panicOutcome, domain.AttemptOutcomeKindPanicked, handlerPanickedCode, handlerPanickedMessage)
	if successOutcome.Kind() != domain.AttemptOutcomeKindSucceeded {
		t.Fatalf("post-panic outcome = %q, want succeeded", successOutcome.Kind())
	}
	output := logs.String()
	if !strings.Contains(output, "secret panic value") || !strings.Contains(output, "runtime/debug.Stack") ||
		!strings.Contains(output, panicJob.ID.String()) || !strings.Contains(output, "demo.panic") {
		t.Fatalf("panic log does not contain value, stack, and attempt identity: %s", output)
	}
	failure, _ := panicOutcome.Failure()
	if strings.Contains(failure.Code(), "secret") || strings.Contains(failure.Message(), "secret") {
		t.Fatalf("persisted panic failure exposed panic value: %#v", failure)
	}
}

func TestWorkerCannotReleaseSlotUntilContextIgnoringHandlerReturns(t *testing.T) {
	t.Parallel()

	job := makeJobs(t, 1, "demo.ignores-context")[0]
	job.Timeout = 10 * time.Millisecond
	deadlineObserved := make(chan struct{})
	releaseHandler := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHandler) }) }
	t.Cleanup(release)
	reported := make(chan domain.AttemptOutcome, 1)
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{job}, nil
		}
		return nil, nil
	}
	dispatcher.report = func(_ context.Context, _ domain.WorkerID, _ domain.JobID, _ domain.AttemptNumber, outcome domain.AttemptOutcome) error {
		reported <- outcome
		return nil
	}
	runtime := newTestWorker(t, dispatcher, 1, map[string]Handler{
		"demo.ignores-context": func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
			<-ctx.Done()
			close(deadlineObserved)
			<-releaseHandler
			return domain.Result{}, ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runWorker(runtime, ctx)
	awaitSignal(t, deadlineObserved)
	select {
	case outcome := <-reported:
		t.Fatalf("worker reported before handler returned: %q", outcome.Kind())
	case err := <-done:
		t.Fatalf("worker stopped while handler ignored cancellation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	outcome := awaitOutcome(t, reported)
	assertExecutionFailure(t, outcome, domain.AttemptOutcomeKindTimedOut, executionTimeoutCode, executionTimeoutMessage)
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
}

func runSingleJob(t *testing.T, job Job, logger *slog.Logger, handler Handler) domain.AttemptOutcome {
	t.Helper()
	var acquired atomic.Bool
	dispatcher := &fakeDispatcher{}
	dispatcher.acquire = func(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error) {
		if acquired.CompareAndSwap(false, true) {
			return []Job{job}, nil
		}
		return nil, nil
	}
	reported := make(chan domain.AttemptOutcome, 1)
	dispatcher.report = func(_ context.Context, _ domain.WorkerID, _ domain.JobID, _ domain.AttemptNumber, outcome domain.AttemptOutcome) error {
		reported <- outcome
		return nil
	}
	runtime := newTestWorkerWithLogger(t, dispatcher, 1, logger, map[string]Handler{job.Type.String(): handler})
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(runtime, ctx)
	var outcome domain.AttemptOutcome
	select {
	case outcome = <-reported:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for attempt report")
	}
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Fatal(err)
	}
	return outcome
}

func assertExecutionFailure(
	t *testing.T,
	outcome domain.AttemptOutcome,
	wantKind domain.AttemptOutcomeKind,
	wantCode string,
	wantMessage string,
) {
	t.Helper()
	failure, ok := outcome.Failure()
	if outcome.Kind() != wantKind || !ok || failure.Code() != wantCode || failure.Message() != wantMessage {
		t.Fatalf("outcome = %#v, failure = %#v, want %q (%q, %q)", outcome, failure, wantKind, wantCode, wantMessage)
	}
}

func awaitOutcome(t *testing.T, outcomes <-chan domain.AttemptOutcome) domain.AttemptOutcome {
	t.Helper()
	select {
	case outcome := <-outcomes:
		return outcome
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for attempt outcome")
		return domain.AttemptOutcome{}
	}
}
