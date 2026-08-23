# Quarry

Quarry is a distributed job execution system written in Go. The current implementation provides a durable HTTP control plane backed by PostgreSQL. It accepts jobs and retrieves their queued state.

The dispatcher and workers are not implemented yet. Submitted jobs remain queued and are not executed.

## Requirements

Install these tools before you run the repository:

- Go 1.27.0
- PowerShell 7
- Docker Desktop with Linux containers

You do not need a host PostgreSQL installation. Go runs the pinned Goose and sqlc versions from `go.mod`.

## Run the API locally

Start PostgreSQL, apply the migrations, and run the API from the repository root:

```powershell
pwsh ./scripts/dev.ps1 db-up
pwsh ./scripts/dev.ps1 migrate-up
go run ./cmd/api
```

The API listens on `:8080` by default. In another terminal, submit and retrieve a job:

```powershell
$body = @{
    type = "example"
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
Invoke-RestMethod -Uri "http://localhost:8080/v1/jobs/$($job.id)"
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

The API accepts these environment variables:

- `QUARRY_DATABASE_URL`: PostgreSQL connection string. The default connects to the development database on port 5432.
- `QUARRY_HTTP_ADDR`: HTTP listen address. The default is `:8080`.

The development script also accepts `QUARRY_POSTGRES_PORT` to change the host port used by Docker Compose and migration commands. Set `QUARRY_DATABASE_URL` to the matching port when you run the API.

## Validate the repository

Run the complete local validation:

```powershell
pwsh ./scripts/dev.ps1 check
```

This checks formatting, dependencies, pinned tools, generated sqlc code, static analysis, tests, builds, and the Compose configuration. It also runs an HTTP smoke test against the Compose PostgreSQL service.

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

Quarry does not yet dispatch or execute jobs. It also does not yet provide retries, leases, cancellation, idempotent submission, metrics, or tracing. These capabilities belong to later milestones.
