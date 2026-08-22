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

	applyMigrations(t, ctx, db, migrationDirectory)
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
	verifyVersion(t, ctx, db, 2)
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

	for _, table := range []string{"jobs", "job_attempts"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %q does not exist", table)
		}
	}

	const jobID = "00000000-0000-0000-0000-000000000001"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts, timeout_ms)
		VALUES ($1, 'test', '{}', 'queued', 3, 30000)
	`, jobID); err != nil {
		t.Fatalf("insert valid job: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO job_attempts (job_id, attempt_no, status, started_at)
		VALUES ($1, 1, 'running', now())
	`, jobID); err != nil {
		t.Fatalf("insert valid attempt: %v", err)
	}

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
		INSERT INTO job_attempts (job_id, attempt_no, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 0, 'running', now())
	`)
	expectConstraintError(t, ctx, db, "23514", `
		INSERT INTO job_attempts (job_id, attempt_no, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 2, '', now())
	`)
	expectConstraintError(t, ctx, db, "23502", `
		INSERT INTO job_attempts (job_id, attempt_no, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 2, 'running', NULL)
	`)
	expectConstraintError(t, ctx, db, "23505", `
		INSERT INTO job_attempts (job_id, attempt_no, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 1, 'running', now())
	`)
	expectConstraintError(t, ctx, db, "23503", `
		INSERT INTO job_attempts (job_id, attempt_no, status, started_at)
		VALUES ('00000000-0000-0000-0000-000000000099', 1, 'running', now())
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

	for _, table := range []string{"jobs", "job_attempts"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check dropped table %q: %v", table, err)
		}
		if exists {
			t.Fatalf("table %q still exists after rollback", table)
		}
	}
}
