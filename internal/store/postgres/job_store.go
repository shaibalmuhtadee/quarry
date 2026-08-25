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
		result:       row.Result,
		status:       row.Status,
		attemptCount: row.AttemptCount,
		maxAttempts:  row.MaxAttempts,
		timeoutMS:    row.TimeoutMs,
		createdAt:    row.CreatedAt,
		updatedAt:    row.UpdatedAt,
		finishedAt:   row.FinishedAt,
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
		result:       row.Result,
		status:       row.Status,
		attemptCount: row.AttemptCount,
		maxAttempts:  row.MaxAttempts,
		timeoutMS:    row.TimeoutMs,
		createdAt:    row.CreatedAt,
		updatedAt:    row.UpdatedAt,
		finishedAt:   row.FinishedAt,
	})
	if err != nil {
		return domain.Job{}, fmt.Errorf("map stored job: %w", err)
	}

	return job, nil
}

func (store *JobStore) ListJobAttempts(ctx context.Context, id domain.JobID) ([]domain.Attempt, error) {
	rows, err := store.queries.GetJobAttempts(ctx, id.UUID())
	if err != nil {
		return nil, fmt.Errorf("get job attempts: %w", err)
	}
	if len(rows) == 0 {
		return nil, domain.ErrJobNotFound
	}
	if !rows[0].AttemptNo.Valid {
		return []domain.Attempt{}, nil
	}

	attempts := make([]domain.Attempt, 0, len(rows))
	for _, row := range rows {
		attempt, err := mapAttempt(row)
		if err != nil {
			return nil, fmt.Errorf("map stored attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}

	return attempts, nil
}

type jobRecord struct {
	id           uuid.UUID
	jobType      string
	payload      []byte
	result       []byte
	status       string
	attemptCount int32
	maxAttempts  int32
	timeoutMS    int64
	createdAt    pgtype.Timestamptz
	updatedAt    pgtype.Timestamptz
	finishedAt   pgtype.Timestamptz
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
	var result *domain.Result
	if record.result != nil {
		parsedResult, err := domain.ParseResult(record.result)
		if err != nil {
			return domain.Job{}, err
		}
		result = &parsedResult
	}
	var finishedAt *time.Time
	if record.finishedAt.Valid {
		finishedAt = &record.finishedAt.Time
	}

	return domain.Job{
		ID:           id,
		Type:         jobType,
		Payload:      payload,
		Result:       result,
		Status:       status,
		AttemptCount: record.attemptCount,
		MaxAttempts:  record.maxAttempts,
		Timeout:      time.Duration(record.timeoutMS) * time.Millisecond,
		CreatedAt:    record.createdAt.Time,
		UpdatedAt:    record.updatedAt.Time,
		FinishedAt:   finishedAt,
	}, nil
}

func mapAttempt(row postgresdb.GetJobAttemptsRow) (domain.Attempt, error) {
	jobID, err := domain.ParseJobID(row.JobID.String())
	if err != nil {
		return domain.Attempt{}, err
	}
	if !row.AttemptNo.Valid || !row.WorkerID.Valid || !row.Status.Valid || !row.StartedAt.Valid {
		return domain.Attempt{}, errors.New("attempt has null required fields")
	}
	number, err := domain.NewAttemptNumber(row.AttemptNo.Int32)
	if err != nil {
		return domain.Attempt{}, err
	}
	workerID, err := domain.ParseWorkerID(uuid.UUID(row.WorkerID.Bytes).String())
	if err != nil {
		return domain.Attempt{}, err
	}
	status, err := domain.ParseAttemptStatus(row.Status.String)
	if err != nil {
		return domain.Attempt{}, err
	}
	var finishedAt *time.Time
	if row.FinishedAt.Valid {
		finishedAt = &row.FinishedAt.Time
	}
	var failure *domain.AttemptFailure
	if row.ErrorCode.Valid != row.ErrorMessage.Valid {
		return domain.Attempt{}, errors.New("attempt has incomplete failure details")
	}
	if row.ErrorCode.Valid {
		parsedFailure, err := domain.NewAttemptFailure(row.ErrorCode.String, row.ErrorMessage.String)
		if err != nil {
			return domain.Attempt{}, err
		}
		failure = &parsedFailure
	}

	return domain.Attempt{
		JobID:      jobID,
		Number:     number,
		WorkerID:   workerID,
		Status:     status,
		Failure:    failure,
		StartedAt:  row.StartedAt.Time,
		FinishedAt: finishedAt,
	}, nil
}
