package loadgen

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	RunID               string
	WarmupDuration      time.Duration
	MeasurementDuration time.Duration
	DrainTimeout        time.Duration
	PollInterval        time.Duration
	MaxOutstanding      int
	MaxHTTPConcurrency  int
}

type RunResult struct {
	RunID                string
	StartedAt            time.Time
	MeasurementStartedAt time.Time
	MeasurementEndedAt   time.Time
	DrainEndedAt         time.Time
	Samples              []Sample
}

type Runner struct {
	client     Client
	config     Config
	submission SubmissionFactory
	now        func() time.Time
	requests   chan struct{}
	sequence   atomic.Uint64
}

func NewRunner(client Client, config Config, submission SubmissionFactory) (*Runner, error) {
	if client == nil {
		return nil, errors.New("load generator client is required")
	}
	if submission == nil {
		return nil, errors.New("submission factory is required")
	}
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}

	return &Runner{
		client:     client,
		config:     config,
		submission: submission,
		now:        func() time.Time { return time.Now().UTC() },
		requests:   make(chan struct{}, config.MaxHTTPConcurrency),
	}, nil
}

func ValidateConfig(config Config) error {
	if config.RunID == "" || len(config.RunID) > 220 {
		return errors.New("run ID must contain 1 to 220 bytes")
	}
	if config.WarmupDuration < 0 {
		return errors.New("warmup duration must not be negative")
	}
	if config.MeasurementDuration <= 0 {
		return errors.New("measurement duration must be positive")
	}
	if config.DrainTimeout <= 0 {
		return errors.New("drain timeout must be positive")
	}
	if config.PollInterval <= 0 {
		return errors.New("poll interval must be positive")
	}
	if config.MaxOutstanding <= 0 {
		return errors.New("maximum outstanding jobs must be positive")
	}
	if config.MaxHTTPConcurrency <= 0 {
		return errors.New("maximum HTTP concurrency must be positive")
	}
	return nil
}

func (runner *Runner) Run(ctx context.Context) (RunResult, error) {
	startedAt := runner.now()
	measurementStartedAt := startedAt.Add(runner.config.WarmupDuration)
	measurementEndedAt := measurementStartedAt.Add(runner.config.MeasurementDuration)
	drainDeadline := measurementEndedAt.Add(runner.config.DrainTimeout)
	runCtx, cancel := context.WithDeadline(ctx, drainDeadline)
	defer cancel()

	sampleCh := make(chan Sample, runner.config.MaxOutstanding)
	var slots sync.WaitGroup
	for range runner.config.MaxOutstanding {
		slots.Add(1)
		go func() {
			defer slots.Done()
			runner.runSlot(runCtx, measurementStartedAt, measurementEndedAt, drainDeadline, sampleCh)
		}()
	}
	go func() {
		slots.Wait()
		close(sampleCh)
	}()

	result := RunResult{
		RunID:                runner.config.RunID,
		StartedAt:            startedAt,
		MeasurementStartedAt: measurementStartedAt,
		MeasurementEndedAt:   measurementEndedAt,
	}
	for sample := range sampleCh {
		result.Samples = append(result.Samples, sample)
	}
	sort.Slice(result.Samples, func(i, j int) bool {
		return result.Samples[i].Header().Sequence < result.Samples[j].Header().Sequence
	})
	result.DrainEndedAt = runner.now()

	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func (runner *Runner) runSlot(
	ctx context.Context,
	measurementStartedAt time.Time,
	measurementEndedAt time.Time,
	drainDeadline time.Time,
	samples chan<- Sample,
) {
	for {
		startedAt := runner.now()
		if !startedAt.Before(measurementEndedAt) {
			return
		}
		sequence := runner.sequence.Add(1)
		phase := phaseAt(startedAt, measurementStartedAt)
		request := runner.submission(sequence)
		header := SampleHeader{
			RunID:                runner.config.RunID,
			Sequence:             sequence,
			Phase:                phase,
			JobType:              request.JobType,
			SubmissionStartedAt:  startedAt,
			MeasurementStartedAt: measurementStartedAt,
			MeasurementEndedAt:   measurementEndedAt,
		}
		sample, continueSlot := runner.runJob(ctx, header, request, drainDeadline)
		samples <- sample
		if !continueSlot {
			return
		}
	}
}

func phaseAt(submissionStartedAt, measurementStartedAt time.Time) Phase {
	if submissionStartedAt.Before(measurementStartedAt) {
		return PhaseWarmup
	}
	return PhaseMeasurement
}

func (runner *Runner) runJob(
	ctx context.Context,
	header SampleHeader,
	submission Submission,
	drainDeadline time.Time,
) (Sample, bool) {
	request := SubmissionRequest{
		Submission:     submission,
		IdempotencyKey: fmt.Sprintf("%s-%020d", runner.config.RunID, header.Sequence),
	}
	var requestErrors []RequestError
	mayHaveCommitted := false
	var submitted SubmittedJob
	for {
		job, err := runner.submit(ctx, request)
		observedAt := runner.now()
		if err == nil {
			submitted = job
			break
		}
		ambiguous := IsAmbiguous(err)
		mayHaveCommitted = mayHaveCommitted || ambiguous
		requestErrors = append(requestErrors, requestError(OperationSubmit, observedAt, err))
		if !IsRetryable(err) {
			return SubmissionFailureSample{
				Base:             header,
				MayHaveCommitted: mayHaveCommitted,
				Errors:           requestErrors,
			}, !mayHaveCommitted
		}
		if !wait(ctx, runner.config.PollInterval) {
			requestErrors = appendContextError(requestErrors, OperationSubmit, runner.now(), ctx.Err())
			return SubmissionFailureSample{
				Base:             header,
				MayHaveCommitted: mayHaveCommitted,
				Errors:           requestErrors,
			}, false
		}
	}
	submissionCompletedAt := runner.now()

	lastStatus := submitted.Status
	for {
		job, err := runner.getJob(ctx, submitted.ID)
		observedAt := runner.now()
		if err != nil {
			requestErrors = append(requestErrors, requestError(OperationPoll, observedAt, err))
			if !IsRetryable(err) {
				return incompleteSample(header, submitted, submissionCompletedAt, lastStatus, observedAt, requestErrors), false
			}
			if !wait(ctx, runner.config.PollInterval) {
				requestErrors = appendContextError(requestErrors, OperationPoll, runner.now(), ctx.Err())
				return incompleteSample(header, submitted, submissionCompletedAt, lastStatus, drainDeadline, requestErrors), false
			}
			continue
		}
		lastStatus = job.Status
		if !job.Status.Terminal() {
			if !wait(ctx, runner.config.PollInterval) {
				requestErrors = appendContextError(requestErrors, OperationPoll, runner.now(), ctx.Err())
				return incompleteSample(header, submitted, submissionCompletedAt, lastStatus, drainDeadline, requestErrors), false
			}
			continue
		}

		terminalObservedAt := observedAt
		if job.FinishedAt == nil {
			err := errors.New("terminal job response omitted finished_at")
			requestErrors = append(requestErrors, requestError(OperationPoll, observedAt, err))
			return incompleteSample(header, submitted, submissionCompletedAt, lastStatus, observedAt, requestErrors), false
		}
		for {
			attempts, err := runner.getJobAttempts(ctx, submitted.ID)
			attemptsObservedAt := runner.now()
			if err == nil {
				return TerminalJobSample{
					Base:                  header,
					JobID:                 submitted.ID,
					CreatedAt:             job.CreatedAt,
					SubmissionCompletedAt: submissionCompletedAt,
					Status:                job.Status,
					FinishedAt:            *job.FinishedAt,
					TerminalObservedAt:    terminalObservedAt,
					Attempts:              toAttemptSamples(attempts),
					Errors:                requestErrors,
				}, true
			}
			requestErrors = append(requestErrors, requestError(OperationAttempts, attemptsObservedAt, err))
			if !IsRetryable(err) {
				return incompleteSample(header, submitted, submissionCompletedAt, lastStatus, attemptsObservedAt, requestErrors), false
			}
			if !wait(ctx, runner.config.PollInterval) {
				requestErrors = appendContextError(requestErrors, OperationAttempts, runner.now(), ctx.Err())
				return incompleteSample(header, submitted, submissionCompletedAt, lastStatus, drainDeadline, requestErrors), false
			}
		}
	}
}

func (runner *Runner) submit(ctx context.Context, request SubmissionRequest) (SubmittedJob, error) {
	if err := runner.acquireRequest(ctx); err != nil {
		return SubmittedJob{}, err
	}
	defer runner.releaseRequest()
	return runner.client.SubmitJob(ctx, request)
}

func (runner *Runner) getJob(ctx context.Context, id string) (Job, error) {
	if err := runner.acquireRequest(ctx); err != nil {
		return Job{}, err
	}
	defer runner.releaseRequest()
	return runner.client.GetJob(ctx, id)
}

func (runner *Runner) getJobAttempts(ctx context.Context, id string) ([]Attempt, error) {
	if err := runner.acquireRequest(ctx); err != nil {
		return nil, err
	}
	defer runner.releaseRequest()
	return runner.client.GetJobAttempts(ctx, id)
}

func (runner *Runner) acquireRequest(ctx context.Context) error {
	select {
	case runner.requests <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (runner *Runner) releaseRequest() {
	<-runner.requests
}

func incompleteSample(
	header SampleHeader,
	submitted SubmittedJob,
	submissionCompletedAt time.Time,
	status JobStatus,
	drainEndedAt time.Time,
	errors []RequestError,
) IncompleteJobSample {
	return IncompleteJobSample{
		Base:                  header,
		JobID:                 submitted.ID,
		CreatedAt:             submitted.CreatedAt,
		SubmissionCompletedAt: submissionCompletedAt,
		LastStatus:            status,
		DrainEndedAt:          drainEndedAt,
		Errors:                errors,
	}
}

func requestError(operation RequestOperation, observedAt time.Time, err error) RequestError {
	return RequestError{
		Operation:  operation,
		ObservedAt: observedAt,
		Retryable:  IsRetryable(err),
		Ambiguous:  IsAmbiguous(err),
		Message:    err.Error(),
	}
}

func appendContextError(errors []RequestError, operation RequestOperation, observedAt time.Time, err error) []RequestError {
	if err == nil {
		return errors
	}
	return append(errors, RequestError{
		Operation:  operation,
		ObservedAt: observedAt,
		Message:    err.Error(),
	})
}

func toAttemptSamples(attempts []Attempt) []AttemptSample {
	samples := make([]AttemptSample, 0, len(attempts))
	for _, attempt := range attempts {
		samples = append(samples, AttemptSample{
			Number:       attempt.Number,
			WorkerID:     attempt.WorkerID,
			Status:       attempt.Status,
			ErrorCode:    attempt.ErrorCode,
			ErrorMessage: attempt.ErrorMessage,
			StartedAt:    attempt.StartedAt,
			FinishedAt:   attempt.FinishedAt,
		})
	}
	return samples
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
