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

var ErrInvalidConfiguration = errors.New("invalid worker configuration")

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

type Dispatcher interface {
	Register(context.Context, Registration) error
	Acquire(context.Context, domain.WorkerID, uint32, []domain.JobType) ([]Job, error)
	ReportSuccess(
		context.Context,
		domain.WorkerID,
		domain.JobID,
		domain.AttemptNumber,
		domain.Result,
	) error
}

type Config struct {
	Registration     Registration
	IdleBackoffMin   time.Duration
	IdleBackoffMax   time.Duration
	ReportBackoffMin time.Duration
	ReportBackoffMax time.Duration
}

type Worker struct {
	dispatcher       Dispatcher
	registration     Registration
	handlers         map[string]Handler
	supportedTypes   []domain.JobType
	idleBackoffMin   time.Duration
	idleBackoffMax   time.Duration
	reportBackoffMin time.Duration
	reportBackoffMax time.Duration
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
		dispatcher:       dispatcher,
		registration:     cfg.Registration,
		handlers:         handlerCopy,
		supportedTypes:   supportedTypes,
		idleBackoffMin:   cfg.IdleBackoffMin,
		idleBackoffMax:   cfg.IdleBackoffMax,
		reportBackoffMin: cfg.ReportBackoffMin,
		reportBackoffMax: cfg.ReportBackoffMax,
	}, nil
}

func (worker *Worker) Run(ctx context.Context) error {
	if err := worker.dispatcher.Register(ctx, worker.registration); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("register worker: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)

	jobs := make(chan Job, worker.registration.Concurrency)
	completed := make(chan struct{}, worker.registration.Concurrency)
	executorErrors := make(chan error, 1)
	var failOnce sync.Once
	fail := func(err error) {
		failOnce.Do(func() {
			executorErrors <- err
			cancel()
		})
	}

	var executors sync.WaitGroup
	for range worker.registration.Concurrency {
		executors.Add(1)
		go func() {
			defer executors.Done()
			worker.execute(runCtx, jobs, completed, fail)
		}()
	}
	defer func() {
		cancel()
		executors.Wait()
	}()

	outstanding := uint32(0)
	idleBackoff := worker.idleBackoffMin
	for {
		if err := receiveExecutorError(executorErrors); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}

		for {
			select {
			case <-completed:
				outstanding--
			default:
				goto completionsDrained
			}
		}

	completionsDrained:
		if outstanding == worker.registration.Concurrency {
			select {
			case <-ctx.Done():
				return nil
			case err := <-executorErrors:
				return err
			case <-completed:
				outstanding--
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
				if executorErr := receiveExecutorError(executorErrors); executorErr != nil {
					return executorErr
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
			outstanding++
			select {
			case jobs <- job:
			case <-runCtx.Done():
				if executorErr := receiveExecutorError(executorErrors); executorErr != nil {
					return executorErr
				}
				return nil
			}
		}
	}
}

func (worker *Worker) execute(
	ctx context.Context,
	jobs <-chan Job,
	completed chan<- struct{},
	fail func(error),
) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			if ctx.Err() != nil {
				return
			}
			handler := worker.handlers[job.Type.String()]
			result, err := handler(ctx, job.Payload)
			if err != nil {
				fail(fmt.Errorf("execute %s attempt %d: %w", job.ID, job.AttemptNumber.Int32(), err))
				return
			}
			if err := worker.reportUntilAcknowledged(ctx, job, result); err != nil {
				if ctx.Err() == nil {
					fail(err)
				}
				return
			}
			select {
			case completed <- struct{}{}:
			case <-ctx.Done():
				return
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

func receiveExecutorError(errors <-chan error) error {
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
