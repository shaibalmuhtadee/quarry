# Milestone 2 execution plan

## Milestone goal

Build the successful distributed execution path. Several worker processes must claim and execute queued jobs concurrently through a stateless gRPC dispatcher backed by PostgreSQL.

Milestone 2 does not include heartbeats, leases, crash recovery, retries, failure classification, execution timeouts, cancellation, panic recovery, graceful worker draining, idempotent submission, metrics, or tracing. Without leases, a lost acquisition response or worker crash can leave a job in `running` until Milestone 3 adds recovery.

## Approved decisions

- Workers advertise supported job types on each `AcquireJobs` request. The dispatcher claims only work that the requesting worker can execute.
- Use a pinned Buf-based Protocol Buffer toolchain. Commit generated Go code and verify it through `scripts/dev.ps1`.
- Persist basic worker registration in PostgreSQL during Milestone 2. Milestone 3 adds worker liveness, heartbeats, leases, and stopped or lost states.
- Support only successful attempt reporting during Milestone 2. Later milestones add failure outcomes and recovery.
- Make identical worker registration and successful attempt reports safe to repeat.
- Return attempt history as an `attempts` array ordered by attempt number. A missing job returns `404`; a job without attempts returns an empty array.
- Use plaintext gRPC for local Milestone 2 development. The dispatcher address remains configurable.

## Slice 1: protobuf and domain contracts

Status: complete

### Goal

Define the Milestone 2 worker protocol and typed worker and attempt identities without adding runtime dispatcher, persistence, or worker behavior.

### Expected files and areas

- `proto/quarry/dispatcher/v1/dispatcher.proto`
- generated code under `internal/rpc/generated/dispatcher/v1/`
- worker and attempt types under `internal/domain/`
- Buf generation configuration
- `go.mod` and `go.sum`
- `scripts/dev.ps1`
- `docs/current-status.md`
- this execution plan

### Dependencies

- direct `grpc-go` and Protocol Buffers runtime dependencies
- pinned Buf and Go Protocol Buffer generators
- existing `github.com/google/uuid`

### Important decisions

- Define only `RegisterWorker`, `AcquireJobs`, and `ReportAttempt`. Do not define `Heartbeat`.
- Include worker ID, hostname, version, configured concurrency, and process start time in registration.
- Include worker ID, available capacity, and supported job types in acquisition.
- Return job ID, attempt number, type, JSON payload, and stored execution timeout.
- Define `ReportAttempt` with a success outcome that can accept additional outcome variants later without replacing the RPC.
- Keep Protocol Buffer types at the network boundary. Domain code uses validated worker IDs, attempt numbers, and attempt statuses.

### Validation required

- generate Protocol Buffer code
- verify committed generated code against fresh generation
- test worker ID, attempt number, and attempt-status parsing
- `go test -count=1 ./internal/domain ./internal/rpc/...`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- protobuf definitions
- worker and attempt protocol contracts
- protocol foundation for registration, acquisition, and successful reporting

### Decisions and deviations discovered during implementation

- Buf CLI v1.72.0 is pinned as a Go tool. Code generation uses the pinned BSR plugins `protocolbuffers/go:v1.36.11` and `grpc/go:v1.5.1`; generation therefore requires network access to the Buf Schema Registry when the plugins are not cached.
- Generated code uses `grpc-go` v1.80.0 and `google.golang.org/protobuf` v1.36.12 as direct runtime dependencies. Adding the Buf tool also updated its indirect dependency graph during `go mod tidy`.
- `buf.gen.yaml` uses the Go plugin `module` option so generated files land directly under `internal/rpc/generated/dispatcher/v1/`. The generated package does not mirror the leading `quarry/` source directory.
- `ReportAttemptRequest` has a success-only `oneof`. Later milestones can add outcomes without replacing the RPC or changing the existing success field.
- `WorkerID` rejects malformed and zero UUIDs. `AttemptNumber` accepts only positive `int32` values. Milestone 2 attempt status values are `running` and `succeeded`.
- `scripts/dev.ps1 generate-check` runs Buf lint and format checks, generates into a unique temporary directory, and compares that output with the committed generated package. A deliberate temporary change to generated code proved that the check fails on stale output; regeneration restored the file and the check passed.
- `.gitattributes` keeps the Buf configuration, Protocol Buffer sources, and generated Go files on LF line endings across Windows and Unix checkouts.
- No architecture deviation was required. Slice 1 added no dispatcher runtime, PostgreSQL behavior, worker runtime, heartbeat, lease, or recovery code.

### Validation result

- `go tool buf lint` passed.
- `go tool buf format --diff --exit-code` passed.
- `go test -count=1 ./internal/domain ./internal/rpc/...` passed. The generated RPC package compiled and had no test files.
- `go vet ./internal/domain ./internal/rpc/...` passed.
- `go build ./internal/rpc/...` passed.
- `pwsh ./scripts/dev.ps1 generate-check` passed after generating code with the pinned plugins. The stale-output negative check failed as expected before regeneration.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned Buf, Goose, and sqlc checks, generated sqlc and Protocol Buffer consistency, vet, all uncached tests, builds, Compose rendering, PostgreSQL integration tests, and the real API HTTP smoke test.
- The first restricted cache attempt hit `Access is denied` under `%LOCALAPPDATA%` for Buf and Go. The same commands passed with normal cache access; no code change was required for that environment error.
- GitHub-hosted CI was not run.

## Slice 2: PostgreSQL registration and atomic claims

Status: complete

### Goal

Implement durable worker registration and the transaction that claims eligible jobs and creates attempts.

### Expected files and areas

- a new migration under `internal/store/postgres/migrations/`
- dispatcher queries under `internal/store/postgres/queries/`
- `internal/store/postgres/dispatcher_store.go`
- generated sqlc code
- PostgreSQL migration and concurrency tests
- existing job mapping where new nullable fields affect reads
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- existing pgx, sqlc, Goose, and Testcontainers dependencies

### Important decisions

- Add only the worker, eligibility, result, and attempt fields required by Milestone 2.
- Do not add lease expiration, heartbeat state, lease indexes, or recovery fields.
- Make identical registrations idempotent and reject conflicting reuse of a worker ID.
- Claim only eligible `queued` jobs supported by the requesting worker.
- Order claims by `available_at`, then `created_at`.
- Use `FOR UPDATE SKIP LOCKED`.
- Transition jobs, increment attempt numbers, assign workers, and insert attempts in one transaction.
- Enforce registered worker concurrency across concurrent acquisition calls.

### Validation required

- migration apply, rollback, and reapplication against fresh PostgreSQL
- registration retry and conflict tests
- concurrent real-PostgreSQL claim tests
- capacity enforcement tests
- `pwsh ./scripts/dev.ps1 generate`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 migration-test`
- `go test -count=1 ./internal/store/postgres/...`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- basic worker registration
- `AcquireJobs` persistence behavior
- atomic PostgreSQL claims
- attempt creation
- concurrent dispatcher-call safety
- required concurrent claimer coverage

### Decisions and deviations discovered during implementation

- Migration 3 adds the Milestone 2 subset of the planned schema: basic worker identity and process metadata; job result, eligibility, current-worker, and finish fields; attempt worker and finish fields; and the partial eligible-work index. Worker state, last-seen timestamps, leases, cancellation, retry, idempotency, and trace fields remain deferred.
- Identical registration uses one `INSERT ... ON CONFLICT` statement and preserves the stored row. Reusing a worker ID with different hostname, version, concurrency, or process start time returns `ErrWorkerRegistrationConflict`.
- `AcquireJobs` locks the registered worker row before counting that worker's running jobs. This serializes concurrent acquisitions for one worker and caps each claim by both advertised free capacity and remaining registered concurrency.
- The claim query filters by advertised job types, orders eligible queued jobs by `available_at` and `created_at`, locks them with `FOR UPDATE SKIP LOCKED`, and updates jobs plus inserts attempts in one PostgreSQL transaction.
- Result and finish columns are present for the Milestone 2 success path, but job result mapping, successful completion, and attempt-history reads remain in Slice 3.
- No architecture deviation was required. Slice 2 added no gRPC service, worker runtime, completion reporting, heartbeat, lease, recovery, retry, cancellation, metrics, or tracing behavior.

### Validation result

- `pwsh ./scripts/dev.ps1 generate` passed for sqlc and Protocol Buffer generation.
- `pwsh ./scripts/dev.ps1 generate-check` passed sqlc and Protocol Buffer generated-code verification.
- `pwsh ./scripts/dev.ps1 migration-test` passed migration apply, rollback to version zero, and reapplication against fresh PostgreSQL 18.6.
- `go test -count=1 ./internal/store/postgres/...` passed registration retry and conflict tests, eligibility and type filtering, ordering, capacity enforcement, and real-PostgreSQL concurrent claims. The concurrent claimer test claimed 100 jobs through eight workers and verified one unique running attempt per job. Eight concurrent acquisition calls for one worker produced only its three registered execution slots.
- `go vet ./internal/store/postgres/...` passed.
- `pwsh ./scripts/dev.ps1 check` passed module consistency, formatting, pinned tool checks, sqlc and Protocol Buffer generated-code checks, vet, all uncached tests, builds, Compose rendering, PostgreSQL integration tests, migrations through version 3, and the real API HTTP smoke test.
- `git diff --check` passed.
- Restricted cache access first produced `Access is denied` under `%LOCALAPPDATA%` for Go and Buf. The same commands passed with normal cache access; no code change was required for that environment error.
- GitHub-hosted CI was not run.

## Slice 3: successful completion and attempt history

Status: not started

### Goal

Complete the successful execution transition and expose attempt history through the HTTP API.

### Expected files and areas

- PostgreSQL completion and attempt-history queries
- `internal/store/postgres/dispatcher_store.go`
- job, attempt, and result domain types
- `internal/store/postgres/job_store.go`
- `internal/api/`
- store and handler tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 and 2

### Important decisions

- Support only `running` to `succeeded`.
- Finish the attempt and job in one transaction after verifying the worker and attempt identity.
- Treat an identical repeated success report as successful.
- Reject mismatched reports without changing state.
- Do not implement replacement attempts or stale reports after reassignment.
- Add successful results and finish timestamps to job lookup.
- Return attempts in ascending attempt order.

### Validation required

- PostgreSQL successful completion tests
- duplicate and mismatched report tests
- atomic job and attempt update tests
- empty, successful, and missing attempt-history HTTP tests
- `go test -count=1 ./internal/domain ./internal/api ./internal/store/postgres/...`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- successful reporting persistence
- successful job result storage
- attempt history API
- current attempt identity checks

### Decisions and deviations discovered during implementation

- None yet.

### Validation result

- Not run yet.

## Slice 4: dispatcher gRPC service and process

Status: not started

### Goal

Expose dispatcher persistence through a runnable, stateless gRPC process.

### Expected files and areas

- `internal/dispatcher/`
- `cmd/dispatcher/main.go`
- dispatcher service and lifecycle tests
- command-local configuration unless a shared package becomes justified
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 3
- gRPC runtime dependencies
- existing PostgreSQL pool

### Important decisions

- Keep PostgreSQL authoritative for workers, jobs, attempts, and transitions.
- Parse Protocol Buffer values into domain types at the RPC boundary.
- Return stable gRPC status codes for invalid input and state conflicts.
- Use unary RPCs and normal gRPC request concurrency.
- Shut down on process cancellation without adding heartbeat or recovery loops.

### Validation required

- RPC boundary and status-code tests
- real-PostgreSQL tests through a gRPC server
- concurrent `AcquireJobs` RPC tests
- dispatcher listener and shutdown tests
- `go test -count=1 ./internal/dispatcher ./cmd/dispatcher`
- `go test -count=1 ./internal/store/postgres/...`
- `pwsh ./scripts/dev.ps1 build`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- dispatcher gRPC server
- `RegisterWorker`
- `AcquireJobs`
- `ReportAttempt`
- concurrent dispatcher calls
- separate dispatcher binary

### Decisions and deviations discovered during implementation

- None yet.

### Validation result

- Not run yet.

## Slice 5: bounded worker and demonstration handlers

Status: not started

### Goal

Build a worker process that registers, pulls supported jobs based on free capacity, executes them with bounded concurrency, and reports success.

### Expected files and areas

- `internal/worker/`
- `internal/worker/handlers/`
- `cmd/worker/main.go`
- worker runtime, handler, and lifecycle tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 4
- generated gRPC client
- existing UUID and standard-library concurrency facilities

### Important decisions

- Generate a new worker UUID when each process starts.
- Use one acquisition loop, one bounded work channel, and exactly `N` executor goroutines.
- Count acquired but unfinished work when advertising free capacity.
- Use short polling with local jittered idle backoff.
- Hold a worker slot until successful reporting is acknowledged.
- Retry transient success-report failures.
- Provide `demo.echo` and `demo.payload_size` handlers that accept every valid JSON payload.
- Do not add heartbeat, timeout enforcement, panic recovery, failure classification, cancellation, or graceful draining semantics.

### Validation required

- bounded concurrency and capacity tests
- deterministic handler tests
- report identity tests
- `go test -count=1 ./internal/worker ./cmd/worker`
- `go test -race -count=1 ./internal/worker`
- `pwsh ./scripts/dev.ps1 build`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- worker process
- bounded execution pool
- multiple worker goroutines
- two deterministic demonstration handlers
- success reporting
- capacity-based pulling

### Decisions and deviations discovered during implementation

- None yet.

### Validation result

- Not run yet.

## Slice 6: distributed process acceptance test and developer flow

Status: not started

### Goal

Prove the Milestone 2 definition of done with real API, dispatcher, worker, and PostgreSQL processes.

### Expected files and areas

- `scripts/dev.ps1`
- process-level integration support
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 5
- Docker Compose PostgreSQL
- built API, dispatcher, and worker binaries

### Important decisions

- Start at least two worker processes with concurrency greater than one.
- Submit a batch that uses both demonstration job types.
- Poll the HTTP API until every job succeeds.
- Verify one successful attempt and the expected result for every job.
- Exercise concurrent worker acquisition through one dispatcher.
- Keep PostgreSQL as the only durable queue.
- Use bounded deadlines and safe process cleanup on Windows and Unix.
- Make the canonical `check` command verify protobuf generation and the distributed smoke path.

### Validation required

- repeat the distributed process test to catch timing assumptions
- `pwsh ./scripts/dev.ps1 distributed-test`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`
- `git status --short`

### Milestone requirements satisfied

- multiple worker processes
- multiple worker goroutines
- concurrent dispatcher calls
- batch execution through the dispatcher
- Milestone 2 definition of done

### Decisions and deviations discovered during implementation

- None yet.

### Validation result

- Not run yet.

## Milestone audit

Status: not started

After all slices pass, audit the implementation against the original Milestone 2 requirements and definition of done in `docs/project-plan.md`.

The audit must:

- run the complete repository validation,
- repeat the multi-process batch demonstration,
- inspect every demonstrated job and attempt in PostgreSQL,
- confirm that concurrent claims create one unique attempt for each logical claim,
- run worker race detection,
- inspect the diff for premature lease, heartbeat, recovery, retry, timeout, cancellation, or observability work,
- record decisions and deviations,
- update `docs/current-status.md`,
- move this plan to `docs/exec-plans/completed/milestone-2.md` only if the definition of done passes.
