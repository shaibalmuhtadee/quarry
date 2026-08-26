# Milestone 5 execution plan

## Milestone goal

Add metrics, traces, structured identifiers, and local observability infrastructure around Quarry's existing execution path. A developer must be able to diagnose queue health and inspect one submitted job without reading PostgreSQL rows manually.

Milestone 5 does not add load generation, benchmarks, benchmark results, acknowledgement-loss fault injection, application container images, Kubernetes resources, workflow behavior, another queue, or any other Milestone 6 or Milestone 7 feature.

## Existing foundation

Milestones 0 through 4 already provide:

- durable jobs and attempts in PostgreSQL,
- HTTP submission, lookup, attempt history, and cancellation,
- gRPC worker registration, acquisition, heartbeats, and outcome reports,
- bounded worker execution,
- leases, stale-attempt fencing, and crash recovery,
- durable retries, backoff, and dead-letter behavior,
- execution timeouts, panic recovery, and cooperative cancellation,
- graceful worker drain and forced-shutdown recovery,
- JSON `log/slog` output from every process,
- API request logs that can include `job_id`,
- worker startup and panic logs with some execution identifiers,
- rerunnable process tests and a common PowerShell validation interface.

The repository has no Prometheus client use, `/metrics` route, OpenTelemetry setup, persisted trace context, Collector, Jaeger configuration, Grafana configuration, or dashboard. OpenTelemetry modules currently listed in `go.mod` are indirect dependencies of development tools and tests.

## Approved decisions

### Trace continuation across asynchronous work

- Persist one W3C `traceparent` value with each logical job.
- Do not persist `tracestate`, baggage, arbitrary request headers, or vendor-specific propagation data.
- Preserve the original job trace context on an idempotent submission replay.
- Treat missing or invalid incoming trace context as absent instead of storing malformed text.
- Carry `traceparent` as opaque execution metadata in `AcquiredJob`. Workers remain unaware of PostgreSQL.
- Instrument worker-to-dispatcher gRPC calls, but do not use the acquisition polling span as the parent of every acquired job.
- After a batch claim commits, start one `dispatcher.claim` span per job from that job's persisted trace parent.
- Inject the `dispatcher.claim` span context into the matching `AcquiredJob` response.
- Start `worker.execute` from the context carried by that job.
- Let the attempt context carry the trace through handler execution, outcome reporting, and durable completion.

One `AcquireJobs` call can claim jobs from different traces. A single polling RPC cannot be the parent of all of them. Per-job continuation keeps each trace valid and makes one job readable as one trace in Jaeger.

### Telemetry ownership and failure behavior

- Add a small `internal/telemetry` package for process telemetry setup, Prometheus instruments, context-aware logging, trace propagation helpers, and metrics HTTP serving.
- Give each process its own Prometheus registry and OpenTelemetry provider.
- Keep telemetry outside application correctness. Metrics, logs, and traces never become authoritative state.
- Fail process startup for invalid telemetry configuration.
- Do not fail job submission, acquisition, execution, reporting, or recovery because an exporter or collector is unavailable.
- Shut down application work before flushing and stopping telemetry.
- Use standard OpenTelemetry environment variables where they fit the required configuration.
- Use service-specific metrics listener configuration for dispatcher and worker processes.
- Expose the API's `/metrics` route on its existing HTTP server.
- Run a small metrics-only HTTP server beside the dispatcher and each worker.

### Structured logs

- Keep `log/slog` as the logging API.
- Wrap the configured handler so records logged with a trace context receive `trace_id` and `span_id`.
- Add `job_id`, `job_type`, `attempt_no`, `worker_id`, `outcome`, and safe `error_code` fields where the event has those values.
- Log attempt start, completion, retry scheduling, stale reports, cancellation, and lease recovery at their owning component.
- Do not log payloads, idempotency keys, trace headers, unsafe handler errors, arbitrary panic values outside the existing protected panic log, or public error messages as metric labels.

### Metric semantics

- Record event metrics only after the related PostgreSQL transaction commits or the worker observes the completed local action.
- Return small typed transition facts from dispatcher store operations when the caller needs the committed outcome for metrics or logs.
- Mark exact repeated reports as already applied so they do not increment counters again.
- Query queue-health gauges from PostgreSQL at scrape time. Do not maintain authoritative queue gauges in process memory.
- Use PostgreSQL time for scheduling delay and queue-age calculations.
- Use bounded labels only.
- Do not use `job_id`, `worker_id`, `idempotency_key`, `error_message`, payload content, URLs, or arbitrary input as metric labels.
- Do not label API submission or queue gauges by `job_type`. The public API accepts client-controlled job types, so that label is not bounded there.
- Label attempt and execution metrics by `job_type` only after the job matches the worker's finite handler registry.
- Preserve the Milestone 4 worker-state decision. `quarry_active_workers` may retain a stopped process until the reaper marks it lost.

The metrics have these meanings:

| Metric | Owner and meaning | Labels |
| --- | --- | --- |
| `quarry_jobs_submitted_total` | API count of newly committed logical jobs | none |
| `quarry_job_attempts_total` | Dispatcher count of newly committed terminal attempts | `job_type`, `outcome`, `error_code` |
| `quarry_job_execution_duration_seconds` | Worker duration of one handler invocation | `job_type`, `outcome` |
| `quarry_job_scheduling_delay_seconds` | Dispatcher claim time minus durable `available_at` | `job_type` |
| `quarry_queue_depth` | PostgreSQL count of `queued` and `retry_wait` jobs | `status` |
| `quarry_oldest_queued_job_age_seconds` | PostgreSQL age of the oldest currently eligible pending job | none |
| `quarry_active_jobs` | PostgreSQL count of `running` jobs | none |
| `quarry_active_workers` | PostgreSQL count of workers still marked `active` | none |
| `quarry_lease_expirations_total` | Dispatcher count of committed expired attempts | `outcome` |
| `quarry_retries_scheduled_total` | Dispatcher count of committed transitions to `retry_wait` | `reason` |
| `quarry_stale_reports_total` | Dispatcher count of fenced attempt reports | none |
| `quarry_dispatch_claim_size` | Dispatcher count of jobs in each successful acquisition result | none |
| `quarry_worker_poll_errors_total` | Worker count of failed acquisition calls | `error_code` |

The PostgreSQL collector may add one bounded health metric for snapshot-query success. A failed snapshot must not appear as a valid cached value.

### Queue-health definitions

- `quarry_queue_depth` reports separate `queued` and `retry_wait` values.
- `quarry_oldest_queued_job_age_seconds` considers only jobs that are eligible at scrape time.
- A future `retry_wait` job does not appear overdue before its `available_at` time.
- PostgreSQL calculates age from its own current time.
- Grafana uses `max`, not `sum`, for database snapshot gauges so future dispatcher replicas do not multiply one shared value.

### Local observability infrastructure

- Add pinned Prometheus, Grafana, OpenTelemetry Collector, and Jaeger images to Compose.
- Do not use `latest` image tags.
- Keep Go services on the host during Milestone 5.
- Configure Prometheus to scrape the host API, dispatcher, and one demonstration worker.
- Support Docker Desktop and Linux host access through `host.docker.internal` plus a host-gateway mapping.
- Configure the Collector to receive OTLP traces and export them to Jaeger.
- Provision the Prometheus Grafana datasource and the Quarry dashboard from committed files.
- Do not require manual Grafana setup.
- Defer API, dispatcher, and worker container images and full-system Compose startup to Milestone 7.

## Slice 1: telemetry runtime and endpoints

Status: complete

### Goal

Create the shared telemetry runtime, configuration, context-aware logging support, and Prometheus endpoints. Do not add job-specific instruments or spans yet.

### Expected files and areas

- new files under `internal/telemetry/`
- `cmd/api/main.go` and tests
- `cmd/dispatcher/main.go` and tests
- `cmd/worker/main.go` and tests
- `internal/api/handler.go`
- `go.mod`
- `go.sum`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Prometheus Go client
- OpenTelemetry API and SDK
- OTLP trace exporter
- official OpenTelemetry HTTP instrumentation
- official OpenTelemetry gRPC instrumentation
- existing `log/slog`, HTTP, gRPC, and process-shutdown code

### Important decisions

- Build one telemetry runtime per process.
- Use a custom Prometheus registry instead of the package-global registry.
- Register standard Go and process collectors explicitly.
- Add the API `/metrics` route to the existing HTTP server.
- Run metrics-only HTTP servers for dispatcher and worker.
- Give metrics servers bounded read, write, idle, and shutdown timeouts.
- Use a `slog.Handler` wrapper to add trace and span identifiers from `context.Context`.
- Keep telemetry startup and shutdown explicit in each command.
- Do not add job-specific metrics or tracing in this slice.

### Validation required

- telemetry configuration default and override tests
- invalid telemetry configuration tests
- registry isolation tests
- metrics endpoint content tests
- metrics server shutdown tests
- tracer-provider shutdown tests
- context-aware `slog` handler tests
- existing command lifecycle regression tests
- `go test -count=1 ./internal/telemetry ./cmd/api ./cmd/dispatcher ./cmd/worker`
- `go test -race -count=1 ./internal/telemetry`
- `go vet ./internal/telemetry ./cmd/api ./cmd/dispatcher ./cmd/worker`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- telemetry configuration foundation
- Prometheus endpoint foundation
- OpenTelemetry runtime foundation
- trace identifiers in context-aware logs

### Decisions and deviations discovered during implementation

- The OTLP HTTP exporter defaults to `http://localhost:4318/v1/traces`. `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` overrides the full trace endpoint, while `OTEL_EXPORTER_OTLP_ENDPOINT` supplies the base URL. Invalid endpoint URLs fail configuration before process startup.
- The dispatcher metrics listener defaults to `:9464`. The worker listener defaults to an ephemeral port because local tests and normal development can run several workers on one host. `QUARRY_WORKER_METRICS_ADDR` can pin the demonstration worker to a Prometheus scrape port in Slice 6.
- Trace-exporter shutdown failures produce a warning after application shutdown instead of changing successful application work into a process failure. Metrics listener startup and shutdown errors still fail the owning process because they indicate a local lifecycle error.
- No architecture deviation was required. Job-specific metrics, HTTP and gRPC instrumentation, spans, trace persistence, and observability infrastructure remain deferred to their approved slices.

### Validation evidence

- `go test -count=1 ./internal/telemetry ./cmd/api ./cmd/dispatcher ./cmd/worker` passed on Windows.
- `go vet ./internal/telemetry ./cmd/api ./cmd/dispatcher ./cmd/worker` passed on Windows.
- Native Windows `go test -race` could not run because the installed Go toolchain has no CGO compiler. `docker run --rm -v "${PWD}:/src" -w /src golang:1.27.0-bookworm go test -race -count=1 ./internal/telemetry` passed on Linux.
- `pwsh ./scripts/dev.ps1 check` passed after the worker metrics listener default changed from a fixed port to an ephemeral port. The command passed package tests, builds, static checks, generated-code checks, migration checks, the API smoke path, the multi-worker distributed test, crash recovery, and the Milestone 4 semantics process test.
- `git diff --check` passed.
- GitHub-hosted CI was not run.

## Slice 2: committed event metrics

Status: complete

### Goal

Instrument submissions, claims, terminal attempts, retries, lease expiration, stale reports, handler duration, claim sizes, and worker poll errors.

### Expected files and areas

- `internal/telemetry/metrics.go` and tests
- `internal/api/handler.go`
- `internal/dispatcher/service.go`
- `internal/dispatcher/reaper.go`
- `internal/store/postgres/dispatcher_store.go`
- `internal/worker/worker.go`
- existing API, dispatcher, store, and worker tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- existing durable attempt transitions
- existing typed attempt outcomes and safe failure codes
- existing worker acquisition and handler boundaries

### Important decisions

- Add typed committed-transition results only where exact metric ownership requires them.
- Record submission metrics only for newly inserted logical jobs.
- Do not count deduplicated replays as new jobs.
- Record terminal attempt metrics only for newly applied transitions.
- Keep exact repeated reports idempotent for both state and metrics.
- Count a retry only when the committed job state becomes `retry_wait`.
- Record handler duration after the handler returns, times out, or panics.
- Map gRPC failures to bounded canonical codes for poll-error labels.
- Keep metrics calls unable to return application errors.

### Validation required

- exact metric-family and label tests through a test registry
- new submission versus deduplicated replay tests
- repeated report does not double-count
- retryable, permanent, exhausted, timeout, panic, cancellation, and success outcome tests
- lease-expiry retry, dead-letter, and cancellation metric tests
- stale-report metric tests
- worker poll-error metric tests
- handler-duration observation tests
- claim-size tests for empty and non-empty successful acquisitions
- `go test -count=1 ./internal/api ./internal/dispatcher ./internal/store/postgres/... ./internal/worker/...`
- relevant worker and dispatcher race tests
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- Prometheus event counters
- Prometheus execution and scheduling histograms
- attempt-outcome diagnosis
- retry, lease, stale-report, claim, and worker-error visibility

### Decisions and deviations discovered during implementation

- PostgreSQL returns the scheduling delay from `statement_timestamp() - available_at` as part of the committed claim query. This keeps the measurement on the same database clock that controls eligibility.
- Attempt-report and lease-recovery store methods return typed transition facts after commit. Exact repeated reports return `Applied: false`, so state and metrics remain idempotent.
- Handler duration is observed once per completed invocation before report retries. Worker poll errors use lowercase canonical gRPC status codes.
- Lease-expiration outcomes use the final durable job status. A retry is counted only for `retry_wait`, with `lease_expired` as the reason.
- The expected file set expanded to the dispatcher SQL query and generated sqlc output because committed scheduling delay and job type must come from the durable transition. No architecture deviation was required.
- PostgreSQL queue-health gauges, trace persistence and spans, dashboards, and observability infrastructure remain deferred to Slices 3 through 7.

### Validation evidence

- Exact metric-family and label tests, new versus deduplicated submission tests, idempotent repeated-report tests, all terminal outcome tests, lease-expiry outcome tests, stale-report tests, handler-duration tests, poll-error tests, and empty/non-empty claim-size tests passed in their owning packages.
- `pwsh ./scripts/dev.ps1 generate-check` passed after regenerating the sqlc query output.
- `go test -count=1 ./internal/telemetry ./internal/api ./internal/dispatcher ./internal/store/postgres/... ./internal/worker/...` passed on Windows, including real PostgreSQL transition and scheduling-delay tests.
- `go vet ./internal/telemetry ./internal/api ./internal/dispatcher ./internal/store/postgres/... ./internal/worker/...` passed on Windows.
- Native Windows race tests remain unavailable because the installed toolchain has no CGO compiler. `docker run --rm -v "${PWD}:/src" -w /src golang:1.27.0-bookworm go test -race -count=1 ./internal/telemetry ./internal/worker/...` passed on Linux. The focused dispatcher service and reaper race command also passed; package-wide dispatcher race execution was not used because its Testcontainers integration tests cannot start sibling containers from inside that container.
- `pwsh ./scripts/dev.ps1 check` passed. It covered package tests, builds, static checks, generated-code checks, migrations, API smoke behavior, distributed execution, crash recovery, and shutdown semantics.
- `git diff --check` passed.
- GitHub-hosted CI was not run.

## Slice 3: PostgreSQL queue-health metrics

Status: not started

### Goal

Expose authoritative queue depth, oldest eligible job age, running-job count, and active-worker count.

### Expected files and areas

- a new observability query under `internal/store/postgres/queries/`
- generated sqlc code
- PostgreSQL queue-snapshot adapter
- Prometheus collector under `internal/telemetry/`
- dispatcher command wiring and tests
- real PostgreSQL integration tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1
- existing job and worker state
- sqlc
- pgx
- Testcontainers PostgreSQL support

### Important decisions

- Query all queue-health values from one PostgreSQL snapshot when practical.
- Report separate `queued` and `retry_wait` queue-depth values.
- Calculate oldest age only from currently eligible pending jobs.
- Use PostgreSQL time for age calculations.
- Report zero when no eligible pending job exists.
- Preserve the existing worker liveness and lost-worker transition semantics.
- Expose snapshot-query failure through one bounded collector-health metric.
- Do not return stale cached database values as current gauges.

### Validation required

- empty queue produces zero gauges
- queued and retry-wait jobs produce exact depth values
- a future retry does not affect oldest eligible age
- an eligible retry affects oldest eligible age
- running jobs produce the exact active-job value
- worker active-to-lost transition changes the active-worker value
- collector query failure does not panic or stop dispatcher work
- real PostgreSQL concurrency does not produce inconsistent negative values
- `go test -count=1 ./internal/telemetry ./internal/store/postgres/... ./cmd/dispatcher`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- queue-health Prometheus gauges
- database-authoritative operational diagnosis
- queue-health part of the Milestone 5 definition of done

### Decisions and deviations discovered during implementation

None yet.

### Validation evidence

Not run.

## Slice 4: durable trace-context contract

Status: not started

### Goal

Persist the submission trace context and carry it through PostgreSQL, the dispatcher, and the worker contract without enabling the complete trace path yet.

### Expected files and areas

- new migration `internal/store/postgres/migrations/00008_add_job_traceparent.sql`
- `internal/store/postgres/queries/jobs.sql`
- `internal/store/postgres/queries/dispatcher.sql`
- generated sqlc code
- `internal/store/postgres/job_store.go`
- `internal/store/postgres/dispatcher_store.go`
- `proto/quarry/dispatcher/v1/dispatcher.proto`
- generated Protocol Buffer and gRPC code
- `internal/dispatcher/service.go`
- `internal/worker/grpc_client.go`
- `internal/worker/worker.go`
- migration, store, contract, dispatcher, and client tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slice 1 trace propagation helper
- existing submission and idempotency behavior
- existing claim transaction and `AcquiredJob` path
- Goose
- sqlc
- Buf
- real PostgreSQL integration tests

### Important decisions

- Add one nullable `jobs.traceparent` column.
- Store only a valid W3C `traceparent` value.
- Leave the column null when no valid trace context exists.
- Preserve the first submission's trace context during an idempotent replay.
- Return the stored value from the atomic claim query.
- Carry it in one new `AcquiredJob` field.
- Treat the field as opaque metadata until the worker telemetry boundary extracts it.
- Keep workflow, scheduling, and database details out of the RPC.

### Validation required

- migration apply, rollback, and reapplication
- valid submission context persists
- missing or invalid context persists as null
- idempotent replay preserves the original context
- claim returns the stored context
- Protocol Buffer round-trip tests
- gRPC service and client mapping tests
- invalid acquired context does not corrupt worker state
- `go test -count=1 ./internal/store/postgres/... ./internal/dispatcher ./internal/worker ./internal/rpc`
- `pwsh ./scripts/dev.ps1 migration-test`
- `pwsh ./scripts/dev.ps1 generate-check`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- trace-context persistence across asynchronous job execution
- execution contract needed by the required trace demonstration

### Decisions and deviations discovered during implementation

None yet.

### Validation evidence

Not run.

## Slice 5: end-to-end tracing and lifecycle logs

Status: not started

### Goal

Produce one connected trace from HTTP submission through persistence, claim, worker execution, handler invocation, outcome reporting, and durable completion. Add consistent identifiers to lifecycle logs.

### Expected files and areas

- tracing support under `internal/telemetry/`
- `cmd/api/main.go`
- `cmd/dispatcher/main.go`
- `cmd/worker/main.go`
- `internal/api/handler.go`
- `internal/api/logging.go`
- `internal/store/postgres/job_store.go`
- `internal/store/postgres/dispatcher_store.go`
- `internal/dispatcher/service.go`
- `internal/dispatcher/reaper.go`
- `internal/worker/worker.go`
- HTTP, gRPC, store, worker, tracing, and logging tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 and 4
- official OpenTelemetry HTTP instrumentation
- official OpenTelemetry gRPC instrumentation
- OpenTelemetry in-memory span recorder for tests
- existing process contexts and attempt-lifetime contexts

### Important decisions

- Instrument inbound HTTP and both sides of gRPC.
- Add manual spans for `db.insert_job`, `dispatcher.claim`, `worker.execute`, `handler`, and `db.complete_attempt`.
- Start each `dispatcher.claim` span from its stored trace parent after the claim transaction commits.
- Inject the new per-job context into the acquired job.
- Start the worker attempt context from the acquired job context.
- Let report RPC instrumentation carry the attempt context back to the dispatcher.
- Add `job.id`, `job.type`, `job.attempt`, `worker.id`, and `job.outcome` trace attributes where available.
- Add lifecycle logs for claim, attempt start, completion, retry scheduling, stale report, cancellation, and lease recovery.
- Keep payloads, idempotency keys, raw trace headers, and unsafe errors out of logs and span attributes.

### Validation required

- in-memory exporter proves the representative spans share one trace ID
- parentage crosses the persisted asynchronous boundary
- one batch containing jobs from different traces preserves separate trace IDs
- worker execution and handler spans use the matching job trace
- report and database-completion spans remain under the attempt trace
- required trace attributes appear on the correct spans
- logs contain available job, attempt, worker, trace, outcome, and safe failure identifiers
- unsafe payload, handler error, and idempotency values do not appear in logs or trace attributes
- focused HTTP, gRPC, worker, and real PostgreSQL integration tests
- relevant worker and dispatcher race tests
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- OpenTelemetry tracing
- required end-to-end trace path in code
- structured identifiers in logs
- single-job inspection foundation

### Decisions and deviations discovered during implementation

None yet.

### Validation evidence

Not run.

## Slice 6: Prometheus, Grafana, Collector, and Jaeger

Status: not started

### Goal

Run the local observability infrastructure and provision a useful Quarry Grafana dashboard.

### Expected files and areas

- `compose.yaml`
- new Prometheus configuration under `deploy/observability/`
- new OpenTelemetry Collector configuration under `deploy/observability/`
- Grafana datasource provisioning
- Grafana dashboard provisioning
- Quarry dashboard JSON
- Jaeger service configuration
- `scripts/dev.ps1` infrastructure commands
- configuration validation tests
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 5
- pinned Prometheus image
- pinned Grafana image
- pinned OpenTelemetry Collector image
- pinned Jaeger image
- Docker Compose

### Important decisions

- Pin compatible image versions and document them in Compose.
- Do not add application images.
- Run the Go services on the host.
- Scrape the API, dispatcher, and one demonstration worker through the host gateway.
- Provision the Prometheus datasource and dashboard from committed files.
- Export Collector traces to Jaeger.
- Use `max` for shared PostgreSQL snapshot gauges.
- Include panels for queue depth, oldest eligible age, active jobs, active workers, submissions, attempt outcomes, scheduling delay, execution duration, retries, lease expirations, stale reports, claim size, and worker poll errors.
- Keep every dashboard query free of unbounded labels.

### Validation required

- `docker compose config --quiet`
- Prometheus configuration validation
- Collector configuration startup
- Grafana health check
- Grafana datasource provisioning check
- Grafana dashboard provisioning check
- Jaeger health and trace-ingestion check
- Prometheus reports the three Go service targets healthy
- infrastructure shutdown and cleanup verification
- `git diff --check`

### Milestone requirements satisfied

- Prometheus infrastructure
- Grafana dashboard
- OpenTelemetry Collector
- Jaeger
- local telemetry configuration
- visual queue-health diagnosis

### Decisions and deviations discovered during implementation

None yet.

### Validation evidence

Not run.

## Slice 7: observability demonstration and documentation

Status: not started

### Goal

Add one rerunnable command that proves the Milestone 5 workflow without manual PostgreSQL inspection, then document how to use it.

### Expected files and areas

- `scripts/dev.ps1`
- process-test helpers
- focused observability integration tests
- `README.md`
- `docs/current-status.md`
- this execution plan

### Dependencies

- Slices 1 through 6
- existing process-test and cleanup helpers
- public Quarry HTTP API
- Prometheus HTTP API
- Grafana HTTP API
- Jaeger HTTP API

### Important decisions

- Add `pwsh ./scripts/dev.ps1 observability-test`.
- Start isolated PostgreSQL and observability infrastructure.
- Start real API, dispatcher, and worker processes with telemetry enabled.
- Submit one deterministic job through the public API.
- Verify completion through the public API.
- Verify the required metric families and queue-health values through Prometheus.
- Find the job trace in Jaeger by `job.id`.
- Require spans for submission, persistence, claim, worker execution, handler execution, report, and completion.
- Verify that Grafana provisioned the Quarry dashboard.
- Add `observability-test` to `check` only after two consecutive standalone passes.
- Do not collect benchmark throughput, load-test latency percentiles, recovery measurements, or benchmark output.

### Validation required

- `pwsh ./scripts/dev.ps1 observability-test` twice
- `pwsh ./scripts/dev.ps1 check`
- relevant Linux race-detector command for worker and dispatcher concurrency
- process, container, network, volume, and temporary-file cleanup checks
- `git diff --check`
- `git status --short`

### Milestone requirements satisfied

- required submitted-job trace demonstration
- queue health without direct PostgreSQL inspection
- one-job inspection through the API, logs, and Jaeger
- documented telemetry configuration
- complete Milestone 5 definition of done, subject to the separate audit

### Decisions and deviations discovered during implementation

None yet.

### Validation evidence

Not run.

## Milestone audit

Status: not started

After every slice is complete, perform a separate audit against `docs/project-plan.md`. Completed slice statuses are not proof that the milestone is complete.

The audit must:

- compare the implementation with the complete observability section and Milestone 5 definition of done,
- inspect every metric's meaning and labels for cardinality, stale values, or double counting,
- inspect a real successful job trace,
- inspect at least one retry or lease-expiry trace,
- verify the trace parentage across persistence and batch acquisition,
- verify that telemetry failures cannot change job state,
- verify that logs contain safe structured identifiers and no unsafe values,
- verify queue diagnosis through Prometheus and Grafana without direct PostgreSQL inspection,
- verify one-job inspection through the public API, logs, and Jaeger,
- run `pwsh ./scripts/dev.ps1 observability-test`,
- run `pwsh ./scripts/dev.ps1 check`,
- run relevant race tests,
- run generated-code and migration checks,
- inspect the diff for unnecessary scope,
- confirm that no Milestone 6 load generator, benchmark, benchmark data, benchmark documentation, or acknowledgement-loss fault injection exists,
- record all observed evidence in this plan and `docs/current-status.md`.

Only after the audit passes:

- mark Milestone 5 complete in `docs/current-status.md`,
- move this file to `docs/exec-plans/completed/milestone-5.md`.
