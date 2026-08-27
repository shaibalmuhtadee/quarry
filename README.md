# Quarry

Quarry is a distributed job execution system written in Go. PostgreSQL stores jobs and attempts, the HTTP API accepts and reads jobs, the gRPC dispatcher claims work, and bounded worker processes run registered handlers.

Quarry provides at-least-once execution with leased attempts, durable retries, submission idempotency, execution timeouts, cooperative cancellation, and graceful worker draining. The dispatcher recovers unfinished work after a worker crash or forced shutdown and fences stale attempt reports.

## Requirements

Install these tools before you run the repository:

- Go 1.27.0
- PowerShell 7
- Docker Desktop with Linux containers

You do not need a host PostgreSQL installation. Go runs the pinned Goose and sqlc versions from `go.mod`.

## Run Quarry locally

Start PostgreSQL and apply the migrations from the repository root:

```powershell
pwsh ./scripts/dev.ps1 db-up
pwsh ./scripts/dev.ps1 migrate-up
```

Run the API, dispatcher, and at least one worker in separate terminals:

```powershell
go run ./cmd/api
go run ./cmd/dispatcher
go run ./cmd/worker
```

The API listens on `:8080`, and the dispatcher listens on `localhost:9090`. Submit a supported job from another terminal:

```powershell
$body = @{
    type = "demo.echo"
    payload = @{ message = "hello" }
    max_attempts = 3
    timeout_ms = 30000
} | ConvertTo-Json -Compress

$job = Invoke-RestMethod `
    -Method Post `
    -Uri "http://localhost:8080/v1/jobs" `
    -ContentType "application/json" `
    -Body $body

$job
do {
    Start-Sleep -Milliseconds 100
    $state = Invoke-RestMethod -Uri "http://localhost:8080/v1/jobs/$($job.id)"
} while ($state.status -ne "succeeded")

$state
Invoke-RestMethod -Uri "http://localhost:8080/v1/jobs/$($job.id)/attempts"
```

The job response includes `latest_failure` when an attempt has failed. The summary contains the stable error code and safe message from the newest failed attempt. The attempts response contains the same safe details for each failed attempt.

Workers also register these demonstration handlers:

- `demo.payload_size` returns the number of bytes in the JSON value received from the dispatcher.
- `demo.sleep` accepts `{"duration_ms": N}`, waits for that duration or context cancellation, and returns `{"slept_ms": N}`. The worker enforces the submitted job timeout around every handler.

To make a submission idempotent, send an `Idempotency-Key` header. Replaying the same job type and input returns the original job; changing the input for the same type and key returns `409 Conflict`.

Request cooperative cancellation with:

```powershell
Invoke-RestMethod -Method Post -Uri "http://localhost:8080/v1/jobs/$($job.id)/cancel"
```

The API also exposes liveness and readiness endpoints:

```powershell
Invoke-RestMethod -Uri "http://localhost:8080/healthz"
Invoke-RestMethod -Uri "http://localhost:8080/readyz"
```

Stop the API with Ctrl+C, then stop PostgreSQL:

```powershell
pwsh ./scripts/dev.ps1 db-down
```

The development PostgreSQL volume is retained across `db-down` and `db-up`.

## Configuration

The processes accept these environment variables:

- `QUARRY_DATABASE_URL`: PostgreSQL connection string. The default connects to the development database on port 5432.
- `QUARRY_HTTP_ADDR`: HTTP listen address. The default is `:8080`.
- `QUARRY_DISPATCHER_ADDR`: dispatcher gRPC listen address for the dispatcher and target address for workers. The default is `localhost:9090`.
- `QUARRY_LEASE_DURATION`: dispatcher lease duration. The default is `20s`.
- `QUARRY_REAPER_INTERVAL`: dispatcher expired-lease scan interval. The default is `1s`.
- `QUARRY_REAPER_BATCH_SIZE`: maximum jobs recovered in one transaction. The default is `100`.
- `QUARRY_WORKER_LIVENESS_TIMEOUT`: dispatcher worker-liveness threshold. The default matches `QUARRY_LEASE_DURATION`.
- `QUARRY_WORKER_CONCURRENCY`: positive worker executor count. The default is `4`.
- `QUARRY_WORKER_HOSTNAME`: worker registration hostname. The default is the operating-system hostname.
- `QUARRY_WORKER_VERSION`: worker registration version. The default is `dev`.
- `QUARRY_HEARTBEAT_INTERVAL`: worker heartbeat interval. The default is `5s`.
- `QUARRY_WORKER_SHUTDOWN_TIMEOUT`: time allowed for active attempts to drain after SIGTERM. The default is `10s`.
- `QUARRY_RETRY_BASE_DELAY`: dispatcher retry backoff base. The default is `1s`.
- `QUARRY_RETRY_MAX_DELAY`: dispatcher retry backoff maximum. The default is `60s`.
- `QUARRY_DISPATCHER_METRICS_ADDR`: dispatcher metrics listen address. The default is `:9464`.
- `QUARRY_WORKER_METRICS_ADDR`: worker metrics listen address. The default is an available local port.
- `OTEL_SERVICE_NAME`: OpenTelemetry service name. Each process has a `quarry-*` default.
- `OTEL_EXPORTER_OTLP_ENDPOINT`: base HTTP endpoint for OTLP traces. Quarry appends `/v1/traces`. The default is `http://localhost:4318`.
- `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`: complete HTTP endpoint for OTLP traces. This value overrides `OTEL_EXPORTER_OTLP_ENDPOINT`.

The development script also accepts `QUARRY_POSTGRES_PORT` to change the host port used by Docker Compose and migration commands. Set `QUARRY_DATABASE_URL` to the matching port when you run the API.

## Inspect metrics and traces

Start the observability services before you start the Quarry processes:

```powershell
pwsh ./scripts/dev.ps1 observability-up
```

Start one worker on the port in the committed Prometheus configuration:

```powershell
$env:QUARRY_WORKER_METRICS_ADDR = ":9465"
go run ./cmd/worker
```

Open these local pages:

- Grafana: `http://localhost:3000/d/quarry-overview/quarry`
- Jaeger: `http://localhost:16686`
- Prometheus: `http://localhost:9091`

The API exposes metrics at `http://localhost:8080/metrics`. The dispatcher exposes metrics on port `9464`, and the worker command above exposes metrics on port `9465`.

Use the Quarry dashboard to inspect queue depth, the oldest eligible job, active jobs, active workers, attempt outcomes, and timing data. To inspect one job, get its state and attempts through the API, search JSON logs for its `job_id`, then search Jaeger for the `job.id` tag. Jaeger shows the HTTP submission, PostgreSQL persistence, dispatcher claim, worker and handler execution, attempt report, and PostgreSQL completion in one trace.

Stop only the observability services with:

```powershell
pwsh ./scripts/dev.ps1 observability-down
```

Docker Compose accepts these host-port overrides: `QUARRY_PROMETHEUS_PORT`, `QUARRY_GRAFANA_PORT`, `QUARRY_OTEL_GRPC_PORT`, `QUARRY_OTEL_HTTP_PORT`, `QUARRY_OTEL_HEALTH_PORT`, and `QUARRY_JAEGER_PORT`. Prometheus still scrapes the committed application ports `8080`, `9464`, and `9465`.

## Validate the repository

Run the complete local validation:

```powershell
pwsh ./scripts/dev.ps1 check
```

This checks formatting, dependencies, pinned tools, generated code, static analysis, tests, builds, and the Compose configuration. It also runs the HTTP smoke test, distributed process test, failure suite, observability proof, and execution-semantics proof against PostgreSQL.

Run the short Milestone 6 benchmark proof by itself:

```powershell
pwsh ./scripts/dev.ps1 benchmark-smoke
```

The command starts isolated PostgreSQL, API, dispatcher, and worker processes. It runs a continuous warmup, measurement, and bounded drain for both benchmark workloads through the public HTTP API. Workload A uses `demo.echo` with deterministic seed and sequence fields. Workload B uses `demo.sleep` with the same deterministic fields and an exact `duration_ms: 25` request. Each run writes compressed raw samples, reads them back to generate its JSON summary, checks the required rates and latency samples, and removes every temporary process and Docker resource. The short smoke configuration is validation only; its output is not publishable benchmark evidence.

Run the Milestone 5 observability proof by itself:

```powershell
pwsh ./scripts/dev.ps1 observability-test
```

The observability test starts isolated PostgreSQL, Prometheus, Grafana, an OpenTelemetry Collector, Jaeger, and real Quarry processes. It verifies a successful job and a two-attempt timeout retry through the public API, metrics, logs, and Jaeger. The test then stops the Collector and proves that another job still completes. It also checks queue-health values, the three Prometheus targets, and the provisioned Grafana dashboard. The test removes its processes, temporary binaries, containers, network, and volume before it exits.

Run the Milestone 4 execution-semantics proof by itself:

```powershell
pwsh ./scripts/dev.ps1 semantics-test
```

The semantics test sends SIGTERM to real Linux worker processes, proves graceful drain and acquisition shutdown, forces a shutdown deadline, and verifies lease-based replacement execution. It also runs retry, timeout, panic, idempotency, and cancellation integrations against real PostgreSQL. The test checks stored attempt outcomes, retry eligibility, cancellation state, final job state, and cleanup of every temporary process and Docker resource.

Run the distributed process test by itself:

```powershell
pwsh ./scripts/dev.ps1 distributed-test
```

The distributed test builds temporary binaries and starts an isolated PostgreSQL database, one API process, one dispatcher process, and two worker processes with concurrency two. It submits 40 jobs across `demo.echo` and `demo.payload_size`, waits for every job to succeed, checks every result and attempt through HTTP and directly in PostgreSQL, and requires both workers to complete work. The test removes its processes, binaries, Compose volume, and network before it exits.

Run the worker-crash recovery test by itself:

```powershell
pwsh ./scripts/dev.ps1 recovery-test
```

The recovery test starts isolated API, dispatcher, worker, and PostgreSQL processes. It submits a long-running `demo.sleep` job, proves worker 1 renews attempt 1, kills that worker without graceful shutdown, and proves the lease and worker heartbeat stop advancing. Worker 2 then completes attempt 2. The test verifies both attempts through HTTP and PostgreSQL, runs a stale-report gRPC integration test, and checks that all temporary processes and Docker resources were removed.

Run the acknowledgement-loss proof by itself:

```powershell
pwsh ./scripts/dev.ps1 ack-loss-test
```

The acknowledgement-loss test starts a fault-enabled worker that appends one marker after handler success and exits before `ReportAttempt`. The test proves that attempt 1 remains unreported, its lease stops advancing, and PostgreSQL abandons it with `lease_expired`. A replacement worker appends the second marker and completes attempt 2. The test checks public attempt history, direct PostgreSQL state, and cleanup.

Run every required failure proof:

```powershell
pwsh ./scripts/dev.ps1 failure-test
```

The failure suite runs the worker-death recovery process test, the acknowledgement-loss process test, and the stale-completion gRPC and PostgreSQL test.

Run the HTTP smoke test by itself:

```powershell
pwsh ./scripts/dev.ps1 smoke-test
```

Run the restart persistence test by itself:

```powershell
pwsh ./scripts/dev.ps1 restart-test
```

The restart test starts a fresh PostgreSQL container, submits a job through one HTTP server and connection pool, tears both down, and retrieves the job through a newly constructed server and pool.

## Update generated database code

After you change a migration or query, regenerate the sqlc output and verify it:

```powershell
pwsh ./scripts/dev.ps1 generate
pwsh ./scripts/dev.ps1 generate-check
```

`generate-check` fails when the files in `internal/store/postgres/generated` differ from fresh sqlc output.

## Current limits

Cancellation and timeout are cooperative; Quarry cannot stop a handler that ignores its context. A forced worker shutdown leaves unfinished attempts for lease recovery, so duplicate execution remains possible under the at-least-once guarantee. The local Jaeger service stores traces in memory and loses them when the container stops.
