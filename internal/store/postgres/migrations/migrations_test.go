package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const postgresImage = "postgres:18.6"

func TestMigrationsApplyRollbackAndReapply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := postgrescontainer.Run(
		ctx,
		postgresImage,
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
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close PostgreSQL connection: %v", err)
		}
	})

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	migrationDirectory := migrationDirectory(t)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set Goose dialect: %v", err)
	}

	if err := goose.UpToContext(ctx, db, migrationDirectory, 3); err != nil {
		t.Fatalf("apply migrations through version 3: %v", err)
	}
	verifyVersion(t, ctx, db, 3)
	seedPreLeaseRunningJob(t, ctx, db)
	applyMigrations(t, ctx, db, migrationDirectory)
	verifyLeaseMigrationBackfill(t, ctx, db)
	verifySchema(t, ctx, db)

	if err := goose.DownToContext(ctx, db, migrationDirectory, 0); err != nil {
		t.Fatalf("roll migrations back to version zero: %v", err)
	}
	verifyVersion(t, ctx, db, 0)
	verifyTablesAbsent(t, ctx, db)

	applyMigrations(t, ctx, db, migrationDirectory)
	verifySchema(t, ctx, db)
}

func migrationDirectory(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration test file")
	}

	return filepath.Dir(filename)
}

func applyMigrations(t *testing.T, ctx context.Context, db *sql.DB, directory string) {
	t.Helper()

	if err := goose.UpContext(ctx, db, directory); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	verifyVersion(t, ctx, db, 4)
}

func seedPreLeaseRunningJob(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO workers (id, hostname, version, concurrency, started_at)
		VALUES ('00000000-0000-0000-0000-000000000020', 'pre-lease-worker', 'test', 1, now());

		INSERT INTO jobs (
			id, job_type, payload, status, attempt_count, max_attempts, timeout_ms, current_worker_id
		)
		VALUES (
			'00000000-0000-0000-0000-000000000021', 'pre-lease-job', '{}', 'running', 1, 3, 30000,
			'00000000-0000-0000-0000-000000000020'
		);

		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES (
			'00000000-0000-0000-0000-000000000021', 1,
			'00000000-0000-0000-0000-000000000020', 'running', now()
		);
	`); err != nil {
		t.Fatalf("seed pre-lease running job: %v", err)
	}
}

func verifyLeaseMigrationBackfill(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	var workerState string
	var hasLastSeen, leaseExpired bool
	if err := db.QueryRowContext(ctx, `
		SELECT workers.state,
		       workers.last_seen_at IS NOT NULL,
		       jobs.lease_expires_at <= statement_timestamp()
		FROM workers
		JOIN jobs ON jobs.current_worker_id = workers.id
		WHERE jobs.id = '00000000-0000-0000-0000-000000000021'
	`).Scan(&workerState, &hasLastSeen, &leaseExpired); err != nil {
		t.Fatalf("read lease migration backfill: %v", err)
	}
	if workerState != "active" || !hasLastSeen || !leaseExpired {
		t.Fatalf("lease migration backfill = state %q, last_seen %t, expired %t", workerState, hasLastSeen, leaseExpired)
	}

	var indexExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('jobs_expired_lease_idx') IS NOT NULL`).Scan(&indexExists); err != nil {
		t.Fatalf("check expired lease index: %v", err)
	}
	if !indexExists {
		t.Fatal("expired lease index does not exist")
	}
}

func verifyVersion(t *testing.T, ctx context.Context, db *sql.DB, want int64) {
	t.Helper()

	got, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if got != want {
		t.Fatalf("migration version = %d, want %d", got, want)
	}
}

func verifySchema(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	for _, table := range []string{"jobs", "job_attempts", "workers"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %q does not exist", table)
		}
	}

	const jobID = "00000000-0000-0000-0000-000000000001"
	const workerID = "00000000-0000-0000-0000-000000000010"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workers (id, hostname, version, concurrency, started_at)
		VALUES ($1, 'worker-a', 'test', 4, now())
	`, workerID); err != nil {
		t.Fatalf("insert valid worker: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ($1, 'test', '{}', 'queued', 3, 30000)
	`, jobID); err != nil {
		t.Fatalf("insert valid job: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES ($1, 1, $2, 'running', now())
	`, jobID, workerID); err != nil {
		t.Fatalf("insert valid attempt: %v", err)
	}

	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO workers (id, hostname, version, concurrency, started_at)
		VALUES ('00000000-0000-0000-0000-000000000011', '', 'test', 4, now())
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO workers (id, hostname, version, concurrency, started_at)
		VALUES ('00000000-0000-0000-0000-000000000012', 'worker-b', '', 4, now())
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO workers (id, hostname, version, concurrency, started_at)
		VALUES ('00000000-0000-0000-0000-000000000013', 'worker-c', 'test', 0, now())
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO workers (id, hostname, version, concurrency, started_at, state)
		VALUES ('00000000-0000-0000-0000-000000000014', 'worker-d', 'test', 1, now(), 'unknown')
	`)

	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000002', 'test', '{}', 'unknown', 3, 30000)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000003', '', '{}', 'queued', 3, 30000)
	`)
	expectConstraintError(t, ctx, db, "23502", `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000004', 'test', NULL, 'queued', 3, 30000)
	`)
	expectConstraintError(t, ctx, db, "23505", `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000001', 'duplicate', '{}', 'queued', 3, 30000)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, attempt_count, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000005', 'test', '{}', 'queued', -1, 3, 30000)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000006', 'test', '{}', 'queued', 0, 30000)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, attempt_count, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000007', 'test', '{}', 'queued', 4, 3, 30000)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000008', 'test', '{}', 'queued', 3, 0)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000009', 'test', '{}', 'queued', 3, 9223372036855)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (id, job_type, payload, status, attempt_count, max_attempts, timeout_ms)
		VALUES ('00000000-0000-0000-0000-000000000015', 'test', '{}', 'running', 1, 3, 30000)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO jobs (
			id, job_type, payload, status, max_attempts, timeout_ms, current_worker_id, lease_expires_at
		)
		VALUES (
			'00000000-0000-0000-0000-000000000016', 'test', '{}', 'queued', 3, 30000,
			'00000000-0000-0000-0000-000000000010', now()
		)
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 0, '00000000-0000-0000-0000-000000000010', 'running', now())
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 2, '00000000-0000-0000-0000-000000000010', '', now())
	`)
	expectConstraintError(t, ctx, db, "23502", `
		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 2, '00000000-0000-0000-0000-000000000010', 'running', NULL)
	`)
	expectConstraintError(t, ctx, db, "23505", `
		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 1, '00000000-0000-0000-0000-000000000010', 'running', now())
	`)
	expectConstraintError(t, ctx, db, "23503", `
		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000099', 1, '00000000-0000-0000-0000-000000000010', 'running', now())
	`)
	expectConstraintError(t, ctx, db, "23503", `
		INSERT INTO job_attempts (job_id, attempt_no, worker_id, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 2, '00000000-0000-0000-0000-000000000099', 'running', now())
	`)
}

func expectConstraintError(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantCode string,
	query string,
) {
	t.Helper()

	_, err := db.ExecContext(ctx, query)
	if err == nil {
		t.Fatalf("query unexpectedly satisfied a schema constraint: %s", query)
	}

	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("query returned a non-PostgreSQL error: %v", err)
	}
	if postgresError.Code != wantCode {
		t.Fatalf("constraint SQLSTATE = %s, want %s: %v", postgresError.Code, wantCode, err)
	}
}

func verifyTablesAbsent(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	for _, table := range []string{"jobs", "job_attempts", "workers"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check dropped table %q: %v", table, err)
		}
		if exists {
			t.Fatalf("table %q still exists after rollback", table)
		}
	}
}
