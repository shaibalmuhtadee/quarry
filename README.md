# Quarry

Quarry is a distributed job execution system written in Go. Milestone 0 provides the repository, PostgreSQL schema, migrations, generated database code, and validation needed for later job execution work.

The current `cmd/api` command checks its PostgreSQL connection and exits. It does not start an HTTP server yet.

## Requirements

Install these tools before you run the repository:

- Go 1.27.0
- PowerShell 7
- Docker Desktop with Linux containers

You do not need a host PostgreSQL installation. Go runs the pinned Goose and sqlc versions from `go.mod`.

## Validate a fresh clone

From the repository root, run:

```powershell
pwsh ./scripts/dev.ps1 check
```

The command checks formatting, dependencies, pinned tools, generated sqlc code, static analysis, tests, builds, and the Compose configuration. It also starts PostgreSQL, applies migrations, runs the `cmd/api` connection check, and stops PostgreSQL without deleting the development volume.

A successful connection check prints:

```text
PostgreSQL connection successful
```

## Run the PostgreSQL connection check

Start PostgreSQL, apply migrations, run the check, and stop PostgreSQL:

```powershell
pwsh ./scripts/dev.ps1 db-up
pwsh ./scripts/dev.ps1 migrate-up
pwsh ./scripts/dev.ps1 api-connect
pwsh ./scripts/dev.ps1 db-down
```

Set `QUARRY_POSTGRES_PORT` before these commands if port 5432 is unavailable. `api-connect` uses the same port value.

## Update generated database code

After you change a migration or a query, regenerate the sqlc output:

```powershell
pwsh ./scripts/dev.ps1 generate
pwsh ./scripts/dev.ps1 generate-check
```

`generate-check` fails when the files in `internal/store/postgres/generated` differ from fresh sqlc output.
