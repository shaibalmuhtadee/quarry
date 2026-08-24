package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const jobStorePostgresImage = "postgres:18.6"

func TestJobStorePersistsJobAcrossPoolRestart(t *testing.T) {
	connectionString := startMigratedPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	jobType, err := domain.ParseJobType("email.send")
	if err != nil {
		t.Fatalf("parse job type: %v", err)
	}
	payload, err := domain.ParsePayload(json.RawMessage(`{"recipient":"user@example.com","priority":2}`))
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	submission, err := domain.NewJobSubmission(jobType, payload, 5, 30*time.Second)
	if err != nil {
		t.Fatalf("create job submission: %v", err)
	}

	var created domain.Job
	func() {
		firstPool, err := postgres.NewPool(ctx, connectionString)
		if err != nil {
			t.Fatalf("create first PostgreSQL pool: %v", err)
		}
		defer firstPool.Close()
		if err := firstPool.Ping(ctx); err != nil {
			t.Fatalf("ping first PostgreSQL pool: %v", err)
		}

		created, err = postgres.NewJobStore(firstPool).CreateJob(ctx, submission)
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		assertStoredJob(t, created, submission)
	}()

	secondPool, err := postgres.NewPool(ctx, connectionString)
	if err != nil {
		t.Fatalf("create second PostgreSQL pool: %v", err)
	}
	t.Cleanup(secondPool.Close)
	if err := secondPool.Ping(ctx); err != nil {
		t.Fatalf("ping second PostgreSQL pool: %v", err)
	}
	store := postgres.NewJobStore(secondPool)

	retrieved, err := store.GetJob(ctx, submission.ID())
	if err != nil {
		t.Fatalf("get job after pool restart: %v", err)
	}
	assertStoredJob(t, retrieved, submission)
	if !retrieved.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("retrieved created timestamp = %s, want %s", retrieved.CreatedAt, created.CreatedAt)
	}
	if !retrieved.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("retrieved updated timestamp = %s, want %s", retrieved.UpdatedAt, created.UpdatedAt)
	}

	_, err = store.GetJob(ctx, domain.NewJobID())
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("get missing job error = %v, want ErrJobNotFound", err)
	}
}

func TestJobStoreListsAttemptsInAttemptNumberOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	dispatcherStore := postgres.NewDispatcherStore(pool)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "history.test", `{}`)

	empty, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list empty attempt history: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty attempt history = %#v, want non-nil empty slice", empty)
	}
	_, err = jobStore.ListJobAttempts(ctx, domain.NewJobID())
	if !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("missing job attempt history error = %v, want ErrJobNotFound", err)
	}

	firstStartedAt := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	secondStartedAt := firstStartedAt.Add(time.Minute)
	for _, attempt := range []struct {
		number    int32
		startedAt time.Time
	}{
		{number: 2, startedAt: secondStartedAt},
		{number: 1, startedAt: firstStartedAt},
	} {
		_, err := pool.Exec(ctx, `
			INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at, finished_at)
			VALUES ($1, $2, $3, 'succeeded', $4, $4)
		`, job.ID.UUID(), attempt.number, worker.UUID(), attempt.startedAt)
		if err != nil {
			t.Fatalf("insert attempt %d: %v", attempt.number, err)
		}
	}

	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list stored attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("stored attempt count = %d, want 2", len(attempts))
	}
	if attempts[0].Number.Int32() != 1 || attempts[1].Number.Int32() != 2 {
		t.Fatalf("stored attempt order = [%d, %d], want [1, 2]", attempts[0].Number.Int32(), attempts[1].Number.Int32())
	}
}

func startMigratedPostgres(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := postgrescontainer.Run(
		ctx,
		jobStorePostgresImage,
		postgrescontainer.WithDatabase("quarry"),
		postgrescontainer.WithUsername("quarry"),
		postgrescontainer.WithPassword("quarry"),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL container: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get PostgreSQL connection string: %v", err)
	}
	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		t.Fatalf("open PostgreSQL connection: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close migration connection: %v", err)
		}
	}()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set Goose dialect: %v", err)
	}
	if err := goose.UpContext(ctx, db, migrationDirectory(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return connectionString
}

func migrationDirectory(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate job store test file")
	}

	return filepath.Join(filepath.Dir(filename), "migrations")
}

func assertStoredJob(t *testing.T, job domain.Job, submission domain.JobSubmission) {
	t.Helper()

	if job.ID != submission.ID() {
		t.Fatalf("job ID = %s, want %s", job.ID, submission.ID())
	}
	if job.Type != submission.Type() {
		t.Fatalf("job type = %s, want %s", job.Type, submission.Type())
	}
	assertJSONEqual(t, job.Payload.JSON(), submission.Payload().JSON())
	if job.Status != domain.JobStatusQueued {
		t.Fatalf("job status = %q, want %q", job.Status, domain.JobStatusQueued)
	}
	if job.AttemptCount != 0 {
		t.Fatalf("job attempt count = %d, want 0", job.AttemptCount)
	}
	if job.MaxAttempts != submission.MaxAttempts() {
		t.Fatalf("job maximum attempts = %d, want %d", job.MaxAttempts, submission.MaxAttempts())
	}
	if job.Timeout != submission.Timeout() {
		t.Fatalf("job timeout = %s, want %s", job.Timeout, submission.Timeout())
	}
	if job.CreatedAt.IsZero() {
		t.Fatal("job created timestamp is zero")
	}
	if job.UpdatedAt.IsZero() {
		t.Fatal("job updated timestamp is zero")
	}
	if job.Result != nil {
		t.Fatalf("queued job result = %s, want nil", job.Result.JSON())
	}
	if job.FinishedAt != nil {
		t.Fatalf("queued job finish timestamp = %s, want nil", *job.FinishedAt)
	}
}

func assertJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()

	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode stored payload: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode submitted payload: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("stored payload = %s, want JSON-equivalent to %s", got, want)
	}
}
