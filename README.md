# Quarry

Quarry is a distributed job execution system written in Go. It accepts asynchronous jobs over HTTP, stores authoritative state in PostgreSQL, and sends leased attempts to bounded workers over gRPC.

The V1 demonstrates concurrent claims, retries, idempotent submission, cooperative cancellation, execution timeouts, graceful shutdown, worker-crash recovery, stale-attempt fencing, metrics, traces, containers, Kubernetes, and measured load. Quarry provides at-least-once execution. It does not claim exactly-once side effects or production high availability.

- [Architecture](docs/architecture.md) explains component ownership, data flow, failure recovery, and deployment topology.
- [Guarantees and limits](docs/guarantees.md) states what Quarry does and does not guarantee.
- [Benchmark evidence](docs/benchmarks.md) contains the verified local campaign, raw-data method, and reproduction commands.
- [Current status](docs/current-status.md) records verified project progress and known limitations.

## Requirements

For the Compose demonstration, install PowerShell 7 and Docker Desktop with Linux containers.

The kind demonstration also requires kind v0.32.0 or newer and kubectl with built-in Kustomize support. Repository validation requires Go 1.27.0.

List the primary commands:

```powershell
pwsh ./scripts/dev.ps1 help
```

## Run the complete Compose stack

From the repository root, start PostgreSQL, migrations, the API, two workers, the dispatcher, Prometheus, Grafana, the OpenTelemetry Collector, and Jaeger:

```powershell
docker compose up --build --detach --wait --scale worker=2
```

Submit a job and wait for its terminal state:

```powershell
$body = @{
    type = "demo.echo"
    payload = @{ message = "hello from Quarry" }
    max_attempts = 3
    timeout_ms = 30000
} | ConvertTo-Json -Compress

$job = Invoke-RestMethod `
    -Method Post `
    -Uri "http://localhost:8080/v1/jobs" `
    -ContentType "application/json" `
    -Body $body

do {
    Start-Sleep -Milliseconds 200
    $state = Invoke-RestMethod -Uri "http://localhost:8080/v1/jobs/$($job.id)"
} until ($state.status -in @("succeeded", "dead_lettered", "cancelled"))

if ($state.status -ne "succeeded") {
    throw "Job $($job.id) finished as $($state.status)."
}

$attempts = Invoke-RestMethod -Uri "http://localhost:8080/v1/jobs/$($job.id)/attempts"
$state
$attempts
```

The final job contains `result.message = "hello from Quarry"`. Its attempt history contains one succeeded attempt with a worker ID.

Inspect the running system:

- Grafana dashboard: [http://localhost:3000/d/quarry-overview/quarry](http://localhost:3000/d/quarry-overview/quarry)
- Jaeger: [http://localhost:16686](http://localhost:16686)
- Prometheus: [http://localhost:9091](http://localhost:9091)
- API metrics: [http://localhost:8080/metrics](http://localhost:8080/metrics)

In Jaeger, select the `quarry-api` service and search for the `job.id` tag with the submitted job ID. The trace connects HTTP submission, PostgreSQL persistence, dispatcher claim, worker execution, and completion.

Remove the stack and its demonstration database volume:

```powershell
docker compose down --volumes --remove-orphans
```

## Prove worker-crash recovery

Run the automated user-facing recovery demonstration after the default Compose stack is down:

```powershell
pwsh ./scripts/dev.ps1 compose-recovery
```

The command starts the full Compose stack with short recovery timings. It validates the target worker's Compose labels, submits a long-running job, force-kills that worker, starts a replacement, and verifies the public API result. Attempt 1 must be `abandoned` with `lease_expired`; attempt 2 must succeed under a different worker ID.

The command prints the job ID, the Jaeger trace ID, and the Grafana and Jaeger URLs. It leaves the stack running so you can inspect both UIs. Remove it when finished:

```powershell
pwsh ./scripts/dev.ps1 compose-recovery-down
```

## Run Quarry in kind

Create a local cluster, build and load the four Quarry images, apply PostgreSQL and migrations in order, deploy the applications, and prove one job through the public API:

```powershell
pwsh ./scripts/dev.ps1 kind-up
kubectl --context kind-quarry-demo --namespace quarry get pods
```

The expected application shape is one API pod, two dispatcher pods, and three worker pods. The local cluster uses one PostgreSQL pod and is not highly available or production-ready.

Remove the persistent demonstration cluster:

```powershell
pwsh ./scripts/dev.ps1 kind-down
```

Run the isolated worker-scaling demonstration:

```powershell
pwsh ./scripts/dev.ps1 kind-scaling-test
```

The command measures Workload B at 1, 4, and 8 Ready workers with fixed load settings. It labels the output non-publishable, restores the default worker deployment, and deletes the temporary cluster.

## Verify benchmark evidence

Regenerate every committed run and campaign summary from the preserved raw samples:

```powershell
pwsh ./scripts/dev.ps1 benchmark-verify
```

The [benchmark report](docs/benchmarks.md) explains the 27-run campaign, its limits, and the separate command for a new full campaign. The kind scaling output is deployment evidence and does not change the benchmark report.

## Validate the repository

Check every local documentation link:

```powershell
pwsh ./scripts/dev.ps1 docs-test
```

Run the complete local suite:

```powershell
pwsh ./scripts/dev.ps1 check
```

`check` validates Go code, generated code, PostgreSQL behavior, images, Compose, Kustomize, kind scaling, observability, failure recovery, shutdown semantics, and race-sensitive code. Its isolated integration tests remove their Compose resources and owned kind clusters. The ordinary development database volume is retained, so do not run the suite while you need its services unchanged.

GitHub Actions runs the shorter pull-request checks for Go, the Linux race detector, images, Compose configuration, and Kustomize rendering. Complete kind, failure, and benchmark runs remain explicit local checks.
