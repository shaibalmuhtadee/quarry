package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	postgresdb "github.com/shaibalmuhtadee/quarry/internal/store/postgres/generated"
)

type JobStore struct {
	queries *postgresdb.Queries
}

func NewJobStore(pool *pgxpool.Pool) *JobStore {
	return &JobStore{queries: postgresdb.New(pool)}
}

func (store *JobStore) CreateJob(ctx context.Context, submission domain.JobSubmission) (domain.Job, error) {
	row, err := store.queries.CreateJob(ctx, postgresdb.CreateJobParams{
		ID:          submission.ID().UUID(),
		JobType:     submission.Type().String(),
		Payload:     submission.Payload().JSON(),
		MaxAttempts: submission.MaxAttempts(),
		TimeoutMs:   submission.Timeout().Milliseconds(),
	})
	if err != nil {
		return domain.Job{}, fmt.Errorf("create job: %w", err)
	}

	job, err := mapJob(jobRecord{
		id:           row.ID,
		jobType:      row.JobType,
		payload:      row.Payload,
		status:       row.Status,
		attemptCount: row.AttemptCount,
		maxAttempts:  row.MaxAttempts,
		timeoutMS:    row.TimeoutMs,
		createdAt:    row.CreatedAt,
		updatedAt:    row.UpdatedAt,
	})
	if err != nil {
		return domain.Job{}, fmt.Errorf("map created job: %w", err)
	}

	return job, nil
}

func (store *JobStore) GetJob(ctx context.Context, id domain.JobID) (domain.Job, error) {
	row, err := store.queries.GetJob(ctx, id.UUID())
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, domain.ErrJobNotFound
	}
	if err != nil {
		return domain.Job{}, fmt.Errorf("get job: %w", err)
	}

	job, err := mapJob(jobRecord{
		id:           row.ID,
		jobType:      row.JobType,
		payload:      row.Payload,
		status:       row.Status,
		attemptCount: row.AttemptCount,
		maxAttempts:  row.MaxAttempts,
		timeoutMS:    row.TimeoutMs,
		createdAt:    row.CreatedAt,
		updatedAt:    row.UpdatedAt,
	})
	if err != nil {
		return domain.Job{}, fmt.Errorf("map stored job: %w", err)
	}

	return job, nil
}

type jobRecord struct {
	id           uuid.UUID
	jobType      string
	payload      []byte
	status       string
	attemptCount int32
	maxAttempts  int32
	timeoutMS    int64
	createdAt    pgtype.Timestamptz
	updatedAt    pgtype.Timestamptz
}

func mapJob(record jobRecord) (domain.Job, error) {
	id, err := domain.ParseJobID(record.id.String())
	if err != nil {
		return domain.Job{}, err
	}
	jobType, err := domain.ParseJobType(record.jobType)
	if err != nil {
		return domain.Job{}, err
	}
	payload, err := domain.ParsePayload(record.payload)
	if err != nil {
		return domain.Job{}, err
	}
	status, err := domain.ParseJobStatus(record.status)
	if err != nil {
		return domain.Job{}, err
	}
	if !record.createdAt.Valid || !record.updatedAt.Valid {
		return domain.Job{}, errors.New("job timestamps are null")
	}

	return domain.Job{
		ID:           id,
		Type:         jobType,
		Payload:      payload,
		Status:       status,
		AttemptCount: record.attemptCount,
		MaxAttempts:  record.maxAttempts,
		Timeout:      time.Duration(record.timeoutMS) * time.Millisecond,
		CreatedAt:    record.createdAt.Time,
		UpdatedAt:    record.updatedAt.Time,
	}, nil
}
