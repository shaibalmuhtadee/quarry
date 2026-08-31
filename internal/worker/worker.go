package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrInvalidConfiguration  = errors.New("invalid worker configuration")
	errLeaseStale            = errors.New("attempt lease is stale")
	errAttemptLost           = errors.New("attempt report lost its lease race")
	errExecutionTimedOut     = errors.New("attempt execution timed out")
	errCancellationRequested = errors.New("job cancellation was requested")
	errWorkerShutdown        = errors.New("worker shutdown deadline reached")
)

type Handler func(context.Context, domain.Payload) (domain.Result, error)

type Registration struct {
	WorkerID    domain.WorkerID
	Hostname    string
	Version     string
	Concurrency uint32
	StartedAt   time.Time
}

type Job struct {
	ID            domain.JobID
	AttemptNumber domain.AttemptNumber
	Type          domain.JobType
	Payload       domain.Payload
	Timeout       time.Duration
	TraceParent   string
}

type HeartbeatAttempt struct {
	JobID         domain.JobID
	AttemptNumber domain.AttemptNumber
}

type HeartbeatResult struct {
	Attempt         HeartbeatAttempt
	Valid           bool
	CancelRequested bool
}

type Dispatcher interface {
	Register(context.Context, Registration) error
	Acquire(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error)
	Heartbeat(context.Context, domain.WorkerID, []HeartbeatAttempt) ([]HeartbeatResult, error)
	ReportAttempt(
		context.Context,
		domain.WorkerID,
		domain.JobID,
		domain.AttemptNumber,
		domain.AttemptOutcome,
	) error
}

type Config struct {
	Registration            Registration
	IdleBackoffMin          time.Duration
	IdleBackoffMax          time.Duration
	ReportBackoffMin        time.Duration
	ReportBackoffMax        time.Duration
	HeartbeatInterval       time.Duration
	ShutdownTimeout         time.Duration
	TestAfterHandlerSuccess func(Job) error
	Logger                  *slog.Logger
	Metrics                 workerMetrics
	Tracer                  trace.Tracer
}

type workerMetrics interface {
	JobExecutionCompleted(domain.JobType, domain.AttemptOutcomeKind, time.Duration)
	WorkerPollError(string)
}

type Worker struct {
	dispatcher                  Dispatcher
	registration                Registration
	handlers                    map[string]Handler
	supportedTypes              []domain.JobType
	idleBackoffMin              time.Duration
	idleBackoffMax              time.Duration
	reportBackoffMin            time.Duration
	reportBackoffMax            time.Duration
	heartbeatInterval           time.Duration
	shutdownTimeout             time.Duration
	testAfterHandlerSuccess     func(Job) error
	testAfterHandlerSuccessOnce sync.Once
	logger                      *slog.Logger
	metrics                     workerMetrics
	tracer                      trace.Tracer
}

func New(dispatcher Dispatcher, handlers map[string]Handler, cfg Config) (*Worker, error) {
	if dispatcher == nil {
		return nil, fmt.Errorf("%w: dispatcher is required", ErrInvalidConfiguration)
	}
	if cfg.Registration.WorkerID.IsZero() {
		return nil, fmt.Errorf("%w: worker ID is required", ErrInvalidConfiguration)
	}
	if cfg.Registration.Hostname == "" || cfg.Registration.Version == "" {
		return nil, fmt.Errorf("%w: hostname and version are required", ErrInvalidConfiguration)
	}
	if cfg.Registration.Concurrency == 0 {
		return nil, fmt.Errorf("%w: concurrency must be positive", ErrInvalidConfiguration)
	}
	if cfg.Registration.StartedAt.IsZero() {
		return nil, fmt.Errorf("%w: start time is required", ErrInvalidConfiguration)
	}
	if len(handlers) == 0 {
		return nil, fmt.Errorf("%w: at least one handler is required", ErrInvalidConfiguration)
	}
	if err := validateBackoff(cfg.IdleBackoffMin, cfg.IdleBackoffMax); err != nil {
		return nil, fmt.Errorf("%w: idle backoff: %v", ErrInvalidConfiguration, err)
	}
	if err := validateBackoff(cfg.ReportBackoffMin, cfg.ReportBackoffMax); err != nil {
		return nil, fmt.Errorf("%w: report backoff: %v", ErrInvalidConfiguration, err)
	}
	if cfg.HeartbeatInterval <= 0 {
		return nil, fmt.Errorf("%w: heartbeat interval must be positive", ErrInvalidConfiguration)
	}
	if cfg.ShutdownTimeout <= 0 {
		return nil, fmt.Errorf("%w: shutdown timeout must be positive", ErrInvalidConfiguration)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	tracer := cfg.Tracer
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("quarry/worker")
	}

	handlerCopy := make(map[string]Handler, len(handlers))
	supportedTypes := make([]domain.JobType, 0, len(handlers))
	for rawType, handler := range handlers {
		jobType, err := domain.ParseJobType(rawType)
		if err != nil {
			return nil, fmt.Errorf("%w: handler type %q: %v", ErrInvalidConfiguration, rawType, err)
		}
		if handler == nil {
			return nil, fmt.Errorf("%w: handler for %q is required", ErrInvalidConfiguration, rawType)
		}
		handlerCopy[rawType] = handler
		supportedTypes = append(supportedTypes, jobType)
	}
	sort.Slice(supportedTypes, func(i, j int) bool {
		return supportedTypes[i].String() < supportedTypes[j].String()
	})

	return &Worker{
		dispatcher:              dispatcher,
		registration:            cfg.Registration,
		handlers:                handlerCopy,
		supportedTypes:          supportedTypes,
		idleBackoffMin:          cfg.IdleBackoffMin,
		idleBackoffMax:          cfg.IdleBackoffMax,
		reportBackoffMin:        cfg.ReportBackoffMin,
		reportBackoffMax:        cfg.ReportBackoffMax,
		heartbeatInterval:       cfg.HeartbeatInterval,
		shutdownTimeout:         cfg.ShutdownTimeout,
		testAfterHandlerSuccess: cfg.TestAfterHandlerSuccess,
		logger:                  logger,
		metrics:                 cfg.Metrics,
		tracer:                  tracer,
	}, nil
}

type activeAttempt struct {
	identity      HeartbeatAttempt
	job           Job
	ctx           context.Context
	cancel        context.CancelCauseFunc
	handlerCtx    context.Context
	cancelHandler context.CancelCauseFunc
}

type activeAttempts struct {
	mu       sync.Mutex
	attempts map[HeartbeatAttempt]*activeAttempt
}

func newActiveAttempts() *activeAttempts {
	return &activeAttempts{attempts: make(map[HeartbeatAttempt]*activeAttempt)}
}

func (active *activeAttempts) add(parent context.Context, job Job) (*activeAttempt, error) {
	identity := HeartbeatAttempt{JobID: job.ID, AttemptNumber: job.AttemptNumber}
	ctx, cancel := context.WithCancelCause(parent)
	handlerCtx, cancelHandler := context.WithCancelCause(ctx)
	attempt := &activeAttempt{
		identity:      identity,
		job:           job,
		ctx:           ctx,
		cancel:        cancel,
		handlerCtx:    handlerCtx,
		cancelHandler: cancelHandler,
	}

	active.mu.Lock()
	defer active.mu.Unlock()
	if _, exists := active.attempts[identity]; exists {
		cancel(errors.New("duplicate active attempt"))
		return nil, errors.New("dispatcher returned a duplicate active attempt")
	}
	active.attempts[identity] = attempt
	return attempt, nil
}

func (active *activeAttempts) len() int {
	active.mu.Lock()
	defer active.mu.Unlock()
	return len(active.attempts)
}

func (active *activeAttempts) snapshot() []HeartbeatAttempt {
	active.mu.Lock()
	defer active.mu.Unlock()
	attempts := make([]HeartbeatAttempt, 0, len(active.attempts))
	for identity := range active.attempts {
		attempts = append(attempts, identity)
	}
	return attempts
}

func (active *activeAttempts) remove(attempt *activeAttempt, cause error) bool {
	active.mu.Lock()
	current, exists := active.attempts[attempt.identity]
	if exists && current == attempt {
		delete(active.attempts, attempt.identity)
	}
	active.mu.Unlock()
	if !exists || current != attempt {
		return false
	}
	attempt.cancel(cause)
	return true
}

func (active *activeAttempts) cancel(identity HeartbeatAttempt, cause error) bool {
	active.mu.Lock()
	attempt, exists := active.attempts[identity]
	if exists {
		delete(active.attempts, identity)
	}
	active.mu.Unlock()
	if !exists {
		return false
	}
	attempt.cancel(cause)
	return true
}

func (active *activeAttempts) signal(identity HeartbeatAttempt, cause error) bool {
	active.mu.Lock()
	attempt, exists := active.attempts[identity]
	active.mu.Unlock()
	if !exists {
		return false
	}
	attempt.cancelHandler(cause)
	return true
}

func (worker *Worker) Run(ctx context.Context) error {
	if err := worker.dispatcher.Register(ctx, worker.registration); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("register worker: %w", err)
	}

	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))

	jobs := make(chan *activeAttempt, worker.registration.Concurrency)
	capacityChanged := make(chan struct{}, 1)
	runtimeErrors := make(chan error, 1)
	active := newActiveAttempts()
	var failOnce sync.Once
	fail := func(err error) {
		failOnce.Do(func() {
			runtimeErrors <- err
			cancel(err)
		})
	}
	notifyCapacityChanged := func() {
		select {
		case capacityChanged <- struct{}{}:
		default:
		}
	}

	var goroutines sync.WaitGroup
	for range worker.registration.Concurrency {
		goroutines.Add(1)
		go func() {
			defer goroutines.Done()
			worker.execute(runCtx, jobs, active, notifyCapacityChanged, fail)
		}()
	}
	goroutines.Add(1)
	go func() {
		defer goroutines.Done()
		if err := worker.heartbeatLoop(runCtx, active, notifyCapacityChanged); err != nil {
			fail(err)
		}
	}()
	waitForGoroutines := true
	defer func() {
		cancel(context.Canceled)
		if waitForGoroutines {
			goroutines.Wait()
		}
	}()
	drain := func() error {
		if active.len() == 0 {
			return nil
		}
		timer := time.NewTimer(worker.shutdownTimeout)
		defer timer.Stop()
		for {
			select {
			case err := <-runtimeErrors:
				return err
			case <-capacityChanged:
				if active.len() == 0 {
					return nil
				}
			case <-timer.C:
				waitForGoroutines = false
				cancel(errWorkerShutdown)
				return nil
			}
		}
	}

	idleBackoff := worker.idleBackoffMin
	for {
		if err := receiveRuntimeError(runtimeErrors); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return drain()
		}

		outstanding := uint32(active.len())
		if outstanding >= worker.registration.Concurrency {
			select {
			case <-ctx.Done():
				return drain()
			case err := <-runtimeErrors:
				return err
			case <-capacityChanged:
			}
			continue
		}

		capacity := worker.registration.Concurrency - outstanding
		acquired, err := worker.dispatcher.Acquire(
			ctx,
			worker.registration.WorkerID,
			capacity,
			worker.supportedTypes,
		)
		if err != nil {
			if worker.metrics != nil {
				worker.metrics.WorkerPollError(strings.ToLower(status.Code(err).String()))
			}
			if ctx.Err() != nil {
				if runtimeErr := receiveRuntimeError(runtimeErrors); runtimeErr != nil {
					return runtimeErr
				}
				return drain()
			}
			if !isTransientRPCError(err) {
				return fmt.Errorf("acquire jobs: %w", err)
			}
			if !wait(ctx, jitter(idleBackoff, worker.idleBackoffMax)) {
				continue
			}
			idleBackoff = nextBackoff(idleBackoff, worker.idleBackoffMax)
			continue
		}
		if uint32(len(acquired)) > capacity {
			return fmt.Errorf("dispatcher returned %d jobs for capacity %d", len(acquired), capacity)
		}
		if len(acquired) == 0 {
			if !wait(ctx, jitter(idleBackoff, worker.idleBackoffMax)) {
				continue
			}
			idleBackoff = nextBackoff(idleBackoff, worker.idleBackoffMax)
			continue
		}

		idleBackoff = worker.idleBackoffMin
		for _, job := range acquired {
			if _, ok := worker.handlers[job.Type.String()]; !ok {
				return fmt.Errorf("dispatcher returned unsupported job type %q", job.Type.String())
			}
			attemptParent := telemetry.ContextFromTraceParent(runCtx, job.TraceParent)
			attempt, err := active.add(attemptParent, job)
			if err != nil {
				return err
			}
			select {
			case jobs <- attempt:
			case <-runCtx.Done():
				active.remove(attempt, context.Cause(runCtx))
				if runtimeErr := receiveRuntimeError(runtimeErrors); runtimeErr != nil {
					return runtimeErr
				}
				return nil
			}
		}
	}
}

func (worker *Worker) execute(
	ctx context.Context,
	jobs <-chan *activeAttempt,
	active *activeAttempts,
	notifyCapacityChanged func(),
	fail func(error),
) {
	for {
		select {
		case <-ctx.Done():
			return
		case attempt := <-jobs:
			if ctx.Err() != nil {
				return
			}
			if errors.Is(context.Cause(attempt.ctx), errLeaseStale) {
				continue
			}
			job := attempt.job
			executionCtx, executionSpan := worker.tracer.Start(attempt.handlerCtx, "worker.execute", trace.WithAttributes(
				attribute.String("job.id", job.ID.String()),
				attribute.String("job.type", job.Type.String()),
				attribute.Int("job.attempt", int(job.AttemptNumber.Int32())),
				attribute.String("worker.id", worker.registration.WorkerID.String()),
			))
			worker.logger.InfoContext(
				executionCtx,
				"attempt started",
				slog.String("job_id", job.ID.String()),
				slog.String("job_type", job.Type.String()),
				slog.Int("attempt_no", int(job.AttemptNumber.Int32())),
				slog.String("worker_id", worker.registration.WorkerID.String()),
			)
			handler := worker.handlers[job.Type.String()]
			handlerCtx, cancelHandler := context.WithTimeoutCause(executionCtx, job.Timeout, errExecutionTimedOut)
			handlerCtx, handlerSpan := worker.tracer.Start(handlerCtx, "handler", trace.WithAttributes(
				attribute.String("job.id", job.ID.String()),
				attribute.String("job.type", job.Type.String()),
				attribute.Int("job.attempt", int(job.AttemptNumber.Int32())),
				attribute.String("worker.id", worker.registration.WorkerID.String()),
			))
			executionStarted := time.Now()
			execution := invokeHandler(handlerCtx, handler, job.Payload)
			executionDuration := time.Since(executionStarted)
			handlerSpan.End()
			cancelHandler()
			handlerCause := context.Cause(handlerCtx)
			if execution.panicked {
				worker.logger.ErrorContext(
					executionCtx,
					"handler panicked",
					slog.String("job_id", job.ID.String()),
					slog.Int("attempt_no", int(job.AttemptNumber.Int32())),
					slog.String("job_type", job.Type.String()),
					slog.String("worker_id", worker.registration.WorkerID.String()),
					slog.String("error_code", "handler_panicked"),
				)
			}
			if errors.Is(context.Cause(attempt.ctx), errLeaseStale) {
				executionSpan.End()
				continue
			}
			if ctx.Err() != nil {
				executionSpan.End()
				return
			}

			var outcome domain.AttemptOutcome
			var err error
			switch {
			case errors.Is(handlerCause, errCancellationRequested):
				outcome, err = cancelledOutcome()
			case errors.Is(handlerCause, errExecutionTimedOut):
				outcome, err = timedOutOutcome()
			case execution.panicked:
				outcome, err = panickedOutcome()
			default:
				outcome, err = classifyHandlerResult(execution.result, execution.err)
			}
			if err != nil {
				executionSpan.End()
				fail(fmt.Errorf("classify %s attempt %d: %w", job.ID, job.AttemptNumber.Int32(), err))
				return
			}
			if worker.metrics != nil {
				worker.metrics.JobExecutionCompleted(job.Type, outcome.Kind(), executionDuration)
			}
			executionSpan.SetAttributes(attribute.String("job.outcome", string(outcome.Kind())))
			if err := worker.runTestAfterHandlerSuccess(job, outcome); err != nil {
				executionSpan.End()
				fail(fmt.Errorf(
					"after successful handler for %s attempt %d: %w",
					job.ID,
					job.AttemptNumber.Int32(),
					err,
				))
				return
			}
			reportCtx := trace.ContextWithSpanContext(attempt.ctx, trace.SpanContextFromContext(executionCtx))
			if err := worker.reportUntilAcknowledged(reportCtx, job, outcome); err != nil {
				executionSpan.End()
				if errors.Is(err, errAttemptLost) || errors.Is(context.Cause(attempt.ctx), errLeaseStale) {
					if active.remove(attempt, err) {
						notifyCapacityChanged()
					}
					continue
				}
				if ctx.Err() == nil {
					fail(err)
				}
				return
			}
			logAttributes := []any{
				slog.String("job_id", job.ID.String()),
				slog.String("job_type", job.Type.String()),
				slog.Int("attempt_no", int(job.AttemptNumber.Int32())),
				slog.String("worker_id", worker.registration.WorkerID.String()),
				slog.String("job_outcome", string(outcome.Kind())),
			}
			if failure, ok := outcome.Failure(); ok {
				logAttributes = append(logAttributes, slog.String("error_code", failure.Code()))
			}
			worker.logger.InfoContext(executionCtx, "attempt report acknowledged", logAttributes...)
			executionSpan.End()
			if active.remove(attempt, nil) {
				notifyCapacityChanged()
			}
		}
	}
}

func (worker *Worker) runTestAfterHandlerSuccess(job Job, outcome domain.AttemptOutcome) error {
	if outcome.Kind() != domain.AttemptOutcomeKindSucceeded || worker.testAfterHandlerSuccess == nil {
		return nil
	}
	var err error
	worker.testAfterHandlerSuccessOnce.Do(func() {
		err = worker.testAfterHandlerSuccess(job)
	})
	return err
}

func (worker *Worker) heartbeatLoop(
	ctx context.Context,
	active *activeAttempts,
	notifyCapacityChanged func(),
) error {
	ticker := time.NewTicker(worker.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		results, err := worker.dispatcher.Heartbeat(
			ctx,
			worker.registration.WorkerID,
			active.snapshot(),
		)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if isTransientRPCError(err) {
				continue
			}
			return fmt.Errorf("heartbeat worker: %w", err)
		}
		for _, result := range results {
			if !result.Valid && active.cancel(result.Attempt, errLeaseStale) {
				notifyCapacityChanged()
				continue
			}
			if result.Valid && result.CancelRequested {
				active.signal(result.Attempt, errCancellationRequested)
			}
		}
	}
}

func (worker *Worker) reportUntilAcknowledged(
	ctx context.Context,
	job Job,
	outcome domain.AttemptOutcome,
) error {
	for {
		err := worker.dispatcher.ReportAttempt(
			ctx,
			worker.registration.WorkerID,
			job.ID,
			job.AttemptNumber,
			outcome,
		)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if status.Code(err) == codes.FailedPrecondition {
			return errAttemptLost
		}
		if !isTransientRPCError(err) {
			return fmt.Errorf("report %s attempt %d: %w", job.ID, job.AttemptNumber.Int32(), err)
		}
		if !wait(ctx, jitter(worker.reportBackoffMin, worker.reportBackoffMax)) {
			return ctx.Err()
		}
	}
}

func validateBackoff(minimum, maximum time.Duration) error {
	if minimum <= 0 || maximum < minimum {
		return errors.New("minimum must be positive and no greater than maximum")
	}
	return nil
}

func receiveRuntimeError(errors <-chan error) error {
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}

func isTransientRPCError(err error) bool {
	switch status.Code(err) {
	case codes.Aborted,
		codes.DeadlineExceeded,
		codes.Internal,
		codes.ResourceExhausted,
		codes.Unavailable:
		return true
	default:
		return false
	}
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}

func jitter(minimum, maximum time.Duration) time.Duration {
	if minimum >= maximum {
		return minimum
	}
	return minimum + time.Duration(rand.Int64N(int64(maximum-minimum)+1))
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
