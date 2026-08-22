param(
    [ValidateSet(
        "check", "test", "tools",
        "db-config", "db-up", "db-ready", "db-down",
        "migrate-up", "migrate-down", "migrate-status", "migration-test",
        "generate", "generate-check", "format-check", "vet", "build",
        "smoke-test"
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

function Get-AvailableLoopbackPort {
    $listener = [System.Net.Sockets.TcpListener]::new(
        [System.Net.IPAddress]::Loopback,
        0
    )
    try {
        $listener.Start()
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally {
        $listener.Stop()
    }
}

function Start-ApiSmokeProcess {
    param(
        [Parameter(Mandatory)]
        [string]$ApiBinary,

        [Parameter(Mandatory)]
        [string]$HttpAddress
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $ApiBinary
    $startInfo.WorkingDirectory = $repositoryRoot
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.Environment["QUARRY_DATABASE_URL"] = Get-PostgresConnectionString
    $startInfo.Environment["QUARRY_HTTP_ADDR"] = $HttpAddress

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        $process.Dispose()
        throw "Failed to start the API smoke-test process."
    }

    return $process
}

function Wait-ApiReady {
    param(
        [Parameter(Mandatory)]
        [System.Diagnostics.Process]$Process,

        [Parameter(Mandatory)]
        [string]$BaseURL
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($Process.HasExited) {
            throw "API exited before becoming ready with code $($Process.ExitCode)."
        }

        try {
            $response = Invoke-RestMethod -Method Get -Uri "$BaseURL/readyz" -TimeoutSec 2
            if ($response.status -eq "ready") {
                return
            }
        }
        catch {
        }

        Start-Sleep -Milliseconds 200
    }

    throw "API did not become ready within 30 seconds."
}

function Test-ApiRoundTrip {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL
    )

    $health = Invoke-RestMethod -Method Get -Uri "$BaseURL/healthz" -TimeoutSec 10
    if ($health.status -ne "ok") {
        throw "API health status was '$($health.status)', expected 'ok'."
    }

    $body = @{
        type = "smoke.test"
        payload = @{
            message = "hello"
        }
        max_attempts = 3
        timeout_ms = 30000
    } | ConvertTo-Json -Compress -Depth 4
    $submitted = Invoke-RestMethod `
        -Method Post `
        -Uri "$BaseURL/v1/jobs" `
        -ContentType "application/json" `
        -Body $body `
        -TimeoutSec 10
    if ([string]::IsNullOrWhiteSpace($submitted.id) -or $submitted.status -ne "queued") {
        throw "API submission did not return a queued job with an ID."
    }

    $retrieved = Invoke-RestMethod `
        -Method Get `
        -Uri "$BaseURL/v1/jobs/$($submitted.id)" `
        -TimeoutSec 10
    if ($retrieved.id -ne $submitted.id) {
        throw "Retrieved job ID '$($retrieved.id)' did not match '$($submitted.id)'."
    }
    if ($retrieved.type -ne "smoke.test" -or $retrieved.status -ne "queued") {
        throw "Retrieved job did not retain its type and queued status."
    }
    if ($retrieved.attempt_count -ne 0 -or $retrieved.max_attempts -ne 3) {
        throw "Retrieved job did not retain its attempt values."
    }
    if ($retrieved.timeout_ms -ne 30000) {
        throw "Retrieved job timeout was '$($retrieved.timeout_ms)', expected 30000."
    }
    if ($retrieved.PSObject.Properties.Name -contains "payload") {
        throw "Retrieved job exposed its stored payload."
    }
}

function Stop-ApiSmokeProcess {
    param(
        [Parameter(Mandatory)]
        [System.Diagnostics.Process]$Process
    )

    try {
        if ($Process.HasExited) {
            return
        }

        if (-not $IsWindows) {
            & kill -TERM $Process.Id
            if ($LASTEXITCODE -eq 0 -and $Process.WaitForExit(10000)) {
                return
            }
        }

        $Process.Kill($true)
        if (-not $Process.WaitForExit(10000)) {
            throw "API smoke-test process did not exit."
        }
    }
    finally {
        $Process.Dispose()
    }
}

function Remove-ApiSmokeBinary {
    param(
        [Parameter(Mandatory)]
        [string]$ApiBinary
    )

    if (-not (Test-Path -LiteralPath $ApiBinary)) {
        return
    }

    $resolvedBinary = [System.IO.Path]::GetFullPath($ApiBinary)
    $resolvedTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if (-not $resolvedBinary.StartsWith($resolvedTemp, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove API smoke-test binary outside the temporary directory: $resolvedBinary"
    }

    Remove-Item -LiteralPath $resolvedBinary -Force
}

function Test-ComposeSmoke {
    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $apiBinary = Join-Path `
        ([System.IO.Path]::GetTempPath()) `
        "quarry-api-smoke-$([Guid]::NewGuid().ToString('N'))$binaryExtension"
    $apiProcess = $null

    try {
        Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
        Invoke-Goose -MigrationCommand "up"
        Invoke-Go -Arguments @("build", "-o", $apiBinary, "./cmd/api")

        $port = Get-AvailableLoopbackPort
        $httpAddress = "127.0.0.1:$port"
        $baseURL = "http://$httpAddress"
        $apiProcess = Start-ApiSmokeProcess -ApiBinary $apiBinary -HttpAddress $httpAddress
        Wait-ApiReady -Process $apiProcess -BaseURL $baseURL
        Test-ApiRoundTrip -BaseURL $baseURL
    }
    finally {
        try {
            if ($null -ne $apiProcess) {
                Stop-ApiSmokeProcess -Process $apiProcess
            }
        }
        finally {
            try {
                Remove-ApiSmokeBinary -ApiBinary $apiBinary
            }
            finally {
                Invoke-Docker -Arguments @("compose", "down")
            }
        }
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
        "smoke-test" {
            Test-ComposeSmoke
        }
    }
}
finally {
    Pop-Location
}
