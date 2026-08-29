# Milestone 6 execution plan

## Milestone goal

Build a repeatable failure and benchmark suite that supports Quarry's performance and recovery claims with real measurements and preserved raw data.

Milestone 6 does not add application container images, a full-system Compose environment, Kubernetes resources, final CI expansion, architecture or guarantees documentation, workflow behavior, another queue, or any other Milestone 7 feature.

## Existing foundation

Milestones 0 through 5 already provide:

- durable jobs and attempts in PostgreSQL,
- the public HTTP API for submission, job lookup, attempt history, and cancellation,
- gRPC worker registration, acquisition, heartbeats, and outcome reporting,
- bounded worker concurrency,
- leases, lease recovery, and stale-attempt fencing,
- retries, timeouts, cancellation, and graceful worker shutdown,
- `demo.echo`, `demo.payload_size`, and context-aware `demo.sleep` handlers,
- a real worker-death process test,
- a real gRPC and PostgreSQL stale-completion test,
- Prometheus scheduling and execution histograms,
- job and attempt timestamps in public HTTP responses,
- rerunnable PowerShell process tests with isolated PostgreSQL and cleanup.

The repository has no Go load generator, acknowledgement-loss injection, benchmark runner, preserved benchmark data, or `docs/benchmarks.md`.

## Approved decisions

### Acknowledgement-loss injection

- Exercise the real `cmd/worker` binary rather than a separate helper binary.
- Add an off-by-default test hook after a successful handler return and before `ReportAttempt`.
- Make the hook fail only the first successful execution in that worker process.
- Activate the hook only through explicit `QUARRY_TEST_...` configuration.
- Register a test-only handler only when configuration supplies a marker-file path.
- Have the test-only handler append one marker before it returns success.
- Use a fault-enabled worker for attempt 1 and a normal replacement worker for attempt 2.
- Require two marker entries, an abandoned first attempt, and a successful second attempt.
- Do not inject faults in the dispatcher. Dropping a dispatcher response after commit tests idempotent reporting, not lost acknowledgement before commit.
- Do not change the database schema or worker-dispatcher protocol for fault injection.

### Failure-suite command

- Add focused `ack-loss-test` and aggregate `failure-test` commands to `scripts/dev.ps1`.
- Have `failure-test` run the existing worker-death recovery proof, the acknowledgement-loss proof, and the stale-completion proof.
- Add the deterministic acknowledgement-loss proof to canonical `check` only after it passes repeatedly as a standalone command.
- Keep benchmark campaigns outside canonical `check`.

### Load model

- Use a bounded closed-loop load model.
- Maintain a configurable maximum number of outstanding jobs.
- Submit a replacement when a job reaches a terminal state.
- Keep warmup and measurement continuous so connections, pools, workers, and PostgreSQL remain active at the phase boundary.
- Exclude warmup jobs from measured percentiles, but preserve their samples.
- Select the campaign's in-flight limit during smoke calibration.
- Hold that limit constant across the worker scaling matrix.
- Record the in-flight limit, polling interval, phase durations, workload, and seed in the campaign manifest.
- Treat an incomplete drain or a reached outstanding-job limit as benchmark evidence, not as a sample to discard.

An unbounded open-loop submitter could grow the durable queue until disk or memory pressure dominates the result. A bounded closed-loop run measures sustained asynchronous execution without hiding overload.

### Measurement definitions

- Define end-to-end latency as durable `finished_at - created_at` for a measured job.
- Define initial scheduling latency as attempt 1 `started_at - job.created_at` for Workloads A and B.
- Define attempt duration as `attempt.finished_at - attempt.started_at`.
- Record client-observed completion latency separately so polling delay remains visible.
- Calculate p50, p95, and p99 with the nearest-rank method over successful measured jobs.
- Count submitted jobs from successful committed `POST /v1/jobs` responses.
- Count completed jobs from measured jobs that reach a terminal state during the measurement window.
- Report submitted jobs/s and completed jobs/s separately.
- Do not present request-acceptance throughput as job throughput.

The Go load generator uses only the public HTTP API. It does not read PostgreSQL or call the dispatcher.

### Workloads

- Map Workload A to `demo.echo` with deterministic small payloads.
- Map Workload B to `demo.sleep` with `duration_ms: 25`.
- Use longer context-aware `demo.sleep` jobs for Workload C.
- Run Workloads A and B across 1, 2, 4, and 8 worker processes with concurrency 8 per worker.
- Run Workload C as a controlled two-worker experiment with concurrency 8 per worker.
- Keep one Workload C worker ready as a replacement before killing the target worker.
- Repeat every publishable configuration three times.

The full scaling matrix applies to Workloads A and B. Workload C isolates lease recovery with one killed worker and one ready replacement. This keeps recovery time distinct from worker-startup time and avoids multiplying the same failure proof across configurations.

### Recovery measurements

- Record the worker termination time in UTC.
- Identify the killed worker and the jobs whose first attempts ran on it.
- Report termination to replacement-attempt start as the primary recovery duration.
- Report termination to final job success as a secondary recovery duration.
- Require the first attempt to end as `abandoned` with `lease_expired`.
- Require the replacement attempt to succeed.
- Preserve every affected job and attempt sample.

### Raw data and summaries

- Store publishable campaigns under `benchmarks/results/<campaign-id>/`.
- Store a versioned campaign manifest with the exact Git commit, worktree state, machine specification, Go version, Docker version, PostgreSQL image, Quarry configuration, workload parameters, and timestamps.
- Store per-job samples as compressed JSON Lines.
- Store raw resource samples separately from per-job samples.
- Generate one summary for each run from its raw samples.
- Generate campaign medians from the three run summaries.
- Never hand-copy or choose the best run's numbers.
- Add a verification command that regenerates summaries and fails on a mismatch.

### Resource measurements

- Read Quarry process CPU and resident memory from the existing Prometheus process metrics.
- Read PostgreSQL CPU and memory from `docker stats`.
- Read database connection counts from `pg_stat_activity`.
- Preserve the raw time series.
- Report the aggregation rule for every resource value in `docs/benchmarks.md`.
- Keep Go services on the host and PostgreSQL in Docker during Milestone 6.
- Defer application container images and full-system Compose startup to Milestone 7.

### Campaign commands and evidence gate

- Add `benchmark-smoke` for short validation runs that do not produce publishable numbers.
- Add `benchmark` for the fixed 30-second warmup, 120-second measurement, three-run campaign.
- Add `benchmark-verify` to recompute committed summaries from raw data.
- Do not run `benchmark` from canonical `check`.
- Require a clean Git revision for a publishable campaign.
- Record any failed or incomplete run instead of deleting it.
- Use only successful complete runs in the three-run median. Rerun an invalid configuration and preserve the invalid run beside the replacement.

## Slice 1: deterministic post-handler fault injection

Status: complete

### Goal

Create an off-by-default seam that stops a worker after a successful handler side effect but before any outcome report.

### Expected files and areas

- `internal/worker/worker.go`
- worker tests under `internal/worker/`
- `internal/worker/handlers/`
- `cmd/worker/main.go`
- `cmd/worker/main_test.go`
- `docs/current-status.md`
- this execution plan

### Dependencies

- the existing handler execution and `reportUntilAcknowledged` boundary
- worker command configuration
- temporary files owned by the process-test harness

### Important decisions

- Invoke the hook only after a handler returns a successful result.
- Invoke it before `ReportAttempt` can start.
- Make the hook single-use within the worker process.
- Return a distinct injected failure so `cmd/worker` exits unsuccessfully.
- Register the marker-file handler only when explicit test configuration supplies its path.
- Keep the production registry and default worker behavior unchanged.
- Do not add a general plugin or fault-injection framework.

### Validation required

- successful execution without the hook still reports once
- the injected path records the handler side effect and sends no outcome report
- unsuccessful, timed-out, cancelled, and panicked handlers do not trigger the success hook
- the hook fires at most once
- missing, partial, or invalid test configuration fails before worker startup
- default worker configuration cannot register the test-only handler or trigger the fault
- `go test -count=1 ./internal/worker/... ./cmd/worker`
- relevant worker race tests
- `go vet ./internal/worker/... ./cmd/worker`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- fault-injection foundation
- acknowledgement-loss failure-mode foundation

### Decisions and deviations discovered during implementation

- The worker seam is an optional `worker.Config.TestAfterHandlerSuccess` callback. A worker-local `sync.Once` limits the callback to the first successful handler outcome.
- `QUARRY_TEST_SIDE_EFFECT_FILE` registers `test.side_effect`. `QUARRY_TEST_EXIT_AFTER_HANDLER_SUCCESS=true` also enables the post-handler failure and requires the marker path. Configuration validation runs before worker startup.
- The test-only handler appends `completed\n` to the configured marker file and syncs the file before it returns success. The default handler registry still contains only the three demonstration handlers.
- No architecture or scope deviation was required.

### Validation evidence

- `TestWorkerPostHandlerSuccessHookStopsBeforeReport` observed the handler side effect before the injected error and observed zero outcome reports.
- `TestWorkerPostHandlerSuccessHookIgnoresNonSuccessOutcomes` covered retryable failure, permanent failure, cancellation, timeout, and panic outcomes. None invoked the hook.
- `TestWorkerPostHandlerSuccessHookRunsAtMostOnce` invoked the seam concurrently and observed one hook call.
- `TestLoadConfigDefaultsAndOverrides`, `TestLoadConfigRejectsInvalidTestFaultConfig`, `TestRegistryContainsOnlyDemonstrationHandlers`, and `TestRunTestFaultWritesMarkerAndStopsBeforeReport` covered default isolation, startup validation, conditional handler registration, the marker side effect, and the pre-report worker failure.
- `go test -count=1 ./internal/worker/... ./cmd/worker` passed.
- `docker run --rm --volume "${PWD}:/src" --workdir /src golang:1.27.0-bookworm go test -race -count=1 ./internal/worker/... ./cmd/worker` passed. The native Windows race command could not run because the local Go toolchain has CGO disabled.
- `go vet ./internal/worker/... ./cmd/worker` passed.
- `pwsh ./scripts/dev.ps1 check` passed, including package tests, builds, static checks, observability validation, distributed execution, worker-death recovery, and shutdown semantics.
- `git diff --check` passed.
- GitHub Actions did not run for this slice.

## Slice 2: acknowledgement-loss process proof and failure suite

Status: complete

### Goal

Prove duplicate execution after handler success is lost before acknowledgement. Group every required failure proof under one command.

### Expected files and areas

- `scripts/dev.ps1`
- process-test helpers in `scripts/dev.ps1`
- focused tests needed by the process harness
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- existing `recovery-test`
- existing stale-completion gRPC and PostgreSQL integration test
- PostgreSQL and the public job and attempt APIs

### Important decisions

- Add focused `ack-loss-test` and aggregate `failure-test` commands.
- Start the fault-enabled worker before the replacement worker.
- Require one marker before the fault-enabled worker exits.
- Start a normal replacement worker and require a second marker.
- Verify public attempt history and direct PostgreSQL state.
- Use short test leases and reaper intervals only inside the isolated test.
- Add the acknowledgement-loss proof to `check` only after repeated standalone passes.
- Preserve the focused `recovery-test` command.
- Verify cleanup of processes, temporary binaries, marker files, containers, networks, and volumes.

### Validation required

- attempt 1 starts on the fault-enabled worker
- the handler records its side effect before that worker exits
- no attempt-1 success commits
- attempt 1 stops heartbeating and expires
- attempt 1 becomes `abandoned` with `lease_expired`
- attempt 2 runs on the replacement worker and succeeds
- the marker file contains exactly two entries
- public attempt history exposes both executions
- stale completion remains fenced
- `pwsh ./scripts/dev.ps1 ack-loss-test` passes twice consecutively
- `pwsh ./scripts/dev.ps1 failure-test`
- relevant worker and dispatcher race tests
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- fault injection
- acknowledgement-loss failure mode
- duplicate side-effect evidence
- worker-death failure proof
- stale-completion proof
- failure suite

### Decisions and deviations discovered during implementation

- A marker path alone is valid test configuration because the replacement `cmd/worker` needs the test-only handler without the injected exit. Setting `QUARRY_TEST_EXIT_AFTER_HANDLER_SUCCESS` without a marker path remains invalid. Default workers still register only demonstration handlers.
- `failure-test` runs `Test-RecoveryProcesses`, `Test-AcknowledgementLossProcesses`, and the focused stale-completion integration test once each. The existing `recovery-test` command still runs its original recovery and stale-completion proofs.
- `check` now runs `failure-test` coverage. The acknowledgement-loss command passed twice consecutively before this canonical integration.
- All process tests use one shared cleanup assertion for host processes, temporary directories, Compose containers, networks, and volumes. The acknowledgement-loss temporary directory owns the marker file and binaries, so one bounded removal cleans them together.
- The existing shutdown-semantics fixture completed before Docker-backed SQL polling could observe two lease values. Live worker logs showed a successful 1.5-second execution. The fixture now sleeps for 6 seconds and uses an 8-second graceful shutdown budget. No runtime behavior changed.
- No architecture, database, or RPC deviation was required.

### Validation evidence

- Two consecutive `pwsh ./scripts/dev.ps1 ack-loss-test` runs passed. Each run observed attempt 1 on the fault-enabled worker, one durable marker before its nonzero exit, no committed outcome report, unchanged lease and worker heartbeat timestamps after exit, `abandoned` with `lease_expired`, successful attempt 2 on a distinct replacement worker, exactly two marker entries, matching public attempt history, exact PostgreSQL state, and complete cleanup.
- `pwsh ./scripts/dev.ps1 failure-test` passed the worker-death recovery process proof, the acknowledgement-loss process proof, and `TestStaleAttemptReportAfterRecoveryThroughGRPCAndPostgres`.
- `docker run --rm --volume "${PWD}:/src" --volume /var/run/docker.sock:/var/run/docker.sock --env TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal --workdir /src golang:1.27.0-bookworm go test -race -count=1 ./internal/worker/... ./cmd/worker ./internal/dispatcher` passed.
- `go test -count=1 ./cmd/worker ./internal/worker/...` passed after the handler-only configuration change.
- Two consecutive standalone `pwsh ./scripts/dev.ps1 semantics-test` runs passed after lengthening the deterministic fixture.
- `pwsh ./scripts/dev.ps1 check` passed with the aggregate failure suite and shutdown-semantics proof in the canonical path.
- `git diff --check` passed.
- GitHub Actions did not run for this slice.

## Slice 3: load-generator core and measurement format

Status: complete

### Goal

Add a bounded Go client that submits jobs through HTTP, observes terminal state, retrieves attempt history, and writes recomputable samples.

### Expected files and areas

- new `cmd/loadgen/`
- new `internal/loadgen/`
- unit and HTTP integration tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- the existing public HTTP API
- Go standard-library HTTP, JSON, compression, sorting, and time support

### Important decisions

- Keep HTTP transport and CLI parsing at the command boundary.
- Put the bounded run loop, sample types, percentile calculation, and output encoding in `internal/loadgen`.
- Bound both outstanding jobs and concurrent HTTP requests.
- Use one reusable `http.Client` and transport.
- Keep the load generator ignorant of PostgreSQL, gRPC, worker identities, and internal Go domain types.
- Version the raw sample schema.
- Preserve failed submissions, terminal failures, malformed responses, polling errors, and incomplete drains.
- Use nearest-rank percentiles.
- Add no substantial dependency.

### Validation required

- successful submission, completion observation, and attempt-history retrieval
- bounded outstanding jobs and bounded HTTP concurrency
- warmup and measurement attribution
- terminal failure and incomplete-drain samples
- retryable HTTP and transport error behavior
- malformed response handling
- raw JSON Lines round trip
- compressed sample output round trip
- percentile behavior for empty, one-value, and boundary-size inputs
- submitted and completed rate calculations use different counters
- `go test -count=1 ./internal/loadgen ./cmd/loadgen`
- `go test -race -count=1 ./internal/loadgen`
- `go vet ./internal/loadgen ./cmd/loadgen`
- `go build ./cmd/loadgen`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- Go load generator
- asynchronous completion observation
- benchmark-output foundation
- completed jobs/s calculation
- p50, p95, and p99 calculation

### Decisions and deviations discovered during implementation

- `internal/loadgen.Runner` starts one closed-loop slot per configured outstanding job. Each slot owns at most one accepted job, while a shared semaphore limits concurrent client calls independently.
- The runner derives one idempotency key from the run ID and job sequence. It retries ambiguous submissions with the same key. If an ambiguous submission cannot be resolved, that slot stops so Quarry cannot exceed the configured outstanding-job limit.
- The raw schema is version 1 and uses distinct submission-failure, terminal-job, and incomplete-job records. The encoder writes compressed JSON Lines. The strict decoder rejects unknown fields, unsupported schema versions, invalid statuses, contradictory fields, and invalid attempt histories.
- Terminal records contain the public attempt history. Request-error records preserve the operation, observation time, retryability, commit ambiguity, and message for submission, polling, and attempt-history failures.
- `cmd/loadgen` owns CLI validation and strict HTTP response parsing. The internal package depends only on its client contract and does not import Quarry's domain, API, dispatcher, worker, gRPC, or PostgreSQL packages.
- Submitted and completed rates use separate counters. Percentiles use durable public timestamps and the nearest-rank method. Warmup samples remain in raw output but do not enter measured summaries.
- Slice 3 accepts a generic job type and JSON payload. Workload A, Workload B, the benchmark commands, resource sampling, campaign manifests, and recovery measurements remain deferred to their approved slices.
- No architecture or implementation deviation was required.

### Validation evidence

- `go test -count=1 ./internal/loadgen ./cmd/loadgen` passed. Tests cover the public HTTP submission, polling, and attempt-history flow; both concurrency bounds; warmup and measurement attribution; terminal failures; incomplete drains; retryable transport and HTTP errors; malformed successful responses; raw and gzip round trips; strict schema rejection; nearest-rank boundaries; and separate submitted and completed rates.
- The command-level test ran `cmd/loadgen` wiring against an HTTP server, wrote `.jsonl.gz` samples, decoded the file through the versioned reader, and checked the generated summary counters.
- `go test -count=10 ./internal/loadgen ./cmd/loadgen` passed before the final schema tightening. The final focused and canonical runs passed after that change.
- Windows reported `go: -race requires cgo`. `docker run --rm -v "${PWD}:/src" -w /src golang:1.27.0-bookworm go test -race -count=1 ./internal/loadgen` passed against the final code.
- `go vet ./internal/loadgen ./cmd/loadgen` passed.
- `go build ./cmd/loadgen` passed.
- The first canonical check attempt hit a transient Testcontainers `rootless Docker is not supported on Windows` error in existing dispatcher integration tests. `go test -count=1 ./internal/dispatcher` passed unchanged against Docker Desktop. Two later `pwsh ./scripts/dev.ps1 check` runs passed in full, including all Go packages, generation checks, Compose smoke, observability, distributed execution, worker-death recovery, acknowledgement loss, stale completion, shutdown semantics, and cleanup.
- `git diff --check` passed. A direct trailing-whitespace scan also covered the new untracked files.
- GitHub-hosted CI was not run and remains unverified.

## Slice 4: queue-overhead and simulated-I/O workloads

Status: complete

### Goal

Implement reproducible Workloads A and B with continuous warmup, measurement, and drain phases.

### Expected files and areas

- `cmd/loadgen/`
- `internal/loadgen/`
- `scripts/dev.ps1`
- workload and process tests
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 3
- existing `demo.echo` handler
- existing context-aware `demo.sleep` handler

### Important decisions

- Use `demo.echo` for Workload A.
- Use `demo.sleep` with `duration_ms: 25` for Workload B.
- Give every payload a deterministic sequence value where the handler contract permits it.
- Keep warmup and measurement continuous.
- Stop new submissions after measurement and drain outstanding measured jobs within a bounded timeout.
- Preserve warmup samples but exclude them from measured percentiles.
- Add `benchmark-smoke` with short override durations and reduced configurations.
- Do not treat smoke output as publishable benchmark evidence.

### Validation required

- exact Workload A and B payload generation
- deterministic sequence and seed behavior
- exact 25 ms requested sleep duration
- phase attribution at warmup and measurement boundaries
- bounded drain and explicit incomplete-drain reporting
- summary regeneration from raw samples
- `go test -count=1 ./internal/loadgen ./cmd/loadgen`
- `go test -race -count=1 ./internal/loadgen`
- `pwsh ./scripts/dev.ps1 benchmark-smoke`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- reproducible Workload A
- reproducible Workload B
- public-API load generation
- benchmark output
- completed jobs/s
- end-to-end p50, p95, and p99
- scheduling p50, p95, and p99
- attempt duration measurement

### Decisions and deviations discovered during implementation

- Workload selection is a typed command-boundary input. Workload A submits `demo.echo` with `{"seed":N,"sequence":N}`. Workload B submits `demo.sleep` with `{"duration_ms":25,"seed":N,"sequence":N}`. Payload bytes depend only on the workload, seed, and sequence, so concurrent slot scheduling cannot change them.
- The load-generator command now accepts `-workload a|b` and `-seed` instead of arbitrary job-type and payload flags. This keeps the benchmark command on the two approved workloads; recovery Workload C remains deferred to Slice 6.
- The raw sample schema advanced from version 1 to version 2. Every sample now carries its run ID and measurement window. `cmd/loadgen` closes and reopens the compressed raw file, regenerates the run summary from decoded samples, and then writes the JSON summary. Campaign manifests and cross-run verification remain deferred to Slice 5.
- Warmup and measurement use one uninterrupted runner. Submission starts are attributed to half-open phase windows, no new job starts at or after the measurement end, and already accepted jobs drain until the configured deadline. Warmup samples remain in raw output and have separate counts but never enter measured rates or percentiles.
- `benchmark-smoke` uses one worker process with concurrency 4, four outstanding jobs, a 750 ms warmup, a 1.5-second measurement, and a five-second drain. It runs both workloads through the public HTTP API, checks raw and generated summary artifacts, and deletes the temporary artifacts and isolated Docker resources. Its results are explicitly non-publishable.
- No architecture deviation or new dependency was required.

### Validation evidence

- `go test -count=1 ./internal/loadgen ./cmd/loadgen` passed against the final code. Tests cover exact Workload A and B payload bytes, seed and sequence determinism, the exact 25 ms sleep request, continuous warmup and measurement samples, boundary attribution, bounded concurrency and drain behavior, explicit incomplete samples, strict schema version 2 round trips, and summary regeneration from decoded gzip samples.
- Native Windows reported `go: -race requires cgo`. `docker run --rm -v "${PWD}:/src" -w /src golang:1.27.0-bookworm go test -race -count=1 ./internal/loadgen` passed against the Slice 4 code.
- Two consecutive `pwsh ./scripts/dev.ps1 benchmark-smoke` runs passed. Both ran real API, dispatcher, worker, load-generator, and PostgreSQL processes for Workloads A and B; preserved warmup and measurement samples; reported successful measured completions with no submission, terminal, or incomplete failures; and verified removal of processes, temporary files, containers, network, and volume. The temporary rates were discarded and are not publishable benchmark evidence.
- Two `pwsh ./scripts/dev.ps1 check` runs passed. The final run validated the exact final code and included formatting, dependency and generation checks, vet, all Go tests and builds, Compose smoke, observability, distributed execution, worker-death recovery, acknowledgement loss, stale completion, shutdown semantics, and cleanup.
- `git diff --check` passed before the tracking update; the final diff check also passed after it.
- GitHub-hosted CI was not run and remains unverified.

## Slice 5: scaling matrix, resource samples, and campaign summaries

Status: complete

### Goal

Drive the 1, 2, 4, and 8-worker matrix at concurrency 8. Capture machine, process, PostgreSQL, and campaign metadata with generated three-run medians.

### Expected files and areas

- `scripts/dev.ps1`
- `cmd/loadgen/`
- `internal/loadgen/`
- benchmark manifest and summary types
- aggregation tests and fixtures
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 4
- existing Prometheus process metrics
- Docker PostgreSQL
- `docker stats`
- PostgreSQL `pg_stat_activity`

### Important decisions

- Run API, dispatcher, workers, and load generator as host binaries.
- Run only PostgreSQL and existing observability infrastructure in Docker.
- Use 1, 2, 4, and 8 worker processes with concurrency 8 per worker.
- Hold the selected in-flight limit constant across the matrix.
- Record machine and software versions before the first run.
- Sample Quarry CPU and memory from Prometheus process metrics.
- Sample PostgreSQL CPU and memory from `docker stats`.
- Sample database connections from `pg_stat_activity`.
- Preserve raw resource time series.
- Generate each run summary from raw samples.
- Generate configuration medians from three valid run summaries.
- Preserve invalid runs and their failure reasons.
- Do not add application images or Kubernetes resources.

### Validation required

- a shortened one-run matrix with at least two worker configurations
- exact worker-process and concurrency values in every manifest
- stable in-flight value across configurations
- machine, Git, Go, Docker, PostgreSQL, and Quarry configuration metadata
- raw CPU, memory, and database-connection samples
- deterministic summary regeneration
- median selection from three fixture summaries
- rejection of missing, duplicated, mixed-configuration, or malformed run data
- cleanup after a failed configuration
- `go test -count=1 ./internal/loadgen ./cmd/loadgen`
- `pwsh ./scripts/dev.ps1 benchmark-smoke`
- `pwsh ./scripts/dev.ps1 benchmark-verify` against smoke fixtures
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- worker scaling
- CPU measurement
- memory measurement
- database-connection measurement
- machine specification
- three-run aggregation
- raw-data preservation
- benchmark-procedure foundation

### Decisions and deviations discovered during implementation

- Campaign, run, and resource schemas live in `internal/loadgen`. The package strictly decodes external JSON, validates the fixed worker counts and concurrency, requires one in-flight limit across the campaign, and rejects duplicate run IDs, directories, and configuration repetitions.
- `cmd/benchmarkctl` regenerates run summaries from compressed job samples and raw resource samples. It also generates three-run configuration medians and compares persisted summaries with regenerated values. PowerShell owns process orchestration and external sampling, so the HTTP load generator remains ignorant of PostgreSQL, Docker, worker identities, and internal service state.
- Every resource sample records each host service's Prometheus `process_cpu_seconds_total` and `process_resident_memory_bytes`, PostgreSQL container CPU and memory from `docker stats`, and Quarry database connections from `pg_stat_activity`. Run summaries report Quarry CPU-counter change divided by elapsed sample time, peak summed Quarry resident memory, average PostgreSQL CPU percentage, peak PostgreSQL memory, and peak database connections.
- `benchmark-smoke` runs Workloads A and B once at 1 and 2 worker processes, concurrency 8, with a fixed in-flight limit of 8. It also injects an intentional failed configuration, preserves its failure reason until verification, and proves that its worker process stops.
- `benchmark` drives the approved Workload A and B matrix at 1, 2, 4, and 8 worker processes, concurrency 8, with three repetitions, a 30-second warmup, and a 120-second measurement. It captures the Git state before creating the results directory and refuses publishable runs from a dirty worktree. Workload C remains deferred to Slice 6.
- `benchmark-verify` exercises deterministic three-run fixtures and verifies any campaigns under `benchmarks/results/`. Invalid runs remain in the manifest and their directories remain present, but only three valid runs contribute to a configuration median.
- No application image, Kubernetes resource, substantial dependency, architecture deviation, or Milestone 7 feature was added.

### Validation evidence

- `go test -count=1 ./internal/loadgen ./cmd/loadgen ./cmd/benchmarkctl` passed against the final code. Tests cover strict manifest and resource parsing, exact matrix configuration, stable in-flight limits, raw resource round trips, resource aggregation, three-run median selection, command-level regeneration, tamper detection, and rejection of missing, duplicate, mixed-configuration, and malformed data.
- `docker run --rm --volume "${PWD}:/src" --workdir /src golang:1.27.0-bookworm go test -race -count=1 ./internal/loadgen` passed. Native Windows race testing remains unavailable because the local Go toolchain has CGO disabled.
- `go vet ./internal/loadgen ./cmd/loadgen ./cmd/benchmarkctl` and `go build ./cmd/loadgen ./cmd/benchmarkctl` passed.
- `pwsh ./scripts/dev.ps1 benchmark-verify` passed against the deterministic three-run campaign fixtures after the final changes.
- Two standalone `pwsh ./scripts/dev.ps1 benchmark-smoke` runs passed. The final run used the exact final harness and completed Workloads A and B at both 1 and 2 worker processes with concurrency 8 and max outstanding 8. Each of the four runs preserved two measurement-window resource samples, regenerated its summary, and reported successful measured completions with no failed or incomplete jobs. The command verified the intentional failed-configuration cleanup and removed every temporary file, host process, container, network, and volume.
- `pwsh ./scripts/dev.ps1 check` passed against the final code. It included formatting, dependency and generation checks, vet, all Go tests and builds, Compose smoke, observability, distributed execution, worker-death recovery, acknowledgement loss, stale completion, shutdown semantics, and cleanup.
- `git diff --check` passed after the final implementation changes and again after the tracking update.
- GitHub-hosted CI was not run and remains unverified.

## Slice 6: controlled worker-kill recovery benchmark

Status: complete

### Goal

Implement Workload C and measure recovery after terminating a real worker during execution.

### Expected files and areas

- `cmd/loadgen/`
- `internal/loadgen/`
- `scripts/dev.ps1`
- recovery sample and summary types
- recovery process tests
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 2
- Slice 5
- existing lease reaper and worker-kill process helpers
- existing context-aware `demo.sleep` handler
- public job and attempt history

### Important decisions

- Use two worker processes with concurrency 8 per worker.
- Start and verify the replacement worker before killing the target worker.
- Kill the target only after its long-running attempts are visible.
- Record the kill time and killed worker ID.
- Derive recovery samples only from jobs whose first attempt ran on the killed worker.
- Measure kill-to-attempt-2-start as the primary recovery duration.
- Measure kill-to-final-success as a secondary recovery duration.
- Require attempt 1 to be abandoned with `lease_expired`.
- Require attempt 2 to succeed.
- Preserve raw samples for every affected job.

### Validation required

- the target worker owns attempt 1 before termination
- the replacement worker is ready before termination
- the target worker stops without graceful shutdown
- attempt 1 stops heartbeating and becomes abandoned
- attempt 2 starts on the replacement worker and succeeds
- stale attempt-1 completion cannot overwrite attempt 2
- kill-to-attempt-2-start and kill-to-success are positive and recomputable
- raw recovery samples contain both attempts and the killed worker ID
- two consecutive shortened recovery benchmark passes
- `pwsh ./scripts/dev.ps1 benchmark-smoke` with the recovery workload
- `pwsh ./scripts/dev.ps1 failure-test`
- relevant worker and dispatcher race tests
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- reproducible Workload C
- worker-kill recovery benchmark
- recovery-duration measurement
- raw recovery evidence

### Decisions and deviations discovered during implementation

- Workload C uses deterministic six-second `demo.sleep` jobs. PowerShell starts only the target worker, waits until it owns the full in-flight batch, starts and verifies the replacement worker, samples both workers, force-kills the target, and records the observed termination time.
- `cmd/loadgen` continues to use only the public HTTP API for job submission and observation. PowerShell uses PostgreSQL only to coordinate the fault at the point where the target owns all attempt-1 jobs. The load generator accepts the resulting recovery event as a strict external input.
- Raw job sample schema version 3 embeds the killed worker ID and termination time on each affected job. Recovery validation requires a measured `demo.sleep` job, an attempt-1 `abandoned` outcome with `lease_expired`, a successful attempt 2 on a distinct worker, positive recovery durations, and a final job timestamp equal to attempt 2's finish timestamp.
- Run summary schema version 2 represents throughput and recovery summaries as distinct variants. Recovery summaries recompute kill-to-attempt-2-start and kill-to-success percentiles from raw attempt timestamps. Campaign summaries compute the median of each recovery percentile across three valid runs.
- Resource aggregation permits a process to disappear while preserving a fixed set of process identities across the run. It calculates CPU from each process's first and last observed counters and does not carry a terminated worker's last metrics into later raw samples.
- `benchmark-smoke` now runs Workload C once with two workers, concurrency 8, max outstanding 8, a two-second lease, and a 250 ms heartbeat. `benchmark` includes three Workload C repetitions with the documented 30-second warmup, 120-second measurement, 20-second lease, and five-second heartbeat. Each campaign manifest records the actual configuration.
- No database, protocol, application-runtime, architecture, or Milestone 7 change was required.

### Validation evidence

- `go test -count=1 ./internal/loadgen ./cmd/loadgen ./cmd/benchmarkctl` passed against the final code. Tests cover Workload C payloads, strict recovery-event parsing, affected-job selection, raw schema round trips, contradictory recovery evidence, recovery-duration recomputation, worker disappearance in resource samples, Workload C manifest constraints, run-summary regeneration, campaign medians, CLI boundary validation, and existing benchmark-controller behavior.
- Two consecutive `pwsh ./scripts/dev.ps1 benchmark-smoke` runs passed against the exact final code. Runs `smoke-bb6ee699b2e04ed1956c20f3fb2279d3-c-w2-r1` and `smoke-74e02d1c90e54e80bce4410385c0d0bb-c-w2-r1` each preserved and regenerated eight affected recovery jobs. Each run verified the killed worker, one distinct replacement worker, lease-expired attempt 1, successful attempt 2, positive recovery durations, at least two resource samples, all five A, B, and C smoke configurations, and complete process, file, container, network, and volume cleanup.
- `pwsh ./scripts/dev.ps1 benchmark-verify` passed deterministic throughput and recovery summary fixtures and found no committed campaign results to verify.
- `pwsh ./scripts/dev.ps1 failure-test` passed the real worker-death recovery proof, acknowledgement-loss proof, stale-completion gRPC and PostgreSQL proof, and cleanup.
- Native Windows race testing remains unavailable because the local toolchain has CGO disabled. `docker run --rm --volume "${PWD}:/src" --workdir /src golang:1.27.0-bookworm go test -race -count=1 ./internal/loadgen` passed. A second Docker command passed focused worker and dispatcher race tests without host Docker-socket access. The broader Docker-socket race command was denied by the execution environment, so package-wide dispatcher integration race coverage was not run.
- `go vet ./internal/loadgen ./cmd/loadgen ./cmd/benchmarkctl` and `go build ./cmd/loadgen ./cmd/benchmarkctl` passed.
- PowerShell parsed `scripts/dev.ps1` without errors.
- `pwsh ./scripts/dev.ps1 check` passed against the final code. It included formatting, dependency and generated-code checks, vet, all Go tests and builds, observability validation, distributed execution, worker-death recovery, acknowledgement loss, stale completion, shutdown semantics, and cleanup.
- `git diff --check` passed after the final implementation and tracking updates. The worktree contains only Slice 6 source, test, command, README, execution-plan, and status changes. No benchmark result or Milestone 7 file was created.
- GitHub-hosted CI did not run for this slice.

## Slice 7: publishable campaign and benchmark documentation

Status: complete

### Goal

Preserve invalid campaigns, complete one publishable campaign, and document its results without presenting the measurements as production capacity.

### Expected files and areas

- `benchmarks/invalid/`
- `benchmarks/results/quarry-20260829T002429Z/`
- `docs/benchmarks.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 6
- the artifacts from three failed full campaigns
- the recovery ownership-race fix in commit `9067502e6445cf191ea316b1790c4d323fb8ab42`

### Important decisions

- Keep each failed campaign under `benchmarks/invalid/` with its original manifest, failure record, and produced artifacts.
- Publish only a campaign that started from a clean commit and completed three valid runs for every configuration.
- Keep the fixed maximum of eight outstanding jobs visible beside every scaling claim.
- Report local measurements as evidence for this system and machine, not as production capacity.

### Validation required

- three failed campaigns remain under `benchmarks/invalid/` with manifests, failure records, and produced run artifacts
- one campaign under `benchmarks/results/` contains 27 valid runs and a generated campaign summary
- `pwsh ./scripts/dev.ps1 benchmark-verify`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- failed and incomplete benchmark evidence remains preserved
- one complete publishable campaign with three valid runs per configuration
- published completed jobs/s, latency, scheduling, execution, scaling, resource, and recovery measurements
- benchmark summary regeneration
- benchmark documentation and qualified resume evidence

### Decisions and deviations discovered during implementation

- The first two incomplete campaigns remain preserved as described below. Neither contributes to published measurements or medians.
- The first attempt started from clean commit `57bfff19343a03c795216f75c82394aae4ed80fb`. All 24 Workload A and B runs completed. Workload C repetition 1 exited with code 1. The original runner captured neither stderr nor job samples before recovery filtering, so the preserved artifacts do not establish the cause. Workload C repetitions 2 and 3 did not run. The failure record's earlier incorrect full commit hash was corrected against the campaign manifest and Git object.
- A subsequent unchanged smoke run exposed a separate timing defect: Workload C completed, but fewer than two resource samples fell inside its six-second measurement window. The recovery smoke measurement is now 20 seconds. The publishable 30-second warmup and 120-second measurement remain unchanged.
- Every benchmark load-generator process now writes stdout and stderr beside its other run artifacts. Workload C also writes its unfiltered compressed samples before attaching recovery metadata, so an invalid recovery run preserves the data and exact error needed for diagnosis.
- The second attempt started from clean commit `e649d16dc32f302b691fda87955fbcb6ea8b861d`. All 24 Workload A and B runs and Workload C repetition 1 completed. Workload C repetition 1 has a generated recovery summary for eight affected jobs. Repetition 2 exited with code 1 because the recovery run had no terminal jobs from the killed worker. Its compressed samples, stdout, stderr, resource samples, and recovery event remain preserved. Repetition 3 did not run.
- A third campaign started from clean commit `51a6cbd6211fa58e989c5def605c3dac8a3e5929`. Its first two Workload C runs completed, but repetition 3 exposed an ownership race between resource sampling and the worker-kill selection. The preserved failure record explains that the harness sampled resources before selecting the current owner after the fix.
- The publishable campaign `quarry-20260829T002429Z` started from clean commit `9067502e6445cf191ea316b1790c4d323fb8ab42`. It completed all 27 runs: three repetitions of Workloads A and B at 1, 2, 4, and 8 workers, plus three two-worker Workload C repetitions.
- `docs/benchmarks.md` reports campaign medians, the machine and software configuration, recovery timing, resource observations, limitations, and reproduction commands.

### Validation evidence

- `go test -count=1 ./cmd/loadgen ./internal/loadgen ./cmd/benchmarkctl` passed after the recovery evidence repair. `TestRunPreservesUnfilteredRecoverySamplesWhenEventAttachmentFails` directly verified that an invalid recovery attachment still leaves readable compressed job samples and does not write a success summary.
- Two consecutive repaired `pwsh ./scripts/dev.ps1 benchmark-smoke` runs passed. Each completed Workloads A and B at one and two workers, preserved two measurement-window resource samples per throughput run, recovered eight Workload C jobs after killing the target worker, regenerated all five valid summaries, and removed its processes, temporary directory, container, network, and volume.
- Direct artifact inspection found all three invalid-campaign manifests and failure records. The publishable manifest records a clean worktree, the current Git commit, machine and software details, 27 valid runs, 30-second warmups, and 120-second measurement windows.
- `pwsh ./scripts/dev.ps1 benchmark-verify` passed against deterministic fixtures and campaign `quarry-20260829T002429Z`. It regenerated and matched all run and campaign summaries from raw data.
- The first sandboxed `pwsh ./scripts/dev.ps1 check` attempt stopped at `go mod tidy -diff` because the sandbox denied access to the Windows Go build cache. The rerun with normal host access passed. It covered formatting, dependency and generation checks, vet, all Go tests and builds, PostgreSQL integration tests, observability, distributed execution, worker-death recovery, acknowledgement loss, stale completion, shutdown semantics, and cleanup.
- `git diff --check` passed after the campaign preservation and execution-plan update.

## Milestone audit

Status: complete

### Findings

- The failure suite uses real worker, dispatcher, API, and PostgreSQL processes. Recorded Slice 2 and Slice 6 evidence covers worker death, acknowledgement loss after handler success, lease-expired abandonment, replacement execution, stale-completion fencing, and process and Docker cleanup.
- The Go load generator uses the public HTTP API. Workloads A and B ran at 1, 2, 4, and 8 worker processes with concurrency 8 and a fixed maximum of eight outstanding jobs. Workload C used one killed worker and one ready replacement worker.
- Campaign `quarry-20260829T002429Z` contains three valid runs for all nine configurations. Every run used a 30-second warmup and a 120-second measurement window.
- Raw compressed job samples and resource JSON Lines support regeneration of submitted and completed jobs/s, nearest-rank latency percentiles, scheduling delay, attempt duration, Quarry CPU and memory, PostgreSQL CPU and memory, connection counts, and recovery timing.
- The campaign manifest records clean commit `9067502e6445cf191ea316b1790c4d323fb8ab42`, the machine and software specification, runtime lease settings, and every run configuration.
- Three incomplete campaigns remain under `benchmarks/invalid/` with their failure reasons and produced artifacts. They do not contribute to published results.
- `docs/benchmarks.md` reports the publishable medians and states the fixed-load and single-machine limits. Resume claims must keep those limits.
- No Milestone 7 application image, full-system Compose environment, Kubernetes resource, final CI expansion, or workflow behavior was added.

### Validation evidence

- `pwsh ./scripts/dev.ps1 benchmark-verify` passed on 2026-08-29 against deterministic fixtures and the publishable campaign. It regenerated and matched every run summary and the campaign summary.
- `pwsh ./scripts/dev.ps1 failure-test`, relevant Linux race tests, and `pwsh ./scripts/dev.ps1 check` passed against the benchmark implementation during Slices 2, 5, 6, and 7 as recorded above.
- The publishable campaign started from the current clean `HEAD`, `9067502e6445cf191ea316b1790c4d323fb8ab42`.
- The user stopped further audit reruns after the publishable campaign because the audit had already consumed hours. No new full `failure-test`, `check`, or race run was performed during closure.
- GitHub-hosted CI remains unverified.
