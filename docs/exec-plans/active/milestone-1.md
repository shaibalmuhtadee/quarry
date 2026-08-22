# Milestone 1 execution plan

## Milestone goal

Build a durable HTTP control plane that accepts queued jobs and retrieves them after an API restart. Milestone 1 has no job execution capability.

Milestone 1 does not include dispatching, workers, gRPC, job acquisition, attempts, retries, leases, cancellation, idempotency, metrics, tracing, or execution logic.

## Approved decisions

- Validate job types by syntax during Milestone 1. A job type is 1 to 128 characters, starts with a lowercase ASCII letter, and contains lowercase ASCII letters, digits, or non-consecutive `.`, `_`, and `-` separators. Milestone 1 does not validate worker availability because no workers exist.
- Generate job UUIDs in the application with `github.com/google/uuid`. Domain code must not expose PostgreSQL UUID types.
- Require one valid JSON value as the payload. The HTTP API will reject unknown request fields, trailing JSON values, and bodies larger than 1 MiB.
- Default an omitted `max_attempts` to 3. Reject explicit zero and negative values. Require a positive `timeout_ms` that fits Go's `time.Duration`; the project plan does not define a timeout default or a narrower operational maximum.
- Add only `attempt_count`, `max_attempts`, and `timeout_ms` to the Milestone 1 schema. Defer `available_at`, results, workers, leases, cancellation, idempotency, tracing, and execution indexes until their behavior exists.
- Omit the submitted payload from `GET /v1/jobs/{id}` because the project plan's response fields do not include it.
- Use `net/http` without a routing dependency. Return `201 Created` and a `Location` header for submission, `400` for invalid input, `404` for a valid missing job ID, and generic `500` responses for internal failures.
- Use `/healthz` for process liveness and `/readyz` for PostgreSQL readiness. Defer `/metrics` until Milestone 5.
- Listen on `:8080` by default and allow `QUARRY_HTTP_ADDR` to override it.

## Slice 1: queued-job domain contract

Status: complete

### Goal

Define the Milestone 1 `Job` model, job identity, queued state, submission values, and validation rules without adding HTTP or database behavior.

### Expected files and areas

- `internal/domain/job.go`
- `internal/domain/job_test.go`
- `go.mod`
- `go.sum` only if the direct UUID dependency changes its contents
- `docs/current-status.md`
- this execution plan

### Dependencies

- Milestone 0
- `github.com/google/uuid`, promoted from an indirect dependency to a direct dependency

### Important decisions

- Use value types for job IDs, job types, and validated JSON payloads so transport and persistence code do not pass unchecked strings or mutable request bytes through the system.
- Keep submission validation in the domain. HTTP parsing and the 1 MiB body limit remain boundary concerns for Slice 3.
- Define the six fixed V1 job status values without adding state-transition behavior.
- Generate the stable job ID when the domain creates a submission. PostgreSQL will persist that identity in Slice 2.
- Require a positive maximum-attempt count and timeout in the domain. The HTTP layer will apply the approved default for an omitted `max_attempts` in Slice 3.

### Validation required

- `go test -count=1 ./internal/domain`
- `pwsh ./scripts/dev.ps1 test`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- core `Job` domain model
- job-type validation mechanism
- domain coverage for invalid job types and malformed payloads

### Decisions and deviations discovered during implementation

- `JobID`, `JobType`, and `Payload` use unexported representations with constructors or parsers. `Payload` copies input and output bytes so callers cannot mutate validated JSON after parsing.
- `JobSubmission` generates its UUID v4 and rejects zero-value job types, payloads, maximum attempts, and timeouts. Slice 3 remains responsible for applying the approved HTTP default for omitted `max_attempts`.
- `github.com/google/uuid` moved from an indirect dependency to a direct dependency without changing `go.sum` checksums.
- The expected file list expanded to include `.gitattributes`. Changing `go.mod` exposed that the Windows checkout converted committed LF Go and module files to CRLF, which caused `gofmt` and `go mod tidy -diff` to reject otherwise unchanged files. The repository now keeps `*.go`, `go.mod`, and `go.sum` as LF. This does not change application architecture or Milestone 1 scope.

### Validation result

- `go test -count=1 ./internal/domain` passed.
- `pwsh ./scripts/dev.ps1 test` passed all Go packages, including the PostgreSQL migration integration test.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned-tool checks, sqlc consistency, vet, uncached tests, builds, Compose rendering, the fresh PostgreSQL migration test, and the Compose connection smoke test.
- `git diff --check` passed.

## Slice 2: durable job persistence

Status: complete

### Goal

Extend the schema only as far as Milestone 1 needs, then add visible SQL and a concrete PostgreSQL store for queued-job creation and lookup.

### Expected files and areas

- a new migration under `internal/store/postgres/migrations/`
- `internal/store/postgres/queries/jobs.sql`
- `internal/store/postgres/generated/`
- PostgreSQL job mapping and integration tests
- `sqlc.yaml` if UUID overrides are required
- `go.mod` and `go.sum` only if existing dependencies change
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- existing Goose, pgx, sqlc, and Testcontainers dependencies

### Important decisions

- Add only `attempt_count`, `max_attempts`, and `timeout_ms` with database constraints.
- Keep new jobs in `queued`. Persist retry and timeout configuration without implementing their execution behavior.
- Keep SQL visible and map generated database values into domain values inside the PostgreSQL package.
- Expose concrete create and get operations. Do not add a generic repository layer.
- Map `pgx.ErrNoRows` to a stable domain-level not-found error.

### Validation required

- apply, roll back, and reapply migrations against fresh PostgreSQL
- create and retrieve a queued job through the PostgreSQL store
- verify JSON, ID, status, attempt count, limits, and timestamps round-trip
- close and reopen the pool before retrieving the same job
- verify missing-job behavior
- `pwsh ./scripts/dev.ps1 generate`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 migration-test`
- `go test -count=1 ./internal/store/postgres/...`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- job persistence
- job-submission storage
- job-lookup storage
- database integration coverage for submission and read behavior

### Decisions and deviations discovered during implementation

- Migration `00002_add_job_submission_fields.sql` adds only `attempt_count`, `max_attempts`, and `timeout_ms`. Database checks keep attempt counts nonnegative and within `max_attempts`, require positive limits, and cap `timeout_ms` at the largest millisecond value that converts safely to `time.Duration`.
- `JobSubmission` now requires a whole positive millisecond timeout. This preserves the approved `timeout_ms` contract and prevents persistence from truncating a valid domain value.
- sqlc maps PostgreSQL UUIDs to `github.com/google/uuid.UUID`. `JobStore` maps generated rows into domain values and returns `domain.ErrJobNotFound` for `pgx.ErrNoRows`.
- The store integration test closes its first pool before opening a second pool against the same PostgreSQL instance. It verifies that all job fields survive the pool restart and that missing IDs return the domain not-found error.
- No Milestone 1 architecture deviation was required.

### Validation result

- `pwsh ./scripts/dev.ps1 generate` and `pwsh ./scripts/dev.ps1 generate-check` passed.
- `pwsh ./scripts/dev.ps1 migration-test` passed migration apply, rollback to version zero, and reapplication against PostgreSQL 18.6.
- `go test -count=1 ./internal/store/postgres/...` passed the store and migration integration tests against PostgreSQL 18.6.
- `go test -count=1 ./internal/domain` passed.
- The first full `pwsh ./scripts/dev.ps1 check` attempt hit a transient Docker provider discovery failure in one Testcontainers package while the other package passed. A standalone full Go test and five consecutive PostgreSQL package test runs then passed.
- The final `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned-tool checks, sqlc consistency, vet, uncached tests, builds, Compose rendering, migrations through version 2, and the Compose connection smoke test.
- `git diff --check` passed.

## Slice 3: public job HTTP handlers

Status: not started

### Goal

Implement `POST /v1/jobs` and `GET /v1/jobs/{id}` with `net/http`, strict boundary validation, stable JSON responses, and handler tests.

### Expected files and areas

- `internal/api/`
- HTTP handler tests that use `httptest`
- small request, response, and error transport types
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 and 2
- standard-library `net/http` and `encoding/json`

### Important decisions

- Define the narrow job-store interface in `internal/api`, where the interface is consumed.
- Reject unknown request fields, trailing JSON values, missing payloads, invalid job types, invalid limits, and bodies larger than 1 MiB.
- Apply `max_attempts = 3` only when the field is omitted. Reject an explicit zero.
- Return `201 Created` plus `Location` for submission.
- Return `400` for malformed input, `404` for a valid missing UUID, and a generic `500` for internal failures.
- Return stable machine-readable JSON error codes.
- Omit the stored payload from the job lookup response.

### Validation required

- handler tests for valid submission, invalid type, malformed or missing payload, invalid limits, malformed job ID, missing job, successful lookup, headers, and status codes
- `go test -count=1 ./internal/api`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- `POST /v1/jobs`
- `GET /v1/jobs/{id}`
- valid-submission test
- invalid-type test
- malformed-payload test
- missing-job test
- usable HTTP and JSON contract

### Decisions and deviations discovered during implementation

- None yet.

## Slice 4: runnable API and operational endpoints

Status: not started

### Goal

Replace the one-shot connection check with the HTTP service, wire PostgreSQL into the handlers, add structured logs, and provide health and readiness endpoints.

### Expected files and areas

- `cmd/api/main.go`
- `internal/api/` operational handlers and logging
- API runtime tests
- `scripts/dev.ps1`
- a small configuration package only if `main.go` cannot own the settings clearly
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 3
- standard-library `log/slog`, HTTP server, and signal support
- existing PostgreSQL pool and health query

### Important decisions

- `/healthz` reports process liveness without a database query.
- `/readyz` checks PostgreSQL with a short timeout and returns `503` when the database is unavailable.
- Defer `/metrics` until Milestone 5.
- Use `:8080` by default and allow `QUARRY_HTTP_ADDR` to override it.
- Configure bounded server timeouts and graceful SIGINT and SIGTERM shutdown.
- Log startup, shutdown, request outcome, job ID when present, method, path, status, and duration with `slog`.
- Remove the obsolete one-shot `api-connect` flow after all validation callers move to the HTTP smoke path.

### Validation required

- health returns `200` without database access
- readiness returns `200` with PostgreSQL and `503` when its query fails
- startup and shutdown do not leak listeners or goroutines
- the smoke command starts PostgreSQL, migrates it, starts the API, submits and reads a job, then stops both
- `go test -count=1 ./cmd/api ./internal/api`
- `pwsh ./scripts/dev.ps1 smoke-test`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- runnable HTTP API
- initial structured logs
- health and readiness endpoints
- durable HTTP submission and lookup through `cmd/api`

### Decisions and deviations discovered during implementation

- None yet.

## Slice 5: restart durability and developer flow

Status: not started

### Goal

Prove that an HTTP-created job survives loss of API process memory, then document the exact development commands.

### Expected files and areas

- real-PostgreSQL API integration tests
- `scripts/dev.ps1`
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 4
- Testcontainers
- two independently constructed API server and database-pool lifecycles against one PostgreSQL instance

### Important decisions

- Destroy the first API server and close its pool before constructing the second API server and pool.
- Keep the restart test in one Go test process unless process-boundary behavior exposes a concrete defect. No API handler, store, or pool may survive between instances.
- Do not use an in-memory store as restart evidence.
- Keep the README limited to implemented Milestone 1 behavior.

### Validation required

1. Start fresh PostgreSQL and apply migrations.
2. Start API instance A.
3. Submit a job over HTTP.
4. Stop instance A and close its pool.
5. Start API instance B with a new pool.
6. Retrieve the same job over HTTP.
7. Verify its stable fields, `queued` status, and zero attempts.
8. Run `pwsh ./scripts/dev.ps1 check`.
9. Run `git diff --check`.

### Milestone requirements satisfied

- persisted job survives API restart
- end-to-end PostgreSQL coverage for HTTP submission and lookup
- durable queue behavior
- documented HTTP control-plane workflow

### Decisions and deviations discovered during implementation

- None yet.

## Milestone audit

Status: not started

After all slices pass, audit the implementation against the original Milestone 1 requirements and definition of done in `docs/project-plan.md`.

The audit must:

- run the complete repository validation against PostgreSQL,
- exercise `POST /v1/jobs` and `GET /v1/jobs/{id}` across an API restart,
- confirm submitted jobs remain queued with no attempts,
- inspect the diff for dispatcher, worker, gRPC, retry, lease, cancellation, idempotency, metrics, tracing, or execution work,
- record any deviation from the project plan,
- update `docs/current-status.md`,
- move this plan to `docs/exec-plans/completed/milestone-1.md` only if the definition of done passes.

## Deferred work

- `GET /v1/jobs/{id}/attempts` until attempts exist
- cancellation and submission idempotency until Milestone 4
- `available_at`, eligible-work indexes, dispatching, workers, and gRPC until Milestone 2 or the milestone that owns the behavior
- retries, leases, timeout enforcement, and execution controls until their planned milestones
- `/metrics`, Prometheus, and tracing until Milestone 5
