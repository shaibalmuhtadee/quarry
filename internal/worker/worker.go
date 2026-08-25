package worker

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrInvalidConfiguration = errors.New("invalid worker configuration")
	errLeaseStale           = errors.New("attempt lease is stale")
	errAttemptLost          = errors.New("attempt report lost its lease race")
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
}

type HeartbeatAttempt struct {
	JobID         domain.JobID
	AttemptNumber domain.AttemptNumber
}

type HeartbeatResult struct {
	Attempt HeartbeatAttempt
	Valid   bool
}

type Dispatcher interface {
	Register(context.Context, Registration) error
	Acquire(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error)
	Heartbeat(context.Context, domain.WorkerID, []HeartbeatAttempt) ([]HeartbeatResult, error)
	ReportSuccess(
		context.Context,
		domain.WorkerID,
		domain.JobID,
		domain.AttemptNumber,
		domain.Result,
	) error
}

type Config struct {
	Registration      Registration
	IdleBackoffMin    time.Duration
	IdleBackoffMax    time.Duration
	ReportBackoffMin  time.Duration
	ReportBackoffMax  time.Duration
	HeartbeatInterval time.Duration
}

type Worker struct {
	dispatcher        Dispatcher
	registration      Registration
	handlers          map[string]Handler
	supportedTypes    []domain.JobType
	idleBackoffMin    time.Duration
	idleBackoffMax    time.Duration
	reportBackoffMin  time.Duration
	reportBackoffMax  time.Duration
	heartbeatInterval time.Duration
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
		dispatcher:        dispatcher,
		registration:      cfg.Registration,
		handlers:          handlerCopy,
		supportedTypes:    supportedTypes,
		idleBackoffMin:    cfg.IdleBackoffMin,
		idleBackoffMax:    cfg.IdleBackoffMax,
		reportBackoffMin:  cfg.ReportBackoffMin,
		reportBackoffMax:  cfg.ReportBackoffMax,
		heartbeatInterval: cfg.HeartbeatInterval,
	}, nil
}

type activeAttempt struct {
	identity HeartbeatAttempt
	job      Job
	ctx      context.Context
	cancel   context.CancelCauseFunc
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
	attempt := &activeAttempt{identity: identity, job: job, ctx: ctx, cancel: cancel}

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

func (worker *Worker) Run(ctx context.Context) error {
	if err := worker.dispatcher.Register(ctx, worker.registration); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("register worker: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)

	jobs := make(chan *activeAttempt, worker.registration.Concurrency)
	capacityChanged := make(chan struct{}, 1)
	runtimeErrors := make(chan error, 1)
	active := newActiveAttempts()
	var failOnce sync.Once
	fail := func(err error) {
		failOnce.Do(func() {
			runtimeErrors <- err
			cancel()
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
	defer func() {
		cancel()
		goroutines.Wait()
	}()

	idleBackoff := worker.idleBackoffMin
	for {
		if err := receiveRuntimeError(runtimeErrors); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}

		outstanding := uint32(active.len())
		if outstanding >= worker.registration.Concurrency {
			select {
			case <-ctx.Done():
				return nil
			case err := <-runtimeErrors:
				return err
			case <-capacityChanged:
			}
			continue
		}

		capacity := worker.registration.Concurrency - outstanding
		acquired, err := worker.dispatcher.Acquire(
			runCtx,
			worker.registration.WorkerID,
			capacity,
			worker.supportedTypes,
		)
		if err != nil {
			if runCtx.Err() != nil {
				if runtimeErr := receiveRuntimeError(runtimeErrors); runtimeErr != nil {
					return runtimeErr
				}
				return nil
			}
			if !isTransientRPCError(err) {
				return fmt.Errorf("acquire jobs: %w", err)
			}
			if !wait(runCtx, jitter(idleBackoff, worker.idleBackoffMax)) {
				continue
			}
			idleBackoff = nextBackoff(idleBackoff, worker.idleBackoffMax)
			continue
		}
		if uint32(len(acquired)) > capacity {
			return fmt.Errorf("dispatcher returned %d jobs for capacity %d", len(acquired), capacity)
		}
		if len(acquired) == 0 {
			if !wait(runCtx, jitter(idleBackoff, worker.idleBackoffMax)) {
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
			attempt, err := active.add(runCtx, job)
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
			handler := worker.handlers[job.Type.String()]
			result, err := handler(attempt.ctx, job.Payload)
			if errors.Is(context.Cause(attempt.ctx), errLeaseStale) {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				fail(fmt.Errorf("execute %s attempt %d: %w", job.ID, job.AttemptNumber.Int32(), err))
				return
			}
			if err := worker.reportUntilAcknowledged(attempt.ctx, job, result); err != nil {
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
			if active.remove(attempt, nil) {
				notifyCapacityChanged()
			}
		}
	}
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
			}
		}
	}
}

func (worker *Worker) reportUntilAcknowledged(
	ctx context.Context,
	job Job,
	result domain.Result,
) error {
	for {
		err := worker.dispatcher.ReportSuccess(
			ctx,
			worker.registration.WorkerID,
			job.ID,
			job.AttemptNumber,
			result,
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
