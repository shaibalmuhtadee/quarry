param(
    [ValidateSet(
        "check", "test", "tools",
        "db-config", "db-up", "db-ready", "db-down",
        "migrate-up", "migrate-down", "migrate-status", "migration-test",
        "generate", "generate-check", "format-check", "vet", "build",
        "api-connect", "smoke-test"
    )]
    [string]$Command = "check"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Find-GoExecutable {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($null -ne $goCommand) {
        return $goCommand.Source
    }

    if ($IsWindows) {
        $installedGo = Join-Path $env:ProgramFiles "Go\bin\go.exe"
        if (Test-Path -LiteralPath $installedGo) {
            return $installedGo
        }
    }

    throw "Go is not available. Install the version declared in go.mod and add it to PATH."
}

function Invoke-Go {
    param(
        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    & $script:GoExecutable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "go $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
}

function Find-GoFmtExecutable {
    $executableName = if ($IsWindows) { "gofmt.exe" } else { "gofmt" }
    $goFmtExecutable = Join-Path (Split-Path -Parent $script:GoExecutable) $executableName
    if (Test-Path -LiteralPath $goFmtExecutable) {
        return $goFmtExecutable
    }

    throw "gofmt is not available beside the Go executable. Reinstall the Go toolchain."
}

function Find-DockerExecutable {
    $dockerCommand = Get-Command docker -ErrorAction SilentlyContinue
    if ($null -ne $dockerCommand) {
        return $dockerCommand.Source
    }

    throw "Docker is not available. Install Docker Desktop and add the Docker CLI to PATH."
}

function Invoke-Docker {
    param(
        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    if ($null -eq $script:DockerExecutable) {
        $script:DockerExecutable = Find-DockerExecutable
    }

    & $script:DockerExecutable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
}

function Test-Tools {
    Invoke-Go -Arguments @("version")
    Invoke-Go -Arguments @("tool", "goose", "-version")
    Invoke-Go -Arguments @("tool", "sqlc", "version")
}

function Test-GoPackages {
    $packages = @(& $script:GoExecutable list -e "./...")
    if ($LASTEXITCODE -ne 0) {
        throw "go list -e ./... failed with exit code $LASTEXITCODE."
    }

    if ($packages.Count -eq 0) {
        Write-Host "No Go packages exist yet; skipping package tests."
        return
    }

    Invoke-Go -Arguments @("test", "-count=1", "./...")
}

function Test-GoFormatting {
    $goFiles = @(
        Get-ChildItem -LiteralPath $repositoryRoot -Recurse -Filter "*.go" -File |
            ForEach-Object { $_.FullName }
    )
    if ($goFiles.Count -eq 0) {
        return
    }

    $unformattedFiles = @(& $script:GoFmtExecutable -l @goFiles)
    if ($LASTEXITCODE -ne 0) {
        throw "gofmt failed with exit code $LASTEXITCODE."
    }
    if ($unformattedFiles.Count -gt 0) {
        throw "gofmt found unformatted files:`n$($unformattedFiles -join "`n")"
    }
}

function Test-GoVet {
    Invoke-Go -Arguments @("vet", "./...")
}

function Test-GoBuild {
    Invoke-Go -Arguments @("build", "./...")
}

function Get-PostgresConnectionString {
    $port = if ([string]::IsNullOrWhiteSpace($env:QUARRY_POSTGRES_PORT)) {
        "5432"
    }
    else {
        $env:QUARRY_POSTGRES_PORT
    }

    return "postgres://quarry:quarry@localhost:$port/quarry?sslmode=disable"
}

function Invoke-Goose {
    param(
        [Parameter(Mandatory)]
        [string]$MigrationCommand
    )

    $migrationDirectory = Join-Path $repositoryRoot "internal/store/postgres/migrations"
    Invoke-Go -Arguments @(
        "tool", "goose", "-dir", $migrationDirectory,
        "postgres", (Get-PostgresConnectionString), $MigrationCommand
    )
}

function Invoke-Sqlc {
    param(
        [Parameter(Mandatory)]
        [string]$SqlcCommand
    )

    Invoke-Go -Arguments @("tool", "sqlc", $SqlcCommand)
}

function Test-ApiConnection {
    $hadDatabaseURL = Test-Path Env:QUARRY_DATABASE_URL
    $previousDatabaseURL = if ($hadDatabaseURL) {
        $env:QUARRY_DATABASE_URL
    }
    else {
        $null
    }

    try {
        $env:QUARRY_DATABASE_URL = Get-PostgresConnectionString
        Invoke-Go -Arguments @("run", "./cmd/api")
    }
    finally {
        if ($hadDatabaseURL) {
            $env:QUARRY_DATABASE_URL = $previousDatabaseURL
        }
        else {
            Remove-Item Env:QUARRY_DATABASE_URL -ErrorAction SilentlyContinue
        }
    }
}

function Test-ComposeSmoke {
    Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
    try {
        Invoke-Goose -MigrationCommand "up"
        Test-ApiConnection
    }
    finally {
        Invoke-Docker -Arguments @("compose", "down")
    }
}

$script:GoExecutable = Find-GoExecutable
$script:GoFmtExecutable = Find-GoFmtExecutable
$script:DockerExecutable = $null
$repositoryRoot = Split-Path -Parent $PSScriptRoot

Push-Location $repositoryRoot
try {
    switch ($Command) {
        "tools" {
            Test-Tools
        }
        "test" {
            Test-GoPackages
        }
        "check" {
            Invoke-Go -Arguments @("mod", "tidy", "-diff")
            Test-GoFormatting
            Test-Tools
            Invoke-Sqlc -SqlcCommand "diff"
            Test-GoVet
            Test-GoPackages
            Test-GoBuild
            Invoke-Docker -Arguments @("compose", "config", "--quiet")
            Test-ComposeSmoke
        }
        "db-config" {
            Invoke-Docker -Arguments @("compose", "config")
        }
        "db-up" {
            Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
        }
        "db-ready" {
            Invoke-Docker -Arguments @(
                "compose", "exec", "--no-TTY", "postgres",
                "pg_isready", "--username", "quarry", "--dbname", "quarry"
            )
        }
        "db-down" {
            Invoke-Docker -Arguments @("compose", "down")
        }
        "migrate-up" {
            Invoke-Goose -MigrationCommand "up"
        }
        "migrate-down" {
            Invoke-Goose -MigrationCommand "down"
        }
        "migrate-status" {
            Invoke-Goose -MigrationCommand "status"
        }
        "migration-test" {
            Invoke-Go -Arguments @("test", "-count=1", "./internal/store/postgres/migrations")
        }
        "generate" {
            Invoke-Sqlc -SqlcCommand "generate"
        }
        "generate-check" {
            Invoke-Sqlc -SqlcCommand "diff"
        }
        "format-check" {
            Test-GoFormatting
        }
        "vet" {
            Test-GoVet
        }
        "build" {
            Test-GoBuild
        }
        "api-connect" {
            Test-ApiConnection
        }
        "smoke-test" {
            Test-ComposeSmoke
        }
    }
}
finally {
    Pop-Location
}
