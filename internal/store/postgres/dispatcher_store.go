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
	pool *pgxpool.Pool
}

func NewDispatcherStore(pool *pgxpool.Pool) *DispatcherStore {
	return &DispatcherStore{pool: pool}
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
