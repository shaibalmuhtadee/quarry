package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	postgresdb "github.com/shaibalmuhtadee/quarry/internal/store/postgres/generated"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	ErrWorkerNotRegistered        = errors.New("worker is not registered")
	ErrWorkerRegistrationConflict = errors.New("worker registration conflicts with the stored registration")
	ErrAttemptReportConflict      = errors.New("attempt report conflicts with the stored attempt")
	ErrInvalidRecoveryConfig      = errors.New("recovery batch size and worker liveness timeout must be positive")
)

type WorkerRegistration struct {
	ID          domain.WorkerID
	Hostname    string
	Version     string
	Concurrency int32
	StartedAt   time.Time
}

type AcquiredJob struct {
	ID              domain.JobID
	AttemptNumber   domain.AttemptNumber
	Type            domain.JobType
	Payload         domain.Payload
	Timeout         time.Duration
	SchedulingDelay time.Duration
	TraceParent     string
}

type AttemptReportTransition struct {
	Applied       bool
	JobType       domain.JobType
	AttemptStatus domain.AttemptStatus
	JobStatus     domain.JobStatus
	ErrorCode     string
}

type RecoveryTransition struct {
	JobID         domain.JobID
	JobType       domain.JobType
	AttemptNumber domain.AttemptNumber
	AttemptStatus domain.AttemptStatus
	JobStatus     domain.JobStatus
	ErrorCode     string
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

type DispatcherStore struct {
	pool          *pgxpool.Pool
	leaseDuration time.Duration
	retryPolicy   domain.RetryPolicy
	tracer        trace.Tracer
}

func NewDispatcherStore(
	pool *pgxpool.Pool,
	leaseDuration time.Duration,
	retryPolicy domain.RetryPolicy,
) *DispatcherStore {
	return NewDispatcherStoreWithTracer(pool, leaseDuration, retryPolicy, trace.NewNoopTracerProvider().Tracer("quarry/store/postgres"))
}

func NewDispatcherStoreWithTracer(
	pool *pgxpool.Pool,
	leaseDuration time.Duration,
	retryPolicy domain.RetryPolicy,
	tracer trace.Tracer,
) *DispatcherStore {
	return &DispatcherStore{
		pool:          pool,
		leaseDuration: leaseDuration,
		retryPolicy:   retryPolicy,
		tracer:        tracer,
	}
}

func (store *DispatcherStore) RegisterWorker(
	ctx context.Context,
	registration WorkerRegistration,
) error {
	_, err := postgresdb.New(store.pool).RegisterWorker(ctx, postgresdb.RegisterWorkerParams{
		ID:          registration.ID.UUID(),
		Hostname:    registration.Hostname,
		Version:     registration.Version,
		Concurrency: registration.Concurrency,
		StartedAt: pgtype.Timestamptz{
			Time:  registration.StartedAt,
			Valid: true,
		},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWorkerRegistrationConflict
	}
	if err != nil {
		return fmt.Errorf("register worker: %w", err)
	}

	return nil
}

func (store *DispatcherStore) AcquireJobs(
	ctx context.Context,
	workerID domain.WorkerID,
	availableCapacity int32,
	supportedJobTypes []domain.JobType,
) ([]AcquiredJob, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin job acquisition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := postgresdb.New(tx)
	registeredConcurrency, err := queries.LockWorker(ctx, workerID.UUID())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkerNotRegistered
	}
	if err != nil {
		return nil, fmt.Errorf("lock acquiring worker: %w", err)
	}

	databaseWorkerID := pgtype.UUID{
		Bytes: workerID.UUID(),
		Valid: true,
	}
	runningJobs, err := queries.CountWorkerRunningJobs(ctx, databaseWorkerID)
	if err != nil {
		return nil, fmt.Errorf("count worker running jobs: %w", err)
	}

	claimLimit := min(availableCapacity, registeredConcurrency-int32(runningJobs))
	if claimLimit <= 0 || len(supportedJobTypes) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit empty job acquisition: %w", err)
		}
		return []AcquiredJob{}, nil
	}

	typeNames := make([]string, len(supportedJobTypes))
	for i, jobType := range supportedJobTypes {
		typeNames[i] = jobType.String()
	}

	rows, err := queries.ClaimJobs(ctx, postgresdb.ClaimJobsParams{
		SupportedJobTypes: typeNames,
		ClaimLimit:        claimLimit,
		WorkerID:          databaseWorkerID,
		LeaseDurationMs:   store.leaseDuration.Milliseconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("claim jobs: %w", err)
	}

	jobs := make([]AcquiredJob, 0, len(rows))
	for _, row := range rows {
		job, err := mapAcquiredJob(row)
		if err != nil {
			return nil, fmt.Errorf("map acquired job: %w", err)
		}
		jobs = append(jobs, job)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit job acquisition: %w", err)
	}

	return jobs, nil
}

func (store *DispatcherStore) Heartbeat(
	ctx context.Context,
	workerID domain.WorkerID,
	attempts []HeartbeatAttempt,
) ([]HeartbeatResult, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin worker heartbeat: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := postgresdb.New(tx)
	workerRows, err := queries.RefreshWorkerHeartbeat(ctx, workerID.UUID())
	if err != nil {
		return nil, fmt.Errorf("refresh worker heartbeat: %w", err)
	}
	if workerRows == 0 {
		return nil, ErrWorkerNotRegistered
	}
	if workerRows != 1 {
		return nil, errors.New("worker heartbeat updated an unexpected number of workers")
	}

	results := make([]HeartbeatResult, len(attempts))
	for i, attempt := range attempts {
		cancelRequested, err := queries.RenewAttemptLease(ctx, postgresdb.RenewAttemptLeaseParams{
			LeaseDurationMs: store.leaseDuration.Milliseconds(),
			JobID:           attempt.JobID.UUID(),
			AttemptNo:       attempt.AttemptNumber.Int32(),
			WorkerID: pgtype.UUID{
				Bytes: workerID.UUID(),
				Valid: true,
			},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			results[i] = HeartbeatResult{Attempt: attempt}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("renew attempt lease: %w", err)
		}
		results[i] = HeartbeatResult{
			Attempt:         attempt,
			Valid:           true,
			CancelRequested: cancelRequested,
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit worker heartbeat: %w", err)
	}

	return results, nil
}

func (store *DispatcherStore) RecoverExpiredAttempts(
	ctx context.Context,
	batchSize int32,
	workerLivenessTimeout time.Duration,
) (int64, error) {
	transitions, err := store.RecoverExpiredAttemptTransitions(ctx, batchSize, workerLivenessTimeout)
	return int64(len(transitions)), err
}

func (store *DispatcherStore) RecoverExpiredAttemptTransitions(
	ctx context.Context,
	batchSize int32,
	workerLivenessTimeout time.Duration,
) ([]RecoveryTransition, error) {
	if batchSize <= 0 || workerLivenessTimeout <= 0 || workerLivenessTimeout.Milliseconds() <= 0 {
		return nil, ErrInvalidRecoveryConfig
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin expired attempt recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := postgresdb.New(tx)
	if _, err := queries.MarkLostWorkers(ctx, workerLivenessTimeout.Milliseconds()); err != nil {
		return nil, fmt.Errorf("mark lost workers: %w", err)
	}

	expiredJobs, err := queries.LockExpiredJobs(ctx, batchSize)
	if err != nil {
		return nil, fmt.Errorf("lock expired jobs: %w", err)
	}
	transitions := make([]RecoveryTransition, 0, len(expiredJobs))
	for _, expired := range expiredJobs {
		jobID, err := domain.ParseJobID(expired.ID.String())
		if err != nil {
			return nil, fmt.Errorf("parse expired job ID: %w", err)
		}
		jobType, err := domain.ParseJobType(expired.JobType)
		if err != nil {
			return nil, fmt.Errorf("parse expired job type: %w", err)
		}
		attemptNumber, err := domain.NewAttemptNumber(expired.AttemptCount)
		if err != nil {
			return nil, fmt.Errorf("parse expired attempt number: %w", err)
		}
		jobStatus := domain.JobStatusRetryWait
		attemptStatus := domain.AttemptStatusAbandoned
		var retryDelay time.Duration
		if expired.CancelRequestedAt.Valid {
			jobStatus = domain.JobStatusCancelled
			attemptStatus = domain.AttemptStatusCancelled
		} else if expired.AttemptCount >= expired.MaxAttempts {
			jobStatus = domain.JobStatusDeadLettered
		} else {
			retryDelay, err = store.retryPolicy.Delay(attemptNumber)
			if err != nil {
				return nil, fmt.Errorf("calculate expired attempt retry delay: %w", err)
			}
		}
		transitionTime, err := queries.GetTransitionTime(ctx)
		if err != nil {
			return nil, fmt.Errorf("read expired attempt transition time: %w", err)
		}
		if !transitionTime.Valid {
			return nil, errors.New("expired attempt transition time is null")
		}

		var attemptRows int64
		if attemptStatus == domain.AttemptStatusCancelled {
			attemptRows, err = queries.CancelExpiredAttempt(ctx, postgresdb.CancelExpiredAttemptParams{
				TransitionTime: transitionTime,
				JobID:          expired.ID,
				AttemptNo:      expired.AttemptCount,
			})
		} else {
			attemptRows, err = queries.AbandonExpiredAttempt(ctx, postgresdb.AbandonExpiredAttemptParams{
				TransitionTime: transitionTime,
				JobID:          expired.ID,
				AttemptNo:      expired.AttemptCount,
			})
		}
		if err != nil {
			return nil, fmt.Errorf("abandon expired attempt: %w", err)
		}
		if attemptRows != 1 {
			return nil, errors.New("abandon expired attempt updated an unexpected number of rows")
		}

		jobRows, err := queries.RecoverExpiredJob(ctx, postgresdb.RecoverExpiredJobParams{
			JobStatus:      string(jobStatus),
			TransitionTime: transitionTime,
			RetryDelayMs:   retryDelay.Milliseconds(),
			JobID:          expired.ID,
			AttemptNo:      expired.AttemptCount,
		})
		if err != nil {
			return nil, fmt.Errorf("recover expired job: %w", err)
		}
		if jobRows != 1 {
			return nil, errors.New("recover expired job updated an unexpected number of rows")
		}
		errorCode := "lease_expired"
		if attemptStatus == domain.AttemptStatusCancelled {
			errorCode = "cancellation_requested"
		}
		transitions = append(transitions, RecoveryTransition{
			JobID:         jobID,
			JobType:       jobType,
			AttemptNumber: attemptNumber,
			AttemptStatus: attemptStatus,
			JobStatus:     jobStatus,
			ErrorCode:     errorCode,
			TraceParent:   expired.Traceparent.String,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit expired attempt recovery: %w", err)
	}

	return transitions, nil
}

func (store *DispatcherStore) ReportAttempt(
	ctx context.Context,
	workerID domain.WorkerID,
	jobID domain.JobID,
	attemptNumber domain.AttemptNumber,
	outcome domain.AttemptOutcome,
) error {
	_, err := store.ReportAttemptTransition(ctx, workerID, jobID, attemptNumber, outcome)
	return err
}

func (store *DispatcherStore) ReportAttemptTransition(
	ctx context.Context,
	workerID domain.WorkerID,
	jobID domain.JobID,
	attemptNumber domain.AttemptNumber,
	outcome domain.AttemptOutcome,
) (AttemptReportTransition, error) {
	ctx, span := store.tracer.Start(ctx, "db.complete_attempt", trace.WithAttributes(
		attribute.String("job.id", jobID.String()),
		attribute.Int("job.attempt", int(attemptNumber.Int32())),
		attribute.String("worker.id", workerID.String()),
		attribute.String("job.outcome", string(outcome.Kind())),
	))
	defer span.End()

	if outcome.IsZero() {
		return AttemptReportTransition{}, domain.ErrInvalidAttemptOutcome
	}

	attemptStatus, jobStatus, resultJSON, failure, err := reportTransition(outcome)
	if err != nil {
		return AttemptReportTransition{}, err
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AttemptReportTransition{}, fmt.Errorf("begin attempt report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := postgresdb.New(tx)
	report, err := queries.LockAttemptReport(ctx, postgresdb.LockAttemptReportParams{
		ResultJson: resultJSON,
		AttemptNo:  attemptNumber.Int32(),
		JobID:      jobID.UUID(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AttemptReportTransition{}, ErrAttemptReportConflict
	}
	if err != nil {
		return AttemptReportTransition{}, fmt.Errorf("lock attempt report: %w", err)
	}
	if report.AttemptCount != attemptNumber.Int32() || report.WorkerID != workerID.UUID() {
		return AttemptReportTransition{}, ErrAttemptReportConflict
	}
	jobType, err := domain.ParseJobType(report.JobType)
	if err != nil {
		return AttemptReportTransition{}, fmt.Errorf("parse reported job type: %w", err)
	}
	if report.CancelRequestedAt.Valid && outcome.Kind() != domain.AttemptOutcomeKindSucceeded {
		attemptStatus, jobStatus, resultJSON, failure, err = cancellationRequestedTransition()
		if err != nil {
			return AttemptReportTransition{}, err
		}
	}
	if jobStatus == domain.JobStatusRetryWait && report.AttemptCount >= report.MaxAttempts {
		jobStatus = domain.JobStatusDeadLettered
	}

	if report.AttemptStatus != string(domain.AttemptStatusRunning) {
		if report.JobStatus != string(jobStatus) ||
			report.AttemptStatus != string(attemptStatus) ||
			!storedFailureMatches(report.ErrorCode, report.ErrorMessage, failure) ||
			(attemptStatus == domain.AttemptStatusSucceeded && !report.ResultMatches) {
			return AttemptReportTransition{}, ErrAttemptReportConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return AttemptReportTransition{}, fmt.Errorf("commit repeated attempt report: %w", err)
		}
		return newAttemptReportTransition(false, jobType, attemptStatus, jobStatus, failure), nil
	}
	if report.JobStatus != string(domain.JobStatusRunning) || !report.LeaseExpiresAt.Valid {
		return AttemptReportTransition{}, ErrAttemptReportConflict
	}
	transitionTime, err := queries.GetTransitionTime(ctx)
	if err != nil {
		return AttemptReportTransition{}, fmt.Errorf("read attempt transition time: %w", err)
	}
	if !transitionTime.Valid {
		return AttemptReportTransition{}, errors.New("attempt transition time is null")
	}
	if !report.LeaseExpiresAt.Time.After(transitionTime.Time) {
		return AttemptReportTransition{}, ErrAttemptReportConflict
	}

	var retryDelay time.Duration
	if jobStatus == domain.JobStatusRetryWait {
		retryDelay, err = store.retryPolicy.Delay(attemptNumber)
		if err != nil {
			return AttemptReportTransition{}, fmt.Errorf("calculate attempt retry delay: %w", err)
		}
	}
	attemptRows, err := queries.FinishAttempt(ctx, postgresdb.FinishAttemptParams{
		AttemptStatus:  string(attemptStatus),
		ErrorCode:      nullableText(failure, true),
		ErrorMessage:   nullableText(failure, false),
		TransitionTime: transitionTime,
		JobID:          jobID.UUID(),
		AttemptNo:      attemptNumber.Int32(),
		WorkerID:       workerID.UUID(),
	})
	if err != nil {
		return AttemptReportTransition{}, fmt.Errorf("finish attempt: %w", err)
	}
	if attemptRows != 1 {
		return AttemptReportTransition{}, errors.New("finish attempt updated an unexpected number of rows")
	}

	jobRows, err := queries.FinishJob(ctx, postgresdb.FinishJobParams{
		JobStatus:      string(jobStatus),
		ResultJson:     resultJSON,
		TransitionTime: transitionTime,
		RetryDelayMs:   retryDelay.Milliseconds(),
		JobID:          jobID.UUID(),
		AttemptNo:      attemptNumber.Int32(),
		WorkerID: pgtype.UUID{
			Bytes: workerID.UUID(),
			Valid: true,
		},
	})
	if err != nil {
		return AttemptReportTransition{}, fmt.Errorf("finish job: %w", err)
	}
	if jobRows != 1 {
		return AttemptReportTransition{}, errors.New("finish job updated an unexpected number of rows")
	}

	if err := tx.Commit(ctx); err != nil {
		return AttemptReportTransition{}, fmt.Errorf("commit attempt report: %w", err)
	}

	return newAttemptReportTransition(true, jobType, attemptStatus, jobStatus, failure), nil
}

func newAttemptReportTransition(
	applied bool,
	jobType domain.JobType,
	attemptStatus domain.AttemptStatus,
	jobStatus domain.JobStatus,
	failure *domain.AttemptFailure,
) AttemptReportTransition {
	errorCode := ""
	if failure != nil {
		errorCode = failure.Code()
	}
	return AttemptReportTransition{
		Applied:       applied,
		JobType:       jobType,
		AttemptStatus: attemptStatus,
		JobStatus:     jobStatus,
		ErrorCode:     errorCode,
	}
}

func cancellationRequestedTransition() (
	domain.AttemptStatus,
	domain.JobStatus,
	[]byte,
	*domain.AttemptFailure,
	error,
) {
	failure, err := domain.NewAttemptFailure("cancellation_requested", "job cancellation was requested")
	if err != nil {
		return "", "", nil, nil, err
	}
	return domain.AttemptStatusCancelled, domain.JobStatusCancelled, nil, &failure, nil
}

func reportTransition(
	outcome domain.AttemptOutcome,
) (domain.AttemptStatus, domain.JobStatus, []byte, *domain.AttemptFailure, error) {
	switch outcome.Kind() {
	case domain.AttemptOutcomeKindSucceeded:
		result, ok := outcome.Result()
		if !ok {
			return "", "", nil, nil, domain.ErrInvalidAttemptOutcome
		}
		return domain.AttemptStatusSucceeded, domain.JobStatusSucceeded, result.JSON(), nil, nil
	case domain.AttemptOutcomeKindRetryableFailure:
		return failureTransition(outcome, domain.AttemptStatusRetryableFailed, domain.JobStatusRetryWait)
	case domain.AttemptOutcomeKindPermanentFailure:
		return failureTransition(outcome, domain.AttemptStatusPermanentFailed, domain.JobStatusDeadLettered)
	case domain.AttemptOutcomeKindCancelled:
		return failureTransition(outcome, domain.AttemptStatusCancelled, domain.JobStatusCancelled)
	case domain.AttemptOutcomeKindTimedOut:
		return failureTransition(outcome, domain.AttemptStatusTimedOut, domain.JobStatusRetryWait)
	case domain.AttemptOutcomeKindPanicked:
		return failureTransition(outcome, domain.AttemptStatusPanicked, domain.JobStatusRetryWait)
	default:
		return "", "", nil, nil, domain.ErrInvalidAttemptOutcomeKind
	}
}

func failureTransition(
	outcome domain.AttemptOutcome,
	attemptStatus domain.AttemptStatus,
	jobStatus domain.JobStatus,
) (domain.AttemptStatus, domain.JobStatus, []byte, *domain.AttemptFailure, error) {
	failure, ok := outcome.Failure()
	if !ok {
		return "", "", nil, nil, domain.ErrInvalidAttemptOutcome
	}
	return attemptStatus, jobStatus, nil, &failure, nil
}

func nullableText(failure *domain.AttemptFailure, code bool) pgtype.Text {
	if failure == nil {
		return pgtype.Text{}
	}
	value := failure.Message()
	if code {
		value = failure.Code()
	}
	return pgtype.Text{String: value, Valid: true}
}

func storedFailureMatches(code, message pgtype.Text, failure *domain.AttemptFailure) bool {
	if failure == nil {
		return !code.Valid && !message.Valid
	}
	return code.Valid && message.Valid && code.String == failure.Code() && message.String == failure.Message()
}

func mapAcquiredJob(row postgresdb.ClaimJobsRow) (AcquiredJob, error) {
	id, err := domain.ParseJobID(row.ID.String())
	if err != nil {
		return AcquiredJob{}, err
	}
	attemptNumber, err := domain.NewAttemptNumber(row.AttemptCount)
	if err != nil {
		return AcquiredJob{}, err
	}
	jobType, err := domain.ParseJobType(row.JobType)
	if err != nil {
		return AcquiredJob{}, err
	}
	payload, err := domain.ParsePayload(row.Payload)
	if err != nil {
		return AcquiredJob{}, err
	}

	traceParent := ""
	if row.Traceparent.Valid {
		traceParent = row.Traceparent.String
	}
	return AcquiredJob{
		ID:              id,
		AttemptNumber:   attemptNumber,
		Type:            jobType,
		Payload:         payload,
		Timeout:         time.Duration(row.TimeoutMs) * time.Millisecond,
		SchedulingDelay: time.Duration(row.SchedulingDelaySeconds * float64(time.Second)),
		TraceParent:     traceParent,
	}, nil
}
