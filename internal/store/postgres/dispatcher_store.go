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
)

var (
	ErrWorkerNotRegistered        = errors.New("worker is not registered")
	ErrWorkerRegistrationConflict = errors.New("worker registration conflicts with the stored registration")
	ErrAttemptReportConflict      = errors.New("attempt report conflicts with the stored attempt")
)

type WorkerRegistration struct {
	ID          domain.WorkerID
	Hostname    string
	Version     string
	Concurrency int32
	StartedAt   time.Time
}

type AcquiredJob struct {
	ID            domain.JobID
	AttemptNumber domain.AttemptNumber
	Type          domain.JobType
	Payload       domain.Payload
	Timeout       time.Duration
}

type DispatcherStore struct {
	pool          *pgxpool.Pool
	leaseDuration time.Duration
}

func NewDispatcherStore(pool *pgxpool.Pool, leaseDuration time.Duration) *DispatcherStore {
	return &DispatcherStore{pool: pool, leaseDuration: leaseDuration}
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

func (store *DispatcherStore) ReportSuccess(
	ctx context.Context,
	workerID domain.WorkerID,
	jobID domain.JobID,
	attemptNumber domain.AttemptNumber,
	result domain.Result,
) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin successful attempt report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := postgresdb.New(tx)
	report, err := queries.LockAttemptReport(ctx, postgresdb.LockAttemptReportParams{
		ResultJson: result.JSON(),
		AttemptNo:  attemptNumber.Int32(),
		JobID:      jobID.UUID(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAttemptReportConflict
	}
	if err != nil {
		return fmt.Errorf("lock successful attempt report: %w", err)
	}
	if report.AttemptCount != attemptNumber.Int32() || report.WorkerID != workerID.UUID() {
		return ErrAttemptReportConflict
	}

	if report.JobStatus == string(domain.JobStatusSucceeded) {
		if report.AttemptStatus != string(domain.AttemptStatusSucceeded) || !report.ResultMatches {
			return ErrAttemptReportConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit repeated successful attempt report: %w", err)
		}
		return nil
	}
	if report.JobStatus != string(domain.JobStatusRunning) ||
		report.AttemptStatus != string(domain.AttemptStatusRunning) {
		return ErrAttemptReportConflict
	}

	finishedAt := pgtype.Timestamptz{
		Time:  time.Now().UTC(),
		Valid: true,
	}
	attemptRows, err := queries.FinishAttemptSuccess(ctx, postgresdb.FinishAttemptSuccessParams{
		FinishedAt: finishedAt,
		JobID:      jobID.UUID(),
		AttemptNo:  attemptNumber.Int32(),
		WorkerID:   workerID.UUID(),
	})
	if err != nil {
		return fmt.Errorf("finish successful attempt: %w", err)
	}
	if attemptRows != 1 {
		return errors.New("finish successful attempt updated an unexpected number of rows")
	}

	jobRows, err := queries.FinishJobSuccess(ctx, postgresdb.FinishJobSuccessParams{
		ResultJson: result.JSON(),
		FinishedAt: finishedAt,
		JobID:      jobID.UUID(),
		AttemptNo:  attemptNumber.Int32(),
		WorkerID: pgtype.UUID{
			Bytes: workerID.UUID(),
			Valid: true,
		},
	})
	if err != nil {
		return fmt.Errorf("finish successful job: %w", err)
	}
	if jobRows != 1 {
		return errors.New("finish successful job updated an unexpected number of rows")
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit successful attempt report: %w", err)
	}

	return nil
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

	return AcquiredJob{
		ID:            id,
		AttemptNumber: attemptNumber,
		Type:          jobType,
		Payload:       payload,
		Timeout:       time.Duration(row.TimeoutMs) * time.Millisecond,
	}, nil
}
