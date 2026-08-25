package postgres

import (
	"bytes"
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
	pool    *pgxpool.Pool
	queries *postgresdb.Queries
}

func NewJobStore(pool *pgxpool.Pool) *JobStore {
	return &JobStore{pool: pool, queries: postgresdb.New(pool)}
}

func (store *JobStore) SubmitJob(ctx context.Context, submission domain.JobSubmission) (domain.JobSubmissionResult, error) {
	var idempotencyKey pgtype.Text
	requestHash, idempotent := submission.RequestHash()
	if key, ok := submission.IdempotencyKey(); ok {
		idempotencyKey = pgtype.Text{String: key.String(), Valid: true}
	}
	row, err := store.queries.SubmitJob(ctx, postgresdb.SubmitJobParams{
		ID:             submission.ID().UUID(),
		JobType:        submission.Type().String(),
		Payload:        submission.Payload().JSON(),
		MaxAttempts:    submission.MaxAttempts(),
		TimeoutMs:      submission.Timeout().Milliseconds(),
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if !idempotent {
			return domain.JobSubmissionResult{}, errors.New("non-idempotent submission unexpectedly conflicted")
		}
		existing, lookupErr := store.queries.GetJobByIdempotencyKey(ctx, postgresdb.GetJobByIdempotencyKeyParams{
			JobType:        submission.Type().String(),
			IdempotencyKey: idempotencyKey,
		})
		if lookupErr != nil {
			return domain.JobSubmissionResult{}, fmt.Errorf("get idempotent submission: %w", lookupErr)
		}
		if !bytes.Equal(existing.RequestHash, requestHash) {
			return domain.JobSubmissionResult{}, domain.ErrIdempotencyConflict
		}
		job, mapErr := mapJob(jobRecord{
			id:                existing.ID,
			jobType:           existing.JobType,
			payload:           existing.Payload,
			result:            existing.Result,
			status:            existing.Status,
			attemptCount:      existing.AttemptCount,
			maxAttempts:       existing.MaxAttempts,
			timeoutMS:         existing.TimeoutMs,
			createdAt:         existing.CreatedAt,
			updatedAt:         existing.UpdatedAt,
			finishedAt:        existing.FinishedAt,
			cancelRequestedAt: existing.CancelRequestedAt,
		})
		if mapErr != nil {
			return domain.JobSubmissionResult{}, fmt.Errorf("map deduplicated job: %w", mapErr)
		}
		return domain.JobSubmissionResult{Job: job, Deduplicated: true}, nil
	}
	if err != nil {
		return domain.JobSubmissionResult{}, fmt.Errorf("submit job: %w", err)
	}

	job, err := mapJob(jobRecord{
		id:                row.ID,
		jobType:           row.JobType,
		payload:           row.Payload,
		result:            row.Result,
		status:            row.Status,
		attemptCount:      row.AttemptCount,
		maxAttempts:       row.MaxAttempts,
		timeoutMS:         row.TimeoutMs,
		createdAt:         row.CreatedAt,
		updatedAt:         row.UpdatedAt,
		finishedAt:        row.FinishedAt,
		cancelRequestedAt: row.CancelRequestedAt,
	})
	if err != nil {
		return domain.JobSubmissionResult{}, fmt.Errorf("map submitted job: %w", err)
	}

	return domain.JobSubmissionResult{Job: job}, nil
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
		id:                   row.ID,
		jobType:              row.JobType,
		payload:              row.Payload,
		result:               row.Result,
		status:               row.Status,
		attemptCount:         row.AttemptCount,
		maxAttempts:          row.MaxAttempts,
		timeoutMS:            row.TimeoutMs,
		createdAt:            row.CreatedAt,
		updatedAt:            row.UpdatedAt,
		finishedAt:           row.FinishedAt,
		cancelRequestedAt:    row.CancelRequestedAt,
		latestFailureCode:    row.LatestFailureErrorCode,
		latestFailureMessage: row.LatestFailureErrorMessage,
	})
	if err != nil {
		return domain.Job{}, fmt.Errorf("map stored job: %w", err)
	}

	return job, nil
}

func (store *JobStore) RequestCancellation(ctx context.Context, id domain.JobID) (domain.Job, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Job{}, fmt.Errorf("begin job cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := postgresdb.New(tx)
	state, err := queries.LockJobForCancellation(ctx, id.UUID())
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, domain.ErrJobNotFound
	}
	if err != nil {
		return domain.Job{}, fmt.Errorf("lock job for cancellation: %w", err)
	}

	transition, err := domain.PlanJobCancellation(domain.JobStatus(state.Status), state.CancelRequestedAt.Valid)
	if err != nil {
		return domain.Job{}, err
	}
	if transition != domain.JobCancellationNoChange {
		transitionTime, err := queries.GetTransitionTime(ctx)
		if err != nil {
			return domain.Job{}, fmt.Errorf("read cancellation transition time: %w", err)
		}
		if !transitionTime.Valid {
			return domain.Job{}, errors.New("cancellation transition time is null")
		}

		var changedRows int64
		switch transition {
		case domain.JobCancellationFinish:
			changedRows, err = queries.CancelPendingJob(ctx, postgresdb.CancelPendingJobParams{
				TransitionTime: transitionTime,
				JobID:          id.UUID(),
			})
		case domain.JobCancellationRequest:
			changedRows, err = queries.RequestRunningJobCancellation(ctx, postgresdb.RequestRunningJobCancellationParams{
				TransitionTime: transitionTime,
				JobID:          id.UUID(),
			})
		default:
			return domain.Job{}, errors.New("invalid job cancellation transition")
		}
		if err != nil {
			return domain.Job{}, fmt.Errorf("request job cancellation: %w", err)
		}
		if changedRows != 1 {
			return domain.Job{}, errors.New("job cancellation updated an unexpected number of rows")
		}
	}

	row, err := queries.GetJob(ctx, id.UUID())
	if err != nil {
		return domain.Job{}, fmt.Errorf("get cancelled job: %w", err)
	}
	job, err := mapJob(jobRecord{
		id: row.ID, jobType: row.JobType, payload: row.Payload, result: row.Result,
		status: row.Status, attemptCount: row.AttemptCount, maxAttempts: row.MaxAttempts,
		timeoutMS: row.TimeoutMs, createdAt: row.CreatedAt, updatedAt: row.UpdatedAt,
		finishedAt: row.FinishedAt, cancelRequestedAt: row.CancelRequestedAt,
		latestFailureCode: row.LatestFailureErrorCode, latestFailureMessage: row.LatestFailureErrorMessage,
	})
	if err != nil {
		return domain.Job{}, fmt.Errorf("map cancelled job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, fmt.Errorf("commit job cancellation: %w", err)
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
	id                   uuid.UUID
	jobType              string
	payload              []byte
	result               []byte
	status               string
	attemptCount         int32
	maxAttempts          int32
	timeoutMS            int64
	createdAt            pgtype.Timestamptz
	updatedAt            pgtype.Timestamptz
	finishedAt           pgtype.Timestamptz
	cancelRequestedAt    pgtype.Timestamptz
	latestFailureCode    pgtype.Text
	latestFailureMessage pgtype.Text
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
	var cancelRequestedAt *time.Time
	if record.cancelRequestedAt.Valid {
		cancelRequestedAt = &record.cancelRequestedAt.Time
	}
	var latestFailure *domain.AttemptFailure
	if record.latestFailureCode.Valid != record.latestFailureMessage.Valid {
		return domain.Job{}, errors.New("job has incomplete latest failure details")
	}
	if record.latestFailureCode.Valid {
		parsedFailure, err := domain.NewAttemptFailure(
			record.latestFailureCode.String,
			record.latestFailureMessage.String,
		)
		if err != nil {
			return domain.Job{}, err
		}
		latestFailure = &parsedFailure
	}

	return domain.Job{
		ID:                id,
		Type:              jobType,
		Payload:           payload,
		Result:            result,
		LatestFailure:     latestFailure,
		Status:            status,
		AttemptCount:      record.attemptCount,
		MaxAttempts:       record.maxAttempts,
		Timeout:           time.Duration(record.timeoutMS) * time.Millisecond,
		CreatedAt:         record.createdAt.Time,
		UpdatedAt:         record.updatedAt.Time,
		FinishedAt:        finishedAt,
		CancelRequestedAt: cancelRequestedAt,
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
