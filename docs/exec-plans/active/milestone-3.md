# Milestone 3 execution plan

## Milestone goal

Add leases, worker heartbeats, and bounded crash recovery. A crashed worker must stop renewing its active attempt, the dispatcher must recover that attempt after lease expiry, and a stale worker must not change the current job state.

Milestone 3 does not add handler-failure outcomes, retry backoff, execution-timeout enforcement, panic recovery, user cancellation, graceful worker draining, idempotent submission, metrics, or tracing. Lease-expiry recovery uses the existing retry states only as required to prevent crashed-worker job loss.

## Approved decisions

- Use PostgreSQL time as the authority for lease validity. A heartbeat cannot revive an expired lease, even if the reaper has not processed it.
- Use a five-second worker heartbeat interval and a twenty-second lease duration as configurable defaults.
- Start with a one-second reaper interval and a recovery batch size of 100. Both values remain configurable.
- Add unary `Heartbeat`. The request identifies the worker and its active job and attempt pairs. The response returns an explicit valid or stale result for every submitted attempt.
- Keep cancellation instructions out of the Milestone 3 heartbeat response.
- Add `abandoned` as the attempt status for lease expiry.
- On lease expiry, finish the current attempt as `abandoned`, clear the job's worker and lease, move the job to `retry_wait`, and set `available_at` to the current database time.
- If a lease-expired attempt exhausts `max_attempts`, mark the job `dead_lettered`. This narrow exhaustion rule prevents permanent loss without adding general handler-failure retries, backoff, or failure classification.
- Add only `active` and `lost` worker states with `last_seen_at`. Defer draining, stopped state, stopped timestamps, and graceful shutdown behavior.
- Add a deterministic `demo.sleep` handler for the crash-recovery acceptance test. The handler honors context cancellation but does not enforce the submitted execution timeout.
- Keep the dispatcher stateless. PostgreSQL owns leases, worker liveness, recovery transitions, and fencing.

## Slice 1: heartbeat and recovery contracts

Status: complete

### Goal

Define the Milestone 3 worker protocol and expired-attempt domain status without adding runtime heartbeat, persistence, lease, or recovery behavior.

### Expected files and areas

- `proto/quarry/dispatcher/v1/dispatcher.proto`
- generated code under `internal/rpc/generated/dispatcher/v1/`
- Protocol Buffer contract tests under `internal/rpc/`
- `internal/domain/attempt.go`
- `internal/domain/attempt_test.go`
- `docs/current-status.md`
- this execution plan

### Dependencies

- existing `grpc-go` and Protocol Buffers runtime dependencies
- pinned Buf and Go Protocol Buffer generators
- existing typed worker IDs, job IDs, and attempt numbers

### Important decisions

- Add only unary `Heartbeat` to the service.
- Represent each request attempt with `job_id` and `attempt_no`; the request-level `worker_id` completes the fencing identity.
- Return one result for every submitted attempt with an explicit `valid` or `stale` enum value.
- Reserve the enum zero value as unspecified so a missing state cannot mean valid or stale.
- Add no cancellation field, lease timestamp, renewal behavior, reaper behavior, or worker loop.
- Add `AttemptStatusAbandoned` without adding general failure statuses or error details.

### Validation required

- generate Protocol Buffer code
- verify committed generated code against fresh generation
- test Protocol Buffer round trips for heartbeat identities and valid and stale lease states
- test attempt-status parsing for `abandoned`
- `go test -count=1 ./internal/domain ./internal/rpc/...`
- `go vet ./internal/domain ./internal/rpc/...`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- worker-heartbeat protocol foundation
- expired-attempt domain representation
- attempt-number fencing contract

### Decisions and deviations discovered during implementation

- `HeartbeatRequest` carries the worker ID plus repeated `HeartbeatAttempt` values. Each attempt contains only the job ID and attempt number required to complete the fencing identity.
- `HeartbeatResponse` returns repeated `HeartbeatAttemptResult` values with the same job and attempt identity. `HeartbeatAttemptState` defines `UNSPECIFIED`, `VALID`, and `STALE`; the zero value cannot be mistaken for a lease decision.
- The contract returns one status per requested attempt rather than failing the entire heartbeat when one attempt is stale. Slice 3 remains responsible for enforcing that response rule.
- `AttemptStatusAbandoned` records lease-expired execution attempts without introducing handler-failure variants or error details.
- A Protocol Buffer serialization test lives in `internal/rpc/` rather than the generated package. `scripts/dev.ps1 generate-check` compares the generated package exactly with fresh Buf output, so hand-written tests do not belong in that directory.
- Adding `Heartbeat` to the generated client interface required a no-op method on the existing worker test double. The worker client and runtime still do not call `Heartbeat`.
- No architecture deviation was required. Slice 1 added no migration, lease timestamp, worker liveness state, heartbeat handler, renewal query, reaper, recovery transition, or worker heartbeat loop.

### Validation result

- `pwsh ./scripts/dev.ps1 generate` passed and produced the committed gRPC and Protocol Buffer code. The first restricted run hit `Access is denied` in the normal tool cache; the same command passed with normal cache access and required no code change.
- `go test -count=1 ./internal/domain ./internal/rpc/...` passed the domain tests, heartbeat serialization tests, and generated-package compilation.
- `go vet ./internal/domain ./internal/rpc/...` passed.
- `pwsh ./scripts/dev.ps1 generate-check` passed sqlc comparison, Buf lint and formatting, and fresh Protocol Buffer generation comparison.
- `go test -count=1 ./internal/worker/... ./cmd/worker ./internal/dispatcher ./cmd/dispatcher` passed after the existing worker RPC test double gained the required no-op `Heartbeat` method.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned tool checks, sqlc and Protocol Buffer generated-code checks, vet, all uncached tests, builds, Compose rendering, migrations through version 3, the HTTP smoke test, and the distributed process test. The distributed test processed 40 jobs with two workers and verified PostgreSQL state.
- `git diff --check` passed.
- GitHub-hosted CI was not run.

## Slice 2: lease schema, worker liveness, and leased claims

Status: complete

### Goal

Make every claim establish a durable lease in the same transaction that assigns the worker and creates the attempt.

### Expected files and areas

- a new migration under `internal/store/postgres/migrations/`
- `internal/store/postgres/queries/dispatcher.sql`
- generated sqlc code
- `internal/store/postgres/dispatcher_store.go`
- migration and dispatcher-store tests
- `cmd/dispatcher` configuration and tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- existing Goose, sqlc, pgx, and Testcontainers dependencies

### Important decisions

- Add `jobs.lease_expires_at` and the partial expired-lease index from `docs/project-plan.md`.
- Add `workers.state` and `workers.last_seen_at` with only `active` and `lost` states.
- Backfill an existing running Milestone 2 job with an immediately expired lease so migration does not preserve unrecoverable work.
- Set the worker to `active` and refresh `last_seen_at` on an identical registration retry.
- Establish the lease with PostgreSQL time inside the claim transaction.
- Clear the lease on successful completion.
- Preserve atomic `FOR UPDATE SKIP LOCKED` claims.

### Validation required

- migration apply, rollback, and reapplication against fresh PostgreSQL
- registration liveness tests
- atomic claim, attempt, assignment, and lease tests
- successful completion lease-clearing tests
- `pwsh ./scripts/dev.ps1 generate`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 migration-test`
- `go test -count=1 ./internal/store/postgres/... ./cmd/dispatcher`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- complete worker registration persistence
- durable leases
- lease assignment during claims

### Decisions and deviations discovered during implementation

- Migration 4 adds `jobs.lease_expires_at`, the planned partial expired-lease index, and a check constraint that keeps running state, worker assignment, and lease presence consistent. It also adds worker `state` and `last_seen_at` with only `active` and `lost` as valid states.
- Existing running jobs receive `statement_timestamp()` as their lease expiry during migration. They are immediately eligible for Slice 4 recovery instead of remaining unrecoverable.
- Identical registration retries preserve the original registration identity and metadata while setting the worker to `active` and refreshing `last_seen_at`. Conflicting reuse of a worker ID remains rejected.
- Claims use one PostgreSQL statement timestamp for the attempt start and lease basis. The dispatcher passes a positive whole-millisecond lease duration, with `QUARRY_LEASE_DURATION=20s` as the default.
- The existing claim transaction still locks the worker, enforces registered capacity, claims with `FOR UPDATE SKIP LOCKED`, assigns the worker, creates the attempt, and establishes the lease atomically.
- Successful completion clears both the active worker assignment and the lease in the existing completion transaction.
- No architecture deviation was required. Slice 2 added no heartbeat handling, lease renewal, expired-lease recovery, worker heartbeat loop, handler retry, or execution control.

### Validation result

- `pwsh ./scripts/dev.ps1 generate` passed and produced the committed sqlc output. The first restricted run hit `Access is denied` in the Go tool cache; the same command passed with normal cache access and required no code change.
- `go test -count=1 ./internal/store/postgres/... ./cmd/dispatcher` passed. This covered migration upgrade backfill, rollback and reapplication, worker liveness refresh, leased claims, concurrent claims, successful lease clearing, and dispatcher lease-duration configuration. The first restricted run could not read the Go build cache; the rerun with normal cache and Docker access passed.
- `pwsh ./scripts/dev.ps1 generate-check` passed fresh sqlc and Protocol Buffer generation comparison plus Buf checks.
- `pwsh ./scripts/dev.ps1 migration-test` passed against PostgreSQL 18.6.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned tool checks, generated-code checks, vet, all uncached tests, builds, Compose rendering, migrations through version 4, the HTTP smoke test, and the distributed process test. The distributed test processed 40 jobs with two workers and verified PostgreSQL state.
- `git diff --check` passed.
- GitHub-hosted CI was not run.

## Slice 3: heartbeat persistence, renewal, and gRPC handling

Status: complete

### Goal

Renew valid leases and update worker liveness through the dispatcher.

### Expected files and areas

- dispatcher SQL and generated sqlc code
- `internal/store/postgres/dispatcher_store.go`
- `internal/dispatcher/service.go`
- dispatcher service and PostgreSQL integration tests
- `internal/worker/grpc_client.go`
- gRPC client tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 and 2

### Important decisions

- Process one heartbeat atomically.
- Update `last_seen_at` when the worker reports no active attempts.
- Renew only jobs whose worker ID, attempt number, running status, and unexpired lease match.
- Return stale status for unknown, reassigned, completed, or expired attempts.
- Do not let one stale attempt prevent other valid attempts from renewing.
- Map an unregistered worker to `FailedPrecondition`.

### Validation required

- PostgreSQL tests for valid renewal, wrong identity, completed attempts, and expired leases
- test that an expired lease cannot revive before the reaper runs
- worker-liveness refresh tests
- gRPC boundary and status-code tests
- `go test -count=1 ./internal/dispatcher ./internal/store/postgres ./internal/worker`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- worker heartbeat
- lease renewal
- attempt-number fencing
- stale-heartbeat rejection

### Decisions and deviations discovered during implementation

- The store processes each heartbeat in one PostgreSQL transaction. It refreshes the registered worker first, then evaluates every submitted attempt without letting a stale result block valid renewals.
- Worker heartbeat refresh sets the worker to `active` and advances `last_seen_at`, including heartbeats with no active attempts. An unknown worker fails the transaction with `ErrWorkerNotRegistered`.
- Lease renewal requires the current worker ID, job ID, attempt number, `running` status, and a lease strictly later than `statement_timestamp()`. Expired leases remain unchanged and cannot revive before the reaper runs.
- Renewal sets the new expiry from PostgreSQL `statement_timestamp()` plus the configured lease duration. PostgreSQL remains the authority for both validity and the renewed timestamp.
- Heartbeat results preserve request order and identity. Unknown, completed, reassigned, expired, and wrong-attempt entries return `stale`; matching unexpired entries return `valid`.
- The gRPC boundary parses worker, job, and attempt identities into domain types before calling the store. It maps an unregistered worker to `FailedPrecondition` and malformed requests to `InvalidArgument`.
- The worker gRPC client now sends typed heartbeat identities and rejects malformed response identities or unspecified states. Slice 4 remains responsible for calling this method from a heartbeat loop.
- No architecture deviation was required. Slice 3 added no worker heartbeat loop, active-attempt registry, lease reaper, recovery transition, retry policy, or completion-after-expiry rule.

### Validation result

- `pwsh ./scripts/dev.ps1 generate` passed and produced the committed sqlc output. The first restricted run hit `Access is denied` in the Go and Buf caches; the rerun with normal cache access passed. A malformed test-double method signature found during formatting was fixed before focused validation.
- `go test -count=1 ./internal/dispatcher ./internal/store/postgres ./internal/worker` passed. The tests covered valid renewal, expired-lease non-revival, completed, unknown, reassigned, and wrong-attempt stale results, mixed valid and stale processing, empty heartbeats, liveness restoration, unregistered workers, gRPC validation and status codes, and worker-client request and response parsing.
- `pwsh ./scripts/dev.ps1 generate-check` passed fresh sqlc and Protocol Buffer generation comparison plus Buf checks.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned tool checks, generated-code checks, vet, all uncached tests, builds, Compose rendering, migrations through version 4, the HTTP smoke test, and the distributed process test. The distributed test processed 40 jobs with two workers and verified PostgreSQL state.
- `git diff --check` passed.
- GitHub-hosted CI was not run.

## Slice 4: worker heartbeat loop and active-attempt registry

Status: not started

### Goal

Make the worker heartbeat every acquired but unfinished attempt while preserving bounded concurrency.

### Expected files and areas

- `internal/worker/worker.go`
- `internal/worker/worker_test.go`
- `internal/worker/grpc_client.go`
- `internal/worker/grpc_client_test.go`
- `cmd/worker/main.go`
- `cmd/worker/main_test.go`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 3
- standard-library synchronization and context facilities

### Important decisions

- Add one heartbeat loop and a mutex-protected active-attempt registry.
- Register an attempt before placing it on the work channel.
- Heartbeat buffered, executing, and success-reporting attempts.
- Remove an attempt only after success is acknowledged or the dispatcher declares its lease stale.
- Give each attempt an internal context and cancel only that context when its lease becomes stale.
- Treat a raced `FailedPrecondition` success report as a lost attempt rather than a fatal worker error.
- Retry transient heartbeat failures on the next interval.
- Keep capacity bounded by all acquired but unfinished work.

### Validation required

- heartbeat identity and active-registry tests
- buffered, executing, and reporting attempt tests
- stale-lease cancellation tests
- transient heartbeat failure and raced-report tests
- existing worker concurrency and capacity tests
- `go test -count=1 ./internal/worker/... ./cmd/worker`
- worker race detection using the established Go 1.27 Linux container when required on Windows
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- worker heartbeat loop
- lease-renewal participation
- worker-side attempt fencing
- bounded concurrency with active lease tracking

### Decisions and deviations discovered during implementation

- Pending implementation.

### Validation result

- Pending implementation.

## Slice 5: bounded lease reaper and expired-attempt recovery

Status: not started

### Goal

Recover expired work safely when one or several dispatcher processes run reapers.

### Expected files and areas

- dispatcher recovery SQL and generated sqlc code
- `internal/store/postgres/dispatcher_store.go`
- reaper code under `internal/dispatcher/`
- `cmd/dispatcher/main.go`
- dispatcher, store, and attempt-history tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 2 through 4

### Important decisions

- Select expired running jobs in bounded batches with `FOR UPDATE SKIP LOCKED`.
- Finish each expired attempt as `abandoned`.
- Clear `current_worker_id` and `lease_expires_at`.
- Move recoverable jobs to immediately eligible `retry_wait`.
- Mark a job `dead_lettered` only when lease expiry exhausts `max_attempts`.
- Mark workers `lost` after their heartbeat age exceeds the liveness threshold. A later valid heartbeat can restore `active`.
- Keep the loop in the dispatcher runtime and the recovery transaction in PostgreSQL.
- Log and retry transient reaper database errors.
- Require a running success report to hold an unexpired current lease.

### Validation required

- real-PostgreSQL expired-attempt recovery tests
- concurrent-reaper exactly-once transition tests
- locked-row skip tests
- renewal-versus-reaper race tests
- stale success tests after attempt 2 starts
- completion-after-expiry rejection tests
- dispatcher reaper lifecycle tests
- `go test -count=1 ./internal/store/postgres/... ./internal/dispatcher ./cmd/dispatcher`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- lease reaper
- expired-attempt handling
- replacement-attempt eligibility
- attempt-number fencing
- stale completion rejection
- multi-dispatcher reaper safety

### Decisions and deviations discovered during implementation

- Pending implementation.

### Validation result

- Pending implementation.

## Slice 6: worker-crash acceptance test and developer flow

Status: not started

### Goal

Prove the Milestone 3 definition of done with real API, dispatcher, worker, and PostgreSQL processes.

### Expected files and areas

- `internal/worker/handlers/handlers.go`
- handler tests
- `scripts/dev.ps1`
- focused dispatcher recovery integration tests
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 5
- Docker Compose PostgreSQL
- built API, dispatcher, and worker binaries

### Important decisions

- Add `demo.sleep` only as deterministic long-running work. Do not enforce the submitted timeout.
- Add `pwsh ./scripts/dev.ps1 recovery-test`.
- Start worker 1 alone and wait for attempt 1 to run and renew its lease.
- Kill worker 1 without graceful shutdown and prove its liveness and lease stop advancing.
- Start worker 2 and require attempt 2 to succeed after recovery.
- Verify attempt 1 and attempt 2 through both HTTP and direct PostgreSQL reads.
- Run the stale-report gRPC integration test as part of the recovery command.
- Add the recovery test to `check` only after it is stable and bounded.

### Validation required

- deterministic `demo.sleep` handler tests
- `pwsh ./scripts/dev.ps1 recovery-test` twice
- `pwsh ./scripts/dev.ps1 distributed-test`
- `pwsh ./scripts/dev.ps1 check`
- relevant worker race tests
- direct PostgreSQL inspection of both attempts, worker identities, lease clearing, and final result
- cleanup verification for processes, containers, networks, volumes, and temporary binaries
- `git diff --check`
- `git status --short`

### Milestone requirements satisfied

- required worker-death test
- complete crash-recovery path
- attempt 1 abandonment
- attempt 2 execution
- stale-report fencing
- Milestone 3 definition of done

### Decisions and deviations discovered during implementation

- Pending implementation.

### Validation result

- Pending implementation.

## Milestone audit

Status: not started

After all slices pass, audit the implementation against the original Milestone 3 requirements and definition of done in `docs/project-plan.md`.

The audit must:

- run the complete local validation,
- repeat the worker-crash recovery test,
- run concurrent-reaper and stale-report tests against real PostgreSQL,
- run worker race detection,
- inspect the implementation for premature Milestone 4 behavior,
- confirm that no handler-failure classification, retry backoff, timeout enforcement, panic recovery, cancellation, graceful drain, or idempotency path was added,
- record decisions and deviations,
- update `docs/current-status.md`,
- move this plan to `docs/exec-plans/completed/milestone-3.md` only if the definition of done passes,
- report GitHub-hosted CI as unverified unless it runs against the completed state.
