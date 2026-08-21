# Milestone 0 execution plan

## Milestone goal

Create the repository skeleton and PostgreSQL persistence foundation. A fresh clone must be able to start PostgreSQL and run the repository's basic validation.

Milestone 0 does not include an HTTP server, dispatcher, worker, protobuf definitions, job execution, retries, leases, idempotency, cancellation, or observability.

## Approved decisions

- Use `github.com/shaibalmuhtadee/quarry` as the Go module path.
- Pin Go 1.27.0.
- Use `scripts/dev.ps1` as the common command interface instead of a Makefile.
- Pin Goose and sqlc as Go tool dependencies.
- Run PostgreSQL 18.6 through Docker Compose. Do not require a host PostgreSQL installation.
- Create only `cmd/api` in Milestone 0. Its only behavior will be a one-shot PostgreSQL connection check.
- Create a minimal `jobs` and `job_attempts` schema. Add later-milestone columns, indexes, the `workers` table, and the worker foreign key only when their behavior is implemented.
- Commit sqlc-generated code and verify generation consistency.

## Slice 1: repository foundation

Status: complete

### Goal

Initialize the Go module, pin development tools, add the common command script, and record project status.

### Expected files

- `go.mod`
- `go.sum`
- `.gitignore`
- `scripts/dev.ps1`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Go 1.27.0
- PowerShell 7
- network access to resolve the pinned Goose and sqlc modules

### Validation

- `go version`
- `pwsh ./scripts/dev.ps1 tools`
- `pwsh ./scripts/dev.ps1 test`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- initialize the Go module
- pin the Go version
- establish dependency management
- establish common commands
- add `docs/current-status.md`

### Validation result

- Go 1.27.0 reported the expected Windows AMD64 toolchain.
- Goose v3.27.1 and sqlc v1.31.1 ran through `scripts/dev.ps1`.
- The test command reported the expected absence of Go packages and exited successfully.
- The complete `check` command passed.
- PowerShell parsed `scripts/dev.ps1` without errors.
- `git diff --check` passed.

## Slice 2: local PostgreSQL

Status: not started

### Goal

Add one PostgreSQL Compose service with a health check and persistent local development storage.

### Expected files

- `compose.yaml`
- PostgreSQL commands in `scripts/dev.ps1`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- Docker Desktop with Linux containers
- PostgreSQL 18.6 Docker image

### Validation

- render the Compose configuration
- start PostgreSQL and wait for it to become healthy
- run `pg_isready` inside the container
- stop the Compose project without deleting its volume

### Milestone requirements satisfied

- create the PostgreSQL Compose service
- provide a reproducible development database
- prove that a fresh environment can start PostgreSQL

## Slice 3: initial schema and migrations

Status: not started

### Goal

Create reviewable Goose migrations for `jobs` and `job_attempts`, then prove the migrations can apply, roll back, and reapply against fresh PostgreSQL.

### Expected files

- `internal/store/postgres/migrations/00001_create_jobs_and_attempts.sql`
- migration integration tests
- migration commands in `scripts/dev.ps1`
- `go.mod`
- `go.sum`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 and 2
- Goose
- Testcontainers for Go
- pgx database-driver support

### Validation

- start a fresh PostgreSQL test container
- apply the migration
- verify both tables and their constraints
- roll back to version zero
- apply the migration again
- run the migration integration test without cached results

### Milestone requirements satisfied

- configure migration tooling
- create the initial job and attempt schema
- apply migrations from an empty database
- reproduce migrations on fresh PostgreSQL

## Slice 4: pgx, sqlc, and application connectivity

Status: not started

### Goal

Generate database code with sqlc and prove that `cmd/api` can connect to PostgreSQL through `pgxpool`.

### Expected files

- `sqlc.yaml`
- `internal/store/postgres/queries/health.sql`
- `internal/store/postgres/generated/`
- PostgreSQL connection code
- `cmd/api/main.go`
- generation and connection commands in `scripts/dev.ps1`
- `go.mod`
- `go.sum`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 3
- pgx/v5
- sqlc

### Validation

- generate sqlc output
- verify that regeneration produces no diff
- run `go vet ./...`
- run `go test ./...`
- build `cmd/api`
- run `cmd/api` against the Compose database

### Milestone requirements satisfied

- create the minimal command structure
- configure pgx and sqlc
- produce generated database code
- prove that the application can connect
- prove that generated code is current

## Slice 5: CI and developer instructions

Status: not started

### Goal

Make local and CI validation use the same commands, then document the Milestone 0 fresh-clone path.

### Expected files

- `.github/workflows/ci.yml`
- final Milestone 0 commands in `scripts/dev.ps1`
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 4
- GitHub Actions with Docker

### Validation

- run formatting checks
- run `go vet ./...`
- run unit tests
- run the migration integration test
- build all current commands
- verify sqlc generation consistency
- validate the Compose configuration
- run the Compose migration and connection smoke test

### Milestone requirements satisfied

- add basic CI
- provide reproducible common commands
- document fresh-clone startup and validation
- satisfy all Milestone 0 tests

## Milestone audit

Status: not started

After all slices pass, audit the repository against the original Milestone 0 requirements and definition of done in `docs/project-plan.md`. Check the diff for later-milestone work. If the audit passes, update `docs/current-status.md` and move this file to `docs/exec-plans/completed/milestone-0.md`.

## Decisions and deviations discovered during implementation

- The common command interface uses PowerShell instead of GNU Make because the development environment is Windows 11. This follows the project plan's allowance for a Makefile or equivalent command interface. GitHub-hosted Ubuntu runners include PowerShell.
- Slice 1 contains no Go packages. The `test` command reports that state and succeeds without running tests. It will run `go test ./...` after a later slice adds the first package.
