# Milestone 4 execution plan

## Milestone goal

Complete Quarry's main execution semantics. Workers must report classified outcomes without stopping the process. The dispatcher must schedule durable retries with bounded full jitter, enforce maximum attempts, support submission idempotency, propagate execution timeouts and cancellation, and drain workers on SIGTERM.

Milestone 4 does not add metrics, tracing, dashboards, an observability collector, workflow behavior, a second queue, or any other Milestone 5 feature.

## Existing foundation

Milestones 0 through 3 already provide:

- durable jobs and attempts in PostgreSQL,
- the six V1 job states,
- submitted `max_attempts` and `timeout_ms`,
- `available_at` ordering and claims from `queued` and `retry_wait`,
- bounded workers and typed attempt identity,
- worker registration and heartbeats,
- leases, lease renewal, and bounded recovery,
- attempt fencing and stale-report rejection,
- immediate replacement eligibility after lease expiry.

Milestone 4 extends these paths. It must not introduce an in-memory retry scheduler or let workers access PostgreSQL.

## Approved decisions

### Attempt outcomes and failure details

- Use explicit attempt outcomes and matching stored statuses: `succeeded`, `retryable_failed`, `permanent_failed`, `cancelled`, `timed_out`, `panicked`, and the existing `abandoned` lease-expiry status.
- Persist a stable error code and a safe diagnostic message for non-success outcomes.
- Keep panic values, stack traces, wrapped internal errors, and other unsafe details out of gRPC responses and public HTTP responses.
- Return error details through attempt history. Derive a job's latest failure summary from attempt history instead of duplicating it on the job row.

### Handler failure classification

- Give handlers explicit constructors or typed errors for retryable and permanent failures.
- Treat an unclassified handler error as permanent and persist a generic safe code and message.
- A handler failure must finish only its attempt. It must not stop the worker process or cancel unrelated attempts.

### Retry policy

- Treat explicit retryable failures, timeouts, panics, and lease expiry as retryable until `max_attempts` is exhausted.
- Never retry a permanent failure or user cancellation.
- Use the project-plan defaults: three maximum attempts when submission omits the value, a one-second base delay, and a sixty-second maximum backoff.
- Configure the dispatcher defaults through `QUARRY_RETRY_BASE_DELAY` and `QUARRY_RETRY_MAX_DELAY`. Both values must be positive whole-millisecond durations, and the maximum must not be less than the base.
- Use exponential backoff with full jitter. For failed attempt `n`, calculate `cap = min(maximum, base * 2^(n - 1))`, then choose a delay in the inclusive range from zero through the cap.
- Put the calculation in a small Go policy with an injected random source so unit tests can choose exact delays.
- Use PostgreSQL `statement_timestamp()` as the schedule base. Persist `available_at` in the same transaction that finishes the attempt and changes the job state.
- Apply the same policy to handler failures, timeouts, panics, and lease-expiry recovery.
- Keep retries durable through `available_at`. Do not add process-local retry timers or another queue.

### Submission idempotency

- Scope an idempotency key by job type, as specified by the `(job_type, idempotency_key)` unique index.
- Hash the job type, canonical payload, resolved `max_attempts`, and `timeout_ms` with SHA-256.
- Normalize JSON object ordering and insignificant whitespace before hashing. Preserve JSON number spellings, so numerically equal but textually different numbers remain different requests.
- Let the PostgreSQL uniqueness constraint resolve concurrent submissions.
- Return `201 Created` for the first submission.
- Return `200 OK` with `deduplicated: true` for an identical replay.
- Return `409 Conflict` when the same job type and key arrive with changed input.

### Execution timeouts and panics

- Derive each handler context from the worker lifecycle, the attempt lifecycle, and the submitted timeout.
- Treat a timeout as retryable until attempts are exhausted.
- Recover panics at the handler boundary. Log the panic and stack with `log/slog`, report a safe panicked outcome, and keep the worker running.
- Treat a panic as retryable until attempts are exhausted.
- Do not claim that Quarry can stop a handler that ignores context cancellation.

### Cancellation

- Queued and retry-wait cancellation becomes terminal in one PostgreSQL transaction.
- Running cancellation persists `cancel_requested_at` while the job remains running.
- A heartbeat returns a cancellation instruction for a matching active attempt.
- The worker cancels only that attempt's handler context and keeps the attempt registered until the cancelled report is acknowledged or the lease becomes stale.
- User cancellation never retries.
- If a running cancellation request exists when the worker reports a failure, timeout, panic, or when its lease expires, resolve the job as cancelled instead of retrying it.
- A valid success report may win a race with a running cancellation request. Cancellation is cooperative, so the request does not rewrite completed work as cancelled.
- Repeating cancellation for an already cancelled job is idempotent.
- Cancelling a succeeded or dead-lettered job returns `409 Conflict` and does not change stored state.

### Graceful worker shutdown

- Default `QUARRY_WORKER_SHUTDOWN_TIMEOUT` to ten seconds.
- On SIGTERM, stop acquisition first. Continue heartbeats and normal outcome reports while active attempts drain.
- If all active attempts finish before the deadline, stop cleanly.
- At the deadline, cancel the remaining attempt contexts with a shutdown-specific cause and exit.
- Do not report shutdown cancellation as user cancellation. Any unfinished attempt recovers through lease expiry.
- Do not add durable `draining` or `stopped` worker states. The pull model needs only local acquisition shutdown for correct draining, and no current API consumes those states.

### Public read models

- Add cancellation state to `GET /v1/jobs/{id}`.
- Add status, error code, and safe error message to `GET /v1/jobs/{id}/attempts`.
- Add the latest failure summary to the job response when a failed attempt exists.
- Keep payloads out of job lookup responses and keep internal error details out of public responses.

## Slice 1: outcome contracts and retry policy

Status: complete

### Goal

Define the complete attempt outcome model, handler-failure classification, heartbeat cancellation instruction, and deterministic retry calculation without changing runtime behavior.

### Expected files and areas

- `internal/domain/attempt.go`
- new failure and retry domain files under `internal/domain/`
- domain tests
- `proto/quarry/dispatcher/v1/dispatcher.proto`
- generated code under `internal/rpc/generated/dispatcher/v1/`
- Protocol Buffer contract tests under `internal/rpc/`
- compile-only updates to existing test doubles
- `docs/current-status.md`
- this execution plan

### Dependencies

- existing typed job IDs, worker IDs, and attempt numbers
- existing Protocol Buffer and gRPC dependencies
- pinned Buf and Go Protocol Buffer generators
- standard-library error and randomness facilities
- no new third-party dependency

### Important decisions

- Represent all approved outcomes explicitly in the domain and Protocol Buffer contract.
- Use constructors to prevent invalid result and failure-field combinations.
- Reserve every Protocol Buffer enum zero value as unspecified.
- Add cancellation information to each heartbeat attempt result without changing lease identity or ordering.
- Make retry-cap and jitter calculation deterministic under an injected random source.
- Do not add persistence or runtime handling in this slice.

### Validation required

- outcome-constructor and status-parsing tests
- retry cap tests through and beyond the sixty-second maximum
- inclusive full-jitter bound tests with exact injected values
- Protocol Buffer round-trip and zero-value tests
- `go test -count=1 ./internal/domain ./internal/rpc/...`
- `go vet ./internal/domain ./internal/rpc/...`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- retryable versus permanent failure model foundation
- exponential-backoff calculation
- full-jitter calculation
- complete `ReportAttempt` outcome contract
- heartbeat cancellation contract

### Decisions and deviations discovered during implementation

- `AttemptOutcome` uses an unexported representation with one constructor for each worker-reportable outcome. A success contains only a `Result`; every non-success outcome contains only validated `AttemptFailure` details.
- Failure codes use lower snake case and are limited to 64 bytes. Safe diagnostic messages must contain non-whitespace text and are limited to 1,024 bytes.
- `abandoned` remains a dispatcher-owned stored attempt status for lease recovery. Workers cannot report it through `ReportAttempt`.
- The Protocol Buffer `oneof` uses distinct fields for retryable failure, permanent failure, cancellation, timeout, and panic. The fields share one `AttemptFailure` message because each carries the same code and safe-message shape. An unset `oneof` remains the invalid zero outcome.
- `HeartbeatAttemptResult.cancel_requested` is a Boolean instruction. Its false zero value preserves the current no-cancellation behavior until Slice 7 adds persistence and worker handling.
- `RetryPolicy` owns only cap and jitter calculation. It accepts positive whole-millisecond base and maximum delays plus an injected bounded random function.
- Full jitter samples whole milliseconds in the inclusive range from zero through the cap. The policy passes `cap_ms + 1` to the injected upper-exclusive random function and rejects an out-of-range result.
- Backoff cap calculation stops at the configured maximum without iterating through every possible attempt number or overflowing `time.Duration`.
- The dispatcher service still accepts only success reports, and the worker still sends only success reports. Slice 2 owns persistence and dispatcher handling for the new outcomes. Later worker behavior remains deferred to Slices 3, 5, and 7.
- No architecture or project-plan deviation was required.

### Validation result

- `go test -count=1 ./internal/domain` passed all attempt status, outcome invariant, failure validation, retry cap, and deterministic full-jitter tests. The first restricted run could not read the Windows Go build cache; the rerun with normal cache access passed without a code change.
- `pwsh ./scripts/dev.ps1 generate` passed and regenerated the committed Protocol Buffer and gRPC code with the pinned tools.
- `go test -count=1 ./internal/domain ./internal/rpc/...` passed domain tests, Protocol Buffer round trips for every outcome, heartbeat cancellation preservation, and generated-package compilation.
- `go vet ./internal/domain ./internal/rpc/...` passed.
- `pwsh ./scripts/dev.ps1 generate-check` passed sqlc comparison, Buf lint and formatting, and fresh Protocol Buffer generation comparison.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned tool checks, generated-code checks, vet, all uncached tests, builds, Compose rendering, migrations through version 4, the HTTP smoke test, the 40-job distributed process test, the worker-crash recovery process test, stale-report coverage, and cleanup verification.
- `git diff --check` passed after the completion documentation update.
- GitHub-hosted CI was not run.

## Slice 2: durable failure transitions and retry scheduling

Status: complete

### Goal

Finish reported failures atomically, schedule eligible retries, dead-letter permanent or exhausted work, and apply the retry policy to expired leases.

### Expected files and areas

- a new migration under `internal/store/postgres/migrations/`
- `internal/store/postgres/queries/dispatcher.sql`
- `internal/store/postgres/queries/jobs.sql`
- generated sqlc code
- `internal/store/postgres/dispatcher_store.go`
- `internal/store/postgres/job_store.go`
- `internal/dispatcher/service.go`
- `cmd/dispatcher/main.go` and retry-policy configuration tests
- domain, dispatcher, store, migration, and attempt-history tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- existing Goose, sqlc, pgx, and Testcontainers dependencies
- existing leased claim, success-report, and recovery transactions

### Important decisions

- Add attempt error fields without duplicating failure state on `jobs`.
- Finish the attempt and change the job in one PostgreSQL transaction.
- Require the current worker, attempt number, running state, and unexpired lease for a new report.
- Accept an exact repeated report idempotently. Reject a changed outcome for the same attempt.
- Persist `retry_wait` with a calculated `available_at` for retryable work that has attempts remaining.
- Persist `dead_lettered` for permanent failures and exhausted retryable outcomes.
- Apply a separate jittered delay to each recovered lease-expired attempt while preserving bounded `FOR UPDATE SKIP LOCKED` recovery.
- Preserve PostgreSQL as the authority for transition time and queue eligibility.

### Validation required

- migration apply, rollback, and reapplication tests
- retryable failure scheduling with an exact injected delay
- claim rejection before `available_at`
- claim success at or after `available_at`
- permanent failure dead-letter tests
- maximum-attempt exhaustion tests
- exact repeated-report and changed-report tests
- failure-report fencing and lease-expiry race tests
- lease-recovery backoff tests
- concurrent reaper and locked-row skip regression tests
- attempt-history error mapping tests
- `go test -count=1 ./internal/store/postgres/... ./internal/dispatcher ./cmd/dispatcher`
- `pwsh ./scripts/dev.ps1 migration-test`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- maximum attempts for reported failures
- durable exponential backoff and full jitter
- `retry_wait`
- `dead_lettered`
- retry timing
- attempts stop at the configured maximum

### Decisions and deviations discovered during implementation

- Migration 5 adds nullable `error_code` and `error_message` columns to `job_attempts`, backfills existing abandoned attempts with the stable `lease_expired` failure, and constrains every stored attempt status to the matching presence or absence of validated failure details.
- `DispatcherStore.ReportAttempt` now owns one transaction for success and every declared failure outcome. It fences on job ID, current attempt number, worker ID, running state, and an unexpired lease; updates the attempt and job together; and accepts only an exact repeated report after completion.
- Retryable failures, timeouts, panics, and non-final abandoned attempts enter `retry_wait`. Permanent failures and exhausted retryable outcomes enter `dead_lettered`. Cancelled reports enter `cancelled`, but worker cancellation propagation and the public cancellation command remain deferred to Slices 6 and 7.
- The store receives a `RetryPolicy` at construction. `cmd/dispatcher` builds it from `QUARRY_RETRY_BASE_DELAY` and `QUARRY_RETRY_MAX_DELAY`, defaulting to one second and sixty seconds, and rejects invalid or sub-millisecond values and a maximum below the base.
- PostgreSQL supplies one `statement_timestamp()` after the report row lock is acquired. The transaction uses that value for lease fencing, attempt completion, job completion, and retry eligibility. This fixed a report-versus-expiry race where evaluating time on separate SQL statements could pass the initial fence and then update zero job rows.
- Lease recovery locks a bounded batch with `FOR UPDATE SKIP LOCKED`, calculates a separate jittered delay for each non-final attempt, and applies every selected transition in the same transaction. Abandoned attempts store the safe `lease_expired` failure.
- Attempt history now maps stored failure details into the domain and returns `error_code` and `error_message` through `GET /v1/jobs/{id}/attempts`. Successful and running attempts return null failure fields.
- Workers still report only success. Handler failure classification and reporting remain deferred to Slice 3; timeout, panic, cancellation, idempotency, and shutdown behavior remain in their approved later slices.
- No architecture or project-plan deviation was required.

### Validation result

- `go test -count=1 ./internal/store/postgres/... ./internal/dispatcher ./cmd/dispatcher` passed in 49.903 seconds for the PostgreSQL store package, 4.518 seconds for migration tests, 6.489 seconds for dispatcher tests, and 0.993 seconds for dispatcher command tests.
- The failure-report versus lease-recovery race test passed three consecutive runs after the lock-time fencing fix.
- `pwsh ./scripts/dev.ps1 migration-test` passed migration apply, rollback, and reapplication through version 5, including the abandoned-attempt failure backfill and outcome constraints.
- `pwsh ./scripts/dev.ps1 generate-check` passed the sqlc comparison and fresh Protocol Buffer generation comparison.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned tool checks, generated-code checks, vet, all uncached tests, builds, Compose rendering, migrations through version 5, the HTTP smoke test, the 40-job distributed process test, the worker-crash recovery process test, stale-report coverage, and cleanup verification.
- `git diff --check` passed after the completion documentation update.
- GitHub-hosted CI was not run.

## Slice 3: worker handler-failure reporting

Status: complete

### Goal

Let handlers report retryable and permanent failures without stopping the worker process or unrelated attempts.

### Expected files and areas

- `internal/worker/worker.go`
- `internal/worker/grpc_client.go`
- worker and gRPC client tests
- handler contracts and handler tests
- dispatcher gRPC integration tests
- `cmd/worker/` tests if constructor wiring changes
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 and 2
- existing active-attempt registry, heartbeat loop, and bounded worker pool
- existing transient report retry behavior

### Important decisions

- Convert typed handler errors into the matching domain outcome at the handler boundary.
- Convert an unclassified error into a generic permanent failure.
- Keep the attempt active and heartbeating until its outcome report is acknowledged.
- Retry transient gRPC report failures with the same attempt identity and outcome.
- Treat `FailedPrecondition` as a lost-attempt race and remove only that attempt.
- Do not add timeout, panic, or user-cancellation handling in this slice.

### Validation required

- retryable and permanent handler classification tests
- unclassified-error tests
- worker-continuation tests after a handler error
- concurrent-attempt isolation tests
- report retry with stable identity and outcome
- real gRPC and PostgreSQL retryable-failure transition test
- real gRPC and PostgreSQL permanent-failure test
- focused worker and dispatcher tests
- worker race detection with the established Go 1.27 Linux container
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- end-to-end retryable failure handling
- end-to-end permanent failure handling
- permanent errors do not retry
- maximum attempts apply to handler failures

### Decisions and deviations discovered during implementation

- Handlers classify failures with validated `HandlerError` constructors for retryable and permanent outcomes. The typed error keeps the safe persisted code and message separate from an optional wrapped internal cause, and classification works through ordinary error wrapping.
- An unclassified handler error becomes a permanent failure with the stable safe code `handler_error` and message `handler failed`; the original error is not sent over gRPC or persisted.
- The worker constructs one immutable outcome after the handler returns and uses the existing acknowledgement loop to retry transient report failures with the same worker, job, attempt, and outcome. The attempt remains active and heartbeating until acknowledgement.
- A handler failure does not enter the worker-fatal path. `FailedPrecondition` still removes only the stale attempt, so subsequent work and unrelated concurrent attempts continue.
- The worker gRPC client now maps success, retryable failure, and permanent failure outcomes. Timeout, panic, and cancellation outcomes remain deliberately rejected until their later slices add the corresponding worker behavior.
- Real gRPC and PostgreSQL integration tests prove that retryable handler failures consume attempts through the submitted maximum and that permanent handler failures dead-letter after one attempt without retrying.
- No architecture or project-plan deviation was required.

### Validation result

- `go test -count=1 ./internal/worker/... ./cmd/worker` passed handler classification, generic safe-error handling, worker continuation, concurrent-attempt isolation, stable report retry, gRPC mapping, and existing worker behavior.
- `go test -run 'TestWorkerRetryableFailureRetriesUntilMaximumAttempts|TestWorkerPermanentFailureDoesNotRetry' -count=1 ./internal/dispatcher` passed against real gRPC and PostgreSQL in 5.716 seconds after Docker access was enabled. The restricted Windows run first reported the known Testcontainers environment error `rootless Docker is not supported on Windows`; no code change was needed.
- `go test -count=1 ./internal/worker/... ./cmd/worker ./internal/dispatcher` passed the complete focused worker and dispatcher suites before the final assertion tightening; the two affected PostgreSQL tests then passed again with their exact stored failure-detail assertions.
- `docker run --rm --volume "C:\Users\shai\Documents\Code\quarry:/src" --workdir /src golang:1.27.0-bookworm go test -race -count=1 ./internal/worker/... ./cmd/worker` passed the worker packages under the race detector.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned tool checks, generated-code checks, vet, all uncached tests, builds, Compose rendering, migrations through version 5, the HTTP smoke test, the 40-job distributed process test, the worker-crash recovery process test, stale-report coverage, and cleanup verification.
- `git diff --check` passed after the completion documentation update.
- GitHub-hosted CI was not run.

## Slice 4: concurrent submission idempotency

Status: not started

### Goal

Deduplicate identical job submissions durably and reject changed submissions that reuse the same idempotency key.

### Expected files and areas

- a new migration under `internal/store/postgres/migrations/`
- job submission and idempotency domain types
- request normalization and hashing under `internal/api/` or `internal/domain/`
- `internal/store/postgres/queries/jobs.sql`
- generated sqlc code
- `internal/store/postgres/job_store.go`
- `internal/api/handler.go`
- API, store, restart, migration, and concurrency tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- existing submission transaction and strict HTTP request parsing
- standard-library JSON and SHA-256 support
- existing Goose, sqlc, pgx, and Testcontainers dependencies
- no new third-party dependency

### Important decisions

- Read `Idempotency-Key` only at the HTTP boundary and pass a validated domain value inward.
- Hash normalized submission values after applying the default `max_attempts`.
- Use the database unique index as the concurrent arbitration point.
- Return the stored job for an exact replay without creating a second logical job.
- Detect a changed request by comparing the stored and submitted hashes.
- Preserve behavior for submissions without an idempotency key.

### Validation required

- canonical JSON and stable hash tests
- omitted-key behavior
- first-submission `201 Created` response
- identical-replay `200 OK` and `deduplicated: true` response
- changed-request `409 Conflict` response
- same key under a different job type
- many concurrent identical submissions creating one job
- database uniqueness under concurrent requests
- idempotent job retrieval after API and pool restart
- `go test -count=1 ./internal/api ./internal/domain ./internal/store/postgres/...`
- `pwsh ./scripts/dev.ps1 migration-test`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- submission idempotency
- concurrent idempotent submissions create one logical job
- changed request with the same key conflicts

### Decisions and deviations discovered during implementation

None yet.

## Slice 5: execution timeouts and panic recovery

Status: not started

### Goal

Apply submitted execution deadlines cooperatively and keep the worker alive after a handler panic.

### Expected files and areas

- `internal/worker/worker.go`
- worker configuration if logger injection is required
- worker tests
- handler tests
- `cmd/worker/main.go` and command tests
- dispatcher integration tests for timeout and panic outcomes
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 3
- existing acquired-job timeout field
- standard-library `context`, `runtime/debug`, and `log/slog`
- no new direct metrics or tracing dependency

### Important decisions

- Create one timeout context for each handler invocation.
- Distinguish timeout from lease-stale, worker-shutdown, and user-cancellation causes.
- If the timeout wins, report `timed_out` even if the handler returns an error after observing cancellation.
- Recover panic at the handler call boundary so one executor can continue processing later attempts.
- Log the panic and stack. Persist only a stable code and safe message.
- Route timeout and panic outcomes through the normal retry policy.

### Validation required

- submitted timeout reaches the handler context
- timeout cancels a context-aware handler
- timeout outcome retries and then dead-letters at the configured maximum
- success-versus-timeout boundary race tests
- panic recovery and worker-survival tests
- panic stack log test
- panic retry and exhaustion tests
- handler-ignores-context limitation test where feasible without leaking the test process
- focused worker and dispatcher tests
- worker race detection with the established Go 1.27 Linux container
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- execution timeouts
- timeout propagates cancellation
- panic recovery
- timeout and panic retry behavior

### Decisions and deviations discovered during implementation

None yet.

## Slice 6: durable cancellation state

Status: not started

### Goal

Add the cancellation state machine and storage operations without exposing the public cancellation route before worker propagation works.

### Expected files and areas

- a new migration under `internal/store/postgres/migrations/`
- `internal/domain/job.go`
- `internal/store/postgres/queries/jobs.sql`
- `internal/store/postgres/queries/dispatcher.sql`
- generated sqlc code
- `internal/store/postgres/job_store.go`
- `internal/store/postgres/dispatcher_store.go`
- job read-model mapping
- API response types without the cancel route
- domain, migration, store, API-read, and recovery tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 2's generic outcome transactions
- existing job locking and lease recovery
- Goose, sqlc, pgx, and Testcontainers

### Important decisions

- Add `cancel_requested_at` as the durable cancellation request.
- Lock the job before deciding the cancellation transition.
- Cancel queued and retry-wait jobs immediately and set terminal timestamps.
- Record a running request without clearing its worker or lease.
- Exclude cancellation-requested work from claims as a defensive invariant.
- Resolve an expired running attempt as cancelled when a request exists.
- Add cancellation state to the job read model before enabling the public command.
- Keep `POST /v1/jobs/{id}/cancel` unregistered until Slice 7 completes propagation.

### Validation required

- queued cancellation transaction
- retry-wait cancellation transaction
- running cancellation request persistence
- repeated cancelled request idempotency
- succeeded and dead-lettered conflict behavior
- cancellation-versus-claim race
- cancellation-aware lease recovery
- job response cancellation state
- migration apply, rollback, and reapplication
- `go test -count=1 ./internal/domain ./internal/api ./internal/store/postgres/...`
- `pwsh ./scripts/dev.ps1 migration-test`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- durable cancellation state
- queued cancellation semantics
- retry-wait cancellation semantics
- recovery of a cancelled running job after worker loss

### Decisions and deviations discovered during implementation

None yet.

## Slice 7: cooperative running cancellation

Status: not started

### Goal

Expose the cancellation API, propagate running cancellation through heartbeats, cancel only the matching handler context, and persist the worker's cancelled report.

### Expected files and areas

- `internal/api/handler.go`
- API tests
- `internal/store/postgres/job_store.go`
- heartbeat queries and generated sqlc code
- `internal/store/postgres/dispatcher_store.go`
- `internal/dispatcher/service.go`
- `internal/worker/grpc_client.go`
- `internal/worker/worker.go`
- dispatcher, client, worker, HTTP, and PostgreSQL integration tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1, 2, 5, and 6
- existing heartbeat identity and active-attempt registry
- generic attempt outcome reporting

### Important decisions

- Return cancellation as an instruction on a valid heartbeat result.
- Signal cancellation without removing the active attempt from the registry.
- Continue heartbeat and report retries until the cancelled report is acknowledged or the lease becomes stale.
- Keep user cancellation distinct from timeout, stale lease, and shutdown.
- Let a valid success report win its race with a cancellation request.
- Convert failure, timeout, panic, and lease-expiry transitions to cancelled when a request already exists.
- Register `POST /v1/jobs/{id}/cancel` only after the full path works.

### Validation required

- queued cancellation through HTTP
- retry-wait cancellation through HTTP
- running cancellation through real HTTP, gRPC, worker, and PostgreSQL boundaries
- one cancelled attempt does not affect another active attempt
- cancellation instruction preserves attempt identity
- cancellation remains heartbeated until acknowledgement
- transient cancelled-report retry
- success-versus-cancellation race
- cancellation-versus-timeout race
- cancelled outcomes never retry
- focused worker, dispatcher, API, and store tests
- worker race detection with the established Go 1.27 Linux container
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- cooperative cancellation
- queued cancellation API
- running cancellation
- cancellation result persistence

### Decisions and deviations discovered during implementation

None yet.

## Slice 8: SIGTERM drain and Milestone 4 demonstration

Status: not started

### Goal

Stop acquisition on SIGTERM, continue heartbeats while attempts drain, enforce the shutdown deadline, and prove graceful completion and forced recovery with real processes.

### Expected files and areas

- `internal/worker/worker.go`
- worker lifecycle tests
- `cmd/worker/main.go`
- worker command configuration and lifecycle tests
- `scripts/dev.ps1`
- focused Milestone 4 integration tests
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 7
- existing `demo.sleep` handler
- Docker Compose PostgreSQL
- built API, dispatcher, and worker binaries
- existing process-test and cleanup helpers

### Important decisions

- Separate acquisition shutdown from the attempt-lifecycle context.
- Keep the heartbeat loop and report path alive while attempts drain.
- Wake the drain waiter whenever the active-attempt registry changes.
- Cancel remaining attempts with a shutdown-specific cause at the configured deadline.
- Return after the deadline instead of waiting forever for a handler that ignores context.
- Let unfinished attempts recover through lease expiry.
- Add `pwsh ./scripts/dev.ps1 semantics-test` as the rerunnable Milestone 4 proof.
- Add `semantics-test` to `check` only after standalone repeated runs pass.

### Validation required

- worker stops acquiring immediately after SIGTERM
- active attempts keep heartbeating during drain
- a short active job reports success before shutdown completes
- a configured deadline cancels remaining handler contexts
- forced shutdown leaves the attempt for lease recovery rather than reporting user cancellation
- another worker receives and completes the replacement attempt
- direct PostgreSQL verification of attempts, retry timing, cancellation state, and final state
- `pwsh ./scripts/dev.ps1 semantics-test` twice
- `pwsh ./scripts/dev.ps1 distributed-test`
- `pwsh ./scripts/dev.ps1 recovery-test`
- worker race detection with the established Go 1.27 Linux container
- `pwsh ./scripts/dev.ps1 check`
- process, container, network, volume, and temporary-binary cleanup verification
- `git diff --check`
- `git status --short`

### Milestone requirements satisfied

- SIGTERM graceful worker drain
- graceful shutdown test
- forced shutdown recovery test
- end-to-end Milestone 4 execution semantics

### Decisions and deviations discovered during implementation

None yet.

## Milestone audit

Status: not started

After all slices pass, audit the implementation against the original Milestone 4 requirements and definition of done in `docs/project-plan.md`.

The audit must:

- run the complete local validation,
- repeat `pwsh ./scripts/dev.ps1 semantics-test`,
- run retry, idempotency, cancellation, and lease-race tests against real PostgreSQL,
- run worker race detection,
- inspect all job and attempt transitions directly,
- verify public cancellation and failure responses,
- verify retry timing through `available_at`,
- inspect the implementation for premature Milestone 5 behavior,
- confirm that no Prometheus metrics, OpenTelemetry tracing, `/metrics` endpoint, collector, Grafana, or dashboard was added,
- record decisions and deviations,
- update `docs/current-status.md`,
- move this plan to `docs/exec-plans/completed/milestone-4.md` only if the definition of done passes,
- report GitHub-hosted CI as unverified unless it runs against the completed state.

The milestone is complete only when the main execution semantics are implemented, directly tested, and defensible from the stored state and process behavior.
