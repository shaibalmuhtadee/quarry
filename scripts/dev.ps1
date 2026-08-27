param(
    [ValidateSet(
        "check", "test", "tools",
        "db-config", "db-up", "db-ready", "db-down",
        "migrate-up", "migrate-down", "migrate-status", "migration-test", "restart-test",
        "generate", "generate-check", "format-check", "vet", "build",
        "smoke-test", "distributed-test", "recovery-test", "semantics-test",
        "observability-config-test", "observability-test", "observability-up", "observability-down"
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
    Invoke-Go -Arguments @("tool", "buf", "--version")
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

function Invoke-Buf {
    param(
        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    Invoke-Go -Arguments (@("tool", "buf") + $Arguments)
}

function Test-BufGeneratedCode {
    Invoke-Buf -Arguments @("lint")
    Invoke-Buf -Arguments @("format", "--diff", "--exit-code")

    $temporaryRoot = Join-Path `
        ([System.IO.Path]::GetTempPath()) `
        "quarry-buf-check-$([Guid]::NewGuid().ToString('N'))"
    $committedOutput = Join-Path $repositoryRoot "internal/rpc/generated"

    try {
        New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
        Invoke-Buf -Arguments @("generate", "--output", $temporaryRoot)

        $generatedOutput = Join-Path $temporaryRoot "internal/rpc/generated"
        if (-not (Test-Path -LiteralPath $committedOutput)) {
            throw "Committed Protocol Buffer output does not exist. Run 'pwsh ./scripts/dev.ps1 generate'."
        }
        if (-not (Test-Path -LiteralPath $generatedOutput)) {
            throw "Buf did not create the expected generated output at $generatedOutput."
        }

        & git diff --no-index --exit-code --no-ext-diff -- $committedOutput $generatedOutput
        if ($LASTEXITCODE -ne 0) {
            throw "Generated Protocol Buffer code is stale. Run 'pwsh ./scripts/dev.ps1 generate'."
        }
    }
    finally {
        if (Test-Path -LiteralPath $temporaryRoot) {
            $resolvedTemporaryRoot = [System.IO.Path]::GetFullPath($temporaryRoot)
            $resolvedSystemTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
            if (-not $resolvedTemporaryRoot.StartsWith(
                $resolvedSystemTemp,
                [System.StringComparison]::OrdinalIgnoreCase
            )) {
                throw "Refusing to remove Protocol Buffer check output outside the temporary directory: $resolvedTemporaryRoot"
            }

            Remove-Item -LiteralPath $resolvedTemporaryRoot -Recurse -Force
        }
    }
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

function Start-DistributedProcess {
    param(
        [Parameter(Mandatory)]
        [string]$Binary,

        [Parameter(Mandatory)]
        [hashtable]$Environment
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $Binary
    $startInfo.WorkingDirectory = $repositoryRoot
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    foreach ($entry in $Environment.GetEnumerator()) {
        $startInfo.Environment[$entry.Key] = $entry.Value
    }

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        $process.Dispose()
        throw "Failed to start distributed-test process '$Binary'."
    }

    return $process
}

function Stop-DistributedProcess {
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
            throw "Distributed-test process $($Process.Id) did not exit."
        }
    }
    finally {
        $Process.Dispose()
    }
}

function Stop-CrashedWorkerProcess {
    param(
        [Parameter(Mandatory)]
        [System.Diagnostics.Process]$Process
    )

    try {
        if ($Process.HasExited) {
            throw "Recovery-test worker exited before crash injection with code $($Process.ExitCode)."
        }
        $Process.Kill($true)
        if (-not $Process.WaitForExit(10000)) {
            throw "Recovery-test worker did not exit after crash injection."
        }
    }
    finally {
        $Process.Dispose()
    }
}

function Wait-TcpReady {
    param(
        [Parameter(Mandatory)]
        [System.Diagnostics.Process]$Process,

        [Parameter(Mandatory)]
        [string]$HostName,

        [Parameter(Mandatory)]
        [int]$Port,

        [Parameter(Mandatory)]
        [string]$ProcessName
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($Process.HasExited) {
            throw "$ProcessName exited before becoming ready with code $($Process.ExitCode)."
        }

        $client = [System.Net.Sockets.TcpClient]::new()
        try {
            $connection = $client.ConnectAsync($HostName, $Port)
            if ($connection.Wait(500) -and $client.Connected) {
                return
            }
        }
        catch {
        }
        finally {
            $client.Dispose()
        }

        Start-Sleep -Milliseconds 100
    }

    throw "$ProcessName did not become ready within 30 seconds."
}

function Invoke-PostgresRows {
    param(
        [Parameter(Mandatory)]
        [string]$Query
    )

    return @(
        Invoke-Docker -Arguments @(
            "compose", "exec", "--no-TTY", "postgres",
            "psql", "--username", "quarry", "--dbname", "quarry",
            "--tuples-only", "--no-align", "--command", $Query
        )
    )
}

function Wait-DistributedWorkers {
    param(
        [Parameter(Mandatory)]
        [string[]]$HostNames,

        [Parameter(Mandatory)]
        [System.Diagnostics.Process[]]$Processes
    )

    $quotedHostNames = $HostNames | ForEach-Object { "'$_'" }
    $query = "SELECT id::text FROM workers WHERE hostname IN ($($quotedHostNames -join ',')) ORDER BY hostname;"
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        foreach ($process in $Processes) {
            if ($process.HasExited) {
                throw "Worker exited before registering with code $($process.ExitCode)."
            }
        }

        $workerIDs = @(
            Invoke-PostgresRows -Query $query |
                ForEach-Object { $_.Trim() } |
                Where-Object { $_ -match '^[0-9a-f-]{36}$' }
        )
        if ($workerIDs.Count -eq $HostNames.Count) {
            return $workerIDs
        }

        Start-Sleep -Milliseconds 200
    }

    throw "Workers did not register within 30 seconds."
}

function Submit-DistributedJobs {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [int]$Count = 40
    )

    $jobs = @()
    for ($index = 1; $index -le $Count; $index++) {
        $jobType = if ($index % 2 -eq 0) { "demo.echo" } else { "demo.payload_size" }
        $payload = if ($jobType -eq "demo.echo") { "echo-$index" } else { "size-$index" }
        $body = @{
            type = $jobType
            payload = $payload
            max_attempts = 3
            timeout_ms = 30000
        } | ConvertTo-Json -Compress
        $submitted = Invoke-RestMethod `
            -Method Post `
            -Uri "$BaseURL/v1/jobs" `
            -ContentType "application/json" `
            -Body $body `
            -TimeoutSec 10
        if ([string]::IsNullOrWhiteSpace($submitted.id)) {
            throw "Distributed job $index did not return an ID."
        }

        $expected = if ($jobType -eq "demo.echo") { $payload } else { $payload.Length + 2 }
        $jobs += @{
            id = $submitted.id
            type = $jobType
            expected = $expected
        }
    }

    return $jobs
}

function Wait-DistributedJobs {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [object[]]$Jobs,

        [Parameter(Mandatory)]
        [string[]]$WorkerIDs,

        [Parameter(Mandatory)]
        [System.Diagnostics.Process[]]$Processes
    )

    $pending = @{}
    foreach ($job in $Jobs) {
        $pending[$job.id] = $job
    }
    $expectedWorkerIDs = @{}
    foreach ($workerID in $WorkerIDs) {
        $expectedWorkerIDs[$workerID] = $true
    }
    $usedWorkerIDs = @{}

    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    while ($pending.Count -gt 0 -and [DateTime]::UtcNow -lt $deadline) {
        foreach ($process in $Processes) {
            if ($process.HasExited) {
                throw "Distributed-test process exited early with code $($process.ExitCode)."
            }
        }

        foreach ($jobID in @($pending.Keys)) {
            $job = $pending[$jobID]
            $state = Invoke-RestMethod -Method Get -Uri "$BaseURL/v1/jobs/$jobID" -TimeoutSec 10
            if ($state.status -eq "queued" -or $state.status -eq "running") {
                continue
            }
            if ($state.status -ne "succeeded") {
                throw "Job $jobID reached unexpected status '$($state.status)'."
            }
            if ($job.type -eq "demo.echo" -and $state.result -ne $job.expected) {
                throw "Echo job $jobID returned '$($state.result)', expected '$($job.expected)'."
            }
            if ($job.type -eq "demo.payload_size" -and $state.result.bytes -ne $job.expected) {
                throw "Payload-size job $jobID returned '$($state.result.bytes)', expected '$($job.expected)'."
            }

            $attemptResponse = Invoke-RestMethod `
                -Method Get `
                -Uri "$BaseURL/v1/jobs/$jobID/attempts" `
                -TimeoutSec 10
            $attempts = @($attemptResponse.attempts)
            if ($attempts.Count -ne 1) {
                throw "Job $jobID has $($attempts.Count) attempts, expected one."
            }
            $attempt = $attempts[0]
            if ($attempt.attempt_no -ne 1 -or $attempt.status -ne "succeeded" -or
                [string]::IsNullOrWhiteSpace($attempt.finished_at)) {
                throw "Job $jobID did not record one finished successful attempt."
            }
            if (-not $expectedWorkerIDs.ContainsKey($attempt.worker_id)) {
                throw "Job $jobID was completed by unexpected worker '$($attempt.worker_id)'."
            }
            $usedWorkerIDs[$attempt.worker_id] = $true
            $pending.Remove($jobID)
        }

        if ($pending.Count -gt 0) {
            Start-Sleep -Milliseconds 100
        }
    }

    if ($pending.Count -gt 0) {
        throw "$($pending.Count) distributed jobs did not succeed within 60 seconds."
    }
    if ($usedWorkerIDs.Count -ne $WorkerIDs.Count) {
        throw "The batch used $($usedWorkerIDs.Count) worker processes, expected $($WorkerIDs.Count)."
    }
}

function Assert-DistributedPostgresState {
    param(
        [Parameter(Mandatory)]
        [object[]]$Jobs,

        [Parameter(Mandatory)]
        [string[]]$WorkerIDs
    )

    $expectedJobs = @{}
    $quotedJobIDs = foreach ($job in $Jobs) {
        if ($job.id -notmatch '^[0-9a-f-]{36}$') {
            throw "Distributed job has invalid ID '$($job.id)'."
        }
        $expectedJobs[$job.id] = $true
        "'$($job.id)'::uuid"
    }
    $expectedWorkers = @{}
    foreach ($workerID in $WorkerIDs) {
        if ($workerID -notmatch '^[0-9a-f-]{36}$') {
            throw "Distributed worker has invalid ID '$workerID'."
        }
        $expectedWorkers[$workerID] = $true
    }

    $query = @"
SELECT
    jobs.id::text,
    jobs.status,
    jobs.attempt_count,
    jobs.current_worker_id IS NULL,
    jobs.result IS NOT NULL,
    jobs.finished_at IS NOT NULL,
    job_attempts.attempt_no,
    job_attempts.worker_id::text,
    job_attempts.status,
    job_attempts.finished_at IS NOT NULL,
    jobs.finished_at = job_attempts.finished_at
FROM jobs
JOIN job_attempts ON job_attempts.job_id = jobs.id
WHERE jobs.id IN ($($quotedJobIDs -join ','))
ORDER BY jobs.id, job_attempts.attempt_no;
"@
    $rows = @(
        Invoke-PostgresRows -Query $query |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ -match '\|' }
    )
    if ($rows.Count -ne $Jobs.Count) {
        throw "PostgreSQL returned $($rows.Count) job-attempt rows, expected $($Jobs.Count)."
    }

    $inspectedJobs = @{}
    foreach ($row in $rows) {
        $columns = $row.Split('|')
        if ($columns.Count -ne 11) {
            throw "PostgreSQL returned an unexpected distributed job-attempt row: '$row'."
        }

        $jobID = $columns[0]
        $workerID = $columns[7]
        if (-not $expectedJobs.ContainsKey($jobID) -or $inspectedJobs.ContainsKey($jobID)) {
            throw "PostgreSQL returned a missing, duplicate, or unexpected job-attempt row for '$jobID'."
        }
        if (-not $expectedWorkers.ContainsKey($workerID)) {
            throw "PostgreSQL assigned job '$jobID' to unexpected worker '$workerID'."
        }
        if ($columns[1] -ne 'succeeded' -or $columns[2] -ne '1' -or
            $columns[3] -ne 't' -or $columns[4] -ne 't' -or $columns[5] -ne 't' -or
            $columns[6] -ne '1' -or $columns[8] -ne 'succeeded' -or
            $columns[9] -ne 't' -or $columns[10] -ne 't') {
            throw "PostgreSQL stored invalid completed state for distributed job '$jobID': '$row'."
        }
        $inspectedJobs[$jobID] = $true
    }

    foreach ($jobID in $expectedJobs.Keys) {
        if (-not $inspectedJobs.ContainsKey($jobID)) {
            throw "PostgreSQL did not return distributed job '$jobID' with one completed attempt."
        }
    }
}

function Get-RecoveryAttemptOneState {
    param(
        [Parameter(Mandatory)]
        [string]$JobID
    )

    if ($JobID -notmatch '^[0-9a-f-]{36}$') {
        throw "Recovery job has invalid ID '$JobID'."
    }
    $query = @"
SELECT
    jobs.status,
    jobs.attempt_count,
    jobs.current_worker_id::text,
    jobs.lease_expires_at::text,
    workers.last_seen_at::text
FROM jobs
JOIN workers ON workers.id = jobs.current_worker_id
WHERE jobs.id = '$JobID'::uuid;
"@
    $rows = @(
        Invoke-PostgresRows -Query $query |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ -match '\|' }
    )
    if ($rows.Count -eq 0) {
        return $null
    }
    if ($rows.Count -ne 1) {
        throw "PostgreSQL returned $($rows.Count) active recovery rows, expected one."
    }
    $columns = $rows[0].Split('|')
    if ($columns.Count -ne 5) {
        throw "PostgreSQL returned an unexpected active recovery row: '$($rows[0])'."
    }
    return [PSCustomObject]@{
        Status = $columns[0]
        AttemptCount = $columns[1]
        WorkerID = $columns[2]
        LeaseExpiresAt = $columns[3]
        LastSeenAt = $columns[4]
    }
}

function Wait-RecoveryAttemptOneRenewal {
    param(
        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [System.Diagnostics.Process]$WorkerProcess
    )

    $initial = $null
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($WorkerProcess.HasExited) {
            throw "Worker 1 exited before renewing attempt 1 with code $($WorkerProcess.ExitCode)."
        }
        $state = Get-RecoveryAttemptOneState -JobID $JobID
        if ($null -ne $state -and $state.Status -eq 'running' -and $state.AttemptCount -eq '1') {
            if ($null -eq $initial) {
                $initial = $state
            }
            elseif ($state.WorkerID -eq $initial.WorkerID -and
                $state.LeaseExpiresAt -ne $initial.LeaseExpiresAt -and
                $state.LastSeenAt -ne $initial.LastSeenAt) {
                return $state
            }
        }
        Start-Sleep -Milliseconds 100
    }

    throw "Attempt 1 did not start and renew its lease within 30 seconds."
}

function Wait-RecoveryJobSucceeded {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [System.Diagnostics.Process[]]$Processes
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    while ([DateTime]::UtcNow -lt $deadline) {
        foreach ($process in $Processes) {
            if ($process.HasExited) {
                throw "Recovery-test process exited early with code $($process.ExitCode)."
            }
        }
        $state = Invoke-RestMethod -Method Get -Uri "$BaseURL/v1/jobs/$JobID" -TimeoutSec 10
        if ($state.status -eq 'succeeded') {
            return $state
        }
        if ($state.status -notin @('running', 'retry_wait')) {
            throw "Recovery job $JobID reached unexpected status '$($state.status)'."
        }
        Start-Sleep -Milliseconds 100
    }

    throw "Recovery job $JobID did not succeed within 45 seconds."
}

function Assert-RecoveryState {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$FirstWorkerID,

        [Parameter(Mandatory)]
        [string]$SecondWorkerID,

        [Parameter(Mandatory)]
        [object]$JobState
    )

    if ($JobState.attempt_count -ne 2 -or $JobState.result.slept_ms -ne 6000) {
        throw "Recovery job did not return the expected attempt count and sleep result."
    }
    $attemptResponse = Invoke-RestMethod `
        -Method Get `
        -Uri "$BaseURL/v1/jobs/$JobID/attempts" `
        -TimeoutSec 10
    $attempts = @($attemptResponse.attempts)
    if ($attempts.Count -ne 2) {
        throw "Recovery job has $($attempts.Count) HTTP attempts, expected two."
    }
    if ($attempts[0].attempt_no -ne 1 -or $attempts[0].worker_id -ne $FirstWorkerID -or
        $attempts[0].status -ne 'abandoned' -or [string]::IsNullOrWhiteSpace($attempts[0].finished_at)) {
        throw "HTTP did not return attempt 1 as finished and abandoned by worker 1."
    }
    if ($attempts[1].attempt_no -ne 2 -or $attempts[1].worker_id -ne $SecondWorkerID -or
        $attempts[1].status -ne 'succeeded' -or [string]::IsNullOrWhiteSpace($attempts[1].finished_at)) {
        throw "HTTP did not return attempt 2 as finished and succeeded by worker 2."
    }

    $query = @"
SELECT
    jobs.status,
    jobs.attempt_count,
    jobs.current_worker_id IS NULL,
    jobs.lease_expires_at IS NULL,
    jobs.result = '{"slept_ms":6000}'::jsonb,
    jobs.finished_at IS NOT NULL,
    job_attempts.attempt_no,
    job_attempts.worker_id::text,
    job_attempts.status,
    job_attempts.finished_at IS NOT NULL,
    CASE WHEN job_attempts.attempt_no = 2 THEN jobs.finished_at = job_attempts.finished_at ELSE true END,
    (SELECT state FROM workers WHERE id = '$FirstWorkerID'::uuid),
    (SELECT state FROM workers WHERE id = '$SecondWorkerID'::uuid)
FROM jobs
JOIN job_attempts ON job_attempts.job_id = jobs.id
WHERE jobs.id = '$JobID'::uuid
ORDER BY job_attempts.attempt_no;
"@
    $rows = @(
        Invoke-PostgresRows -Query $query |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ -match '\|' }
    )
    if ($rows.Count -ne 2) {
        throw "PostgreSQL returned $($rows.Count) recovery attempt rows, expected two."
    }
    for ($index = 0; $index -lt 2; $index++) {
        $columns = $rows[$index].Split('|')
        if ($columns.Count -ne 13) {
            throw "PostgreSQL returned an unexpected recovery row: '$($rows[$index])'."
        }
        $attemptNumber = [string]($index + 1)
        $expectedWorkerID = if ($index -eq 0) { $FirstWorkerID } else { $SecondWorkerID }
        $expectedAttemptStatus = if ($index -eq 0) { 'abandoned' } else { 'succeeded' }
        if ($columns[0] -ne 'succeeded' -or $columns[1] -ne '2' -or
            $columns[2] -ne 't' -or $columns[3] -ne 't' -or $columns[4] -ne 't' -or
            $columns[5] -ne 't' -or $columns[6] -ne $attemptNumber -or
            $columns[7] -ne $expectedWorkerID -or $columns[8] -ne $expectedAttemptStatus -or
            $columns[9] -ne 't' -or $columns[10] -ne 't' -or
            $columns[11] -ne 'lost' -or $columns[12] -ne 'active') {
            throw "PostgreSQL stored invalid recovery state: '$($rows[$index])'."
        }
    }
}

function Assert-RecoveryCleanup {
    param(
        [Parameter(Mandatory)]
        [string]$ComposeProject,

        [Parameter(Mandatory)]
        [string]$TemporaryDirectory,

        [Parameter(Mandatory)]
        [int[]]$ProcessIDs
    )

    if (Test-Path -LiteralPath $TemporaryDirectory) {
        throw "Recovery-test temporary directory still exists: $TemporaryDirectory"
    }
    foreach ($processID in $ProcessIDs) {
        if ($null -ne (Get-Process -Id $processID -ErrorAction SilentlyContinue)) {
            throw "Recovery-test process $processID is still running."
        }
    }
    $containers = @(Invoke-Docker -Arguments @(
        "ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project=$ComposeProject"
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    $networks = @(Invoke-Docker -Arguments @(
        "network", "ls", "--quiet", "--filter", "label=com.docker.compose.project=$ComposeProject"
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    $volumes = @(Invoke-Docker -Arguments @(
        "volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project=$ComposeProject"
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($containers.Count -ne 0 -or $networks.Count -ne 0 -or $volumes.Count -ne 0) {
        throw "Recovery-test Compose resources remain after cleanup."
    }
}

function Remove-DistributedTestDirectory {
    param(
        [Parameter(Mandatory)]
        [string]$Directory
    )

    if (-not (Test-Path -LiteralPath $Directory)) {
        return
    }

    $resolvedDirectory = [System.IO.Path]::GetFullPath($Directory)
    $resolvedTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if (-not $resolvedDirectory.StartsWith($resolvedTemp, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove distributed-test output outside the temporary directory: $resolvedDirectory"
    }

    Remove-Item -LiteralPath $resolvedDirectory -Recurse -Force
}

function Test-DistributedProcesses {
    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-distributed-$testID"
    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $apiBinary = Join-Path $temporaryDirectory "quarry-api$binaryExtension"
    $dispatcherBinary = Join-Path $temporaryDirectory "quarry-dispatcher$binaryExtension"
    $workerBinary = Join-Path $temporaryDirectory "quarry-worker$binaryExtension"
    $previousComposeProject = $env:COMPOSE_PROJECT_NAME
    $previousPostgresPort = $env:QUARRY_POSTGRES_PORT
    $apiProcess = $null
    $dispatcherProcess = $null
    $workerProcesses = @()

    $env:COMPOSE_PROJECT_NAME = "quarry-m2-$testID"
    $env:QUARRY_POSTGRES_PORT = [string](Get-AvailableLoopbackPort)
    $databaseURL = Get-PostgresConnectionString

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        Invoke-Go -Arguments @("build", "-o", $apiBinary, "./cmd/api")
        Invoke-Go -Arguments @("build", "-o", $dispatcherBinary, "./cmd/dispatcher")
        Invoke-Go -Arguments @("build", "-o", $workerBinary, "./cmd/worker")
        Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
        Invoke-Goose -MigrationCommand "up"

        $httpPort = Get-AvailableLoopbackPort
        $dispatcherPort = Get-AvailableLoopbackPort
        $httpAddress = "127.0.0.1:$httpPort"
        $dispatcherAddress = "127.0.0.1:$dispatcherPort"
        $baseURL = "http://$httpAddress"
        $apiProcess = Start-DistributedProcess -Binary $apiBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_HTTP_ADDR = $httpAddress
        }
        Wait-ApiReady -Process $apiProcess -BaseURL $baseURL

        $dispatcherProcess = Start-DistributedProcess -Binary $dispatcherBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_DISPATCHER_ADDR = $dispatcherAddress
        }
        Wait-TcpReady `
            -Process $dispatcherProcess `
            -HostName "127.0.0.1" `
            -Port $dispatcherPort `
            -ProcessName "Dispatcher"

        $jobs = Submit-DistributedJobs -BaseURL $baseURL
        $workerHostNames = @("distributed-worker-1-$testID", "distributed-worker-2-$testID")
        foreach ($workerHostName in $workerHostNames) {
            $workerProcesses += Start-DistributedProcess -Binary $workerBinary -Environment @{
                QUARRY_DISPATCHER_ADDR = $dispatcherAddress
                QUARRY_WORKER_CONCURRENCY = "2"
                QUARRY_WORKER_HOSTNAME = $workerHostName
                QUARRY_WORKER_VERSION = "distributed-test"
            }
        }
        $workerIDs = Wait-DistributedWorkers -HostNames $workerHostNames -Processes $workerProcesses

        $allProcesses = @($apiProcess, $dispatcherProcess) + $workerProcesses
        Wait-DistributedJobs `
            -BaseURL $baseURL `
            -Jobs $jobs `
            -WorkerIDs $workerIDs `
            -Processes $allProcesses
        Assert-DistributedPostgresState -Jobs $jobs -WorkerIDs $workerIDs
        Write-Host "Distributed test passed: $($jobs.Count) jobs, $($workerIDs.Count) workers, concurrency 2, PostgreSQL state verified."
    }
    finally {
        try {
            $stopErrors = @()
            foreach ($workerProcess in $workerProcesses) {
                try {
                    Stop-DistributedProcess -Process $workerProcess
                }
                catch {
                    $stopErrors += $_
                }
            }
            foreach ($serviceProcess in @($dispatcherProcess, $apiProcess)) {
                if ($null -eq $serviceProcess) {
                    continue
                }
                try {
                    Stop-DistributedProcess -Process $serviceProcess
                }
                catch {
                    $stopErrors += $_
                }
            }
            if ($stopErrors.Count -gt 0) {
                throw "Failed to stop $($stopErrors.Count) distributed-test processes."
            }
        }
        finally {
            try {
                Invoke-Docker -Arguments @("compose", "down", "--volumes")
            }
            finally {
                try {
                    Remove-DistributedTestDirectory -Directory $temporaryDirectory
                }
                finally {
                    $env:COMPOSE_PROJECT_NAME = $previousComposeProject
                    $env:QUARRY_POSTGRES_PORT = $previousPostgresPort
                }
            }
        }
    }
}

function Build-LinuxWorkerBinary {
    param(
        [Parameter(Mandatory)]
        [string]$Output
    )

    $previousGOOS = $env:GOOS
    $previousGOARCH = $env:GOARCH
    $previousCGOEnabled = $env:CGO_ENABLED
    try {
        $env:GOOS = "linux"
        $env:GOARCH = "amd64"
        $env:CGO_ENABLED = "0"
        Invoke-Go -Arguments @("build", "-o", $Output, "./cmd/worker")
    }
    finally {
        $env:GOOS = $previousGOOS
        $env:GOARCH = $previousGOARCH
        $env:CGO_ENABLED = $previousCGOEnabled
    }
}

function Start-SemanticsWorker {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName,

        [Parameter(Mandatory)]
        [string]$TemporaryDirectory,

        [Parameter(Mandatory)]
        [string]$DispatcherAddress,

        [Parameter(Mandatory)]
        [string]$HostName,

        [Parameter(Mandatory)]
        [string]$ShutdownTimeout
    )

    Invoke-Docker -Arguments @(
        "run", "--detach",
        "--name", $ContainerName,
        "--add-host", "host.docker.internal:host-gateway",
        "--volume", "${TemporaryDirectory}:/work:ro",
        "--env", "QUARRY_DISPATCHER_ADDR=$DispatcherAddress",
        "--env", "QUARRY_WORKER_CONCURRENCY=1",
        "--env", "QUARRY_WORKER_HOSTNAME=$HostName",
        "--env", "QUARRY_WORKER_VERSION=semantics-test",
        "--env", "QUARRY_HEARTBEAT_INTERVAL=100ms",
        "--env", "QUARRY_WORKER_SHUTDOWN_TIMEOUT=$ShutdownTimeout",
        "postgres:18.6", "/work/quarry-worker-linux"
    ) | Out-Null
}

function Test-SemanticsWorkerRunning {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    $running = @(
        Invoke-Docker -Arguments @(
            "inspect", "--format", "{{.State.Running}}", $ContainerName
        )
    )
    return $running.Count -eq 1 -and $running[0].Trim() -eq "true"
}

function Wait-SemanticsWorker {
    param(
        [Parameter(Mandatory)]
        [string]$HostName,

        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (-not (Test-SemanticsWorkerRunning -ContainerName $ContainerName)) {
            throw "Semantics-test worker '$ContainerName' exited before registration."
        }
        $rows = @(
            Invoke-PostgresRows -Query "SELECT id::text FROM workers WHERE hostname = '$HostName';" |
                ForEach-Object { $_.Trim() } |
                Where-Object { $_ -match '^[0-9a-f-]{36}$' }
        )
        if ($rows.Count -eq 1) {
            return $rows[0]
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Semantics-test worker '$HostName' did not register within 30 seconds."
}

function Submit-SemanticsJob {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$Type,

        [Parameter(Mandatory)]
        [object]$Payload
    )

    $body = @{
        type = $Type
        payload = $Payload
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
        throw "Semantics-test submission did not return a queued job with an ID."
    }
    return $submitted.id
}

function Wait-SemanticsAttemptRenewal {
    param(
        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    $initial = $null
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (-not (Test-SemanticsWorkerRunning -ContainerName $ContainerName)) {
            throw "Semantics-test worker exited before renewing job '$JobID'."
        }
        $state = Get-RecoveryAttemptOneState -JobID $JobID
        if ($null -ne $state -and $state.Status -eq "running" -and $state.AttemptCount -eq "1") {
            if ($null -eq $initial) {
                $initial = $state
            }
            elseif ($state.WorkerID -eq $initial.WorkerID -and
                $state.LeaseExpiresAt -ne $initial.LeaseExpiresAt -and
                $state.LastSeenAt -ne $initial.LastSeenAt) {
                return $state
            }
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Semantics-test job '$JobID' did not start and renew within 30 seconds."
}

function Stop-SemanticsWorkerGracefully {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    Invoke-Docker -Arguments @("kill", "--signal", "SIGTERM", $ContainerName) | Out-Null
    $exitCodes = @(Invoke-Docker -Arguments @("wait", $ContainerName))
    if ($exitCodes.Count -ne 1 -or $exitCodes[0].Trim() -ne "0") {
        throw "Semantics-test worker '$ContainerName' exited with '$($exitCodes -join ',')', expected 0."
    }
}

function Remove-SemanticsWorker {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    $existing = @(
        Invoke-Docker -Arguments @(
            "ps", "--all", "--quiet", "--filter", "name=^/$ContainerName$"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($existing.Count -gt 0) {
        Invoke-Docker -Arguments @("rm", "--force", $ContainerName) | Out-Null
    }
}

function Wait-SemanticsJobStatus {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$ExpectedStatus,

        [int]$TimeoutSeconds = 30
    )

    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        $state = Invoke-RestMethod -Method Get -Uri "$BaseURL/v1/jobs/$JobID" -TimeoutSec 10
        if ($state.status -eq $ExpectedStatus) {
            return $state
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Semantics-test job '$JobID' did not reach '$ExpectedStatus' within $TimeoutSeconds seconds."
}

function Assert-SemanticsRows {
    param(
        [Parameter(Mandatory)]
        [string]$GracefulJobID,

        [Parameter(Mandatory)]
        [string]$CancelledJobID,

        [Parameter(Mandatory)]
        [string]$ForcedJobID,

        [Parameter(Mandatory)]
        [string]$GracefulWorkerID,

        [Parameter(Mandatory)]
        [string]$ForcedWorkerID,

        [Parameter(Mandatory)]
        [string]$ReplacementWorkerID
    )

    $query = @"
SELECT
    jobs.id::text,
    jobs.status,
    jobs.attempt_count,
    jobs.cancel_requested_at IS NOT NULL,
    attempts.attempt_no,
    attempts.worker_id::text,
    attempts.status,
    attempts.error_code,
    attempts.finished_at IS NOT NULL
FROM jobs
LEFT JOIN job_attempts AS attempts ON attempts.job_id = jobs.id
WHERE jobs.id IN (
    '$GracefulJobID'::uuid,
    '$CancelledJobID'::uuid,
    '$ForcedJobID'::uuid
)
ORDER BY jobs.id, attempts.attempt_no;
"@
    $rows = @(
        Invoke-PostgresRows -Query $query |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ -match '\|' }
    )
    if ($rows.Count -ne 4) {
        throw "PostgreSQL returned $($rows.Count) semantics rows, expected four."
    }

    $expected = @{
        "$GracefulJobID|1" = @("succeeded", "1", "f", $GracefulWorkerID, "succeeded", "", "t")
        "$CancelledJobID|" = @("cancelled", "0", "t", "", "", "", "f")
        "$ForcedJobID|1" = @("succeeded", "2", "f", $ForcedWorkerID, "abandoned", "lease_expired", "t")
        "$ForcedJobID|2" = @("succeeded", "2", "f", $ReplacementWorkerID, "succeeded", "", "t")
    }
    foreach ($row in $rows) {
        $columns = $row.Split('|')
        if ($columns.Count -ne 9) {
            throw "PostgreSQL returned an unexpected semantics row: '$row'."
        }
        $key = "$($columns[0])|$($columns[4])"
        if (-not $expected.ContainsKey($key)) {
            throw "PostgreSQL returned an unexpected semantics row key '$key'."
        }
        $want = $expected[$key]
        $actual = @($columns[1], $columns[2], $columns[3], $columns[5], $columns[6], $columns[7], $columns[8])
        if (-not [System.Linq.Enumerable]::SequenceEqual([string[]]$actual, [string[]]$want)) {
            throw "PostgreSQL semantics row '$row' did not match expected state."
        }
        $expected.Remove($key)
    }
    if ($expected.Count -ne 0) {
        throw "PostgreSQL did not return every expected semantics row."
    }
}

function Test-SemanticsProcesses {
    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-semantics-$testID"
    $apiBinary = Join-Path $temporaryDirectory "quarry-api.exe"
    $dispatcherBinary = Join-Path $temporaryDirectory "quarry-dispatcher.exe"
    if (-not $IsWindows) {
        $apiBinary = Join-Path $temporaryDirectory "quarry-api"
        $dispatcherBinary = Join-Path $temporaryDirectory "quarry-dispatcher"
    }
    $linuxWorkerBinary = Join-Path $temporaryDirectory "quarry-worker-linux"
    $composeProject = "quarry-m4-$testID"
    $gracefulContainer = "quarry-m4-graceful-$testID"
    $forcedContainer = "quarry-m4-forced-$testID"
    $replacementContainer = "quarry-m4-replacement-$testID"
    $workerContainers = @($gracefulContainer, $forcedContainer, $replacementContainer)
    $previousComposeProject = $env:COMPOSE_PROJECT_NAME
    $previousPostgresPort = $env:QUARRY_POSTGRES_PORT
    $apiProcess = $null
    $dispatcherProcess = $null
    $processIDs = [System.Collections.Generic.List[int]]::new()

    $env:COMPOSE_PROJECT_NAME = $composeProject
    $env:QUARRY_POSTGRES_PORT = [string](Get-AvailableLoopbackPort)
    $databaseURL = Get-PostgresConnectionString

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        Invoke-Go -Arguments @("build", "-o", $apiBinary, "./cmd/api")
        Invoke-Go -Arguments @("build", "-o", $dispatcherBinary, "./cmd/dispatcher")
        Build-LinuxWorkerBinary -Output $linuxWorkerBinary
        Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
        Invoke-Goose -MigrationCommand "up"

        $httpPort = Get-AvailableLoopbackPort
        $dispatcherPort = Get-AvailableLoopbackPort
        $httpAddress = "127.0.0.1:$httpPort"
        $dispatcherListenAddress = "0.0.0.0:$dispatcherPort"
        $containerDispatcherAddress = "host.docker.internal:$dispatcherPort"
        $baseURL = "http://$httpAddress"
        $apiProcess = Start-DistributedProcess -Binary $apiBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_HTTP_ADDR = $httpAddress
        }
        $processIDs.Add($apiProcess.Id)
        Wait-ApiReady -Process $apiProcess -BaseURL $baseURL
        $dispatcherProcess = Start-DistributedProcess -Binary $dispatcherBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_DISPATCHER_ADDR = $dispatcherListenAddress
            QUARRY_LEASE_DURATION = "1s"
            QUARRY_REAPER_INTERVAL = "100ms"
            QUARRY_REAPER_BATCH_SIZE = "10"
            QUARRY_WORKER_LIVENESS_TIMEOUT = "1s"
            QUARRY_RETRY_BASE_DELAY = "1ms"
            QUARRY_RETRY_MAX_DELAY = "1ms"
        }
        $processIDs.Add($dispatcherProcess.Id)
        Wait-TcpReady -Process $dispatcherProcess -HostName "127.0.0.1" -Port $dispatcherPort -ProcessName "Dispatcher"

        $gracefulJobID = Submit-SemanticsJob -BaseURL $baseURL -Type "demo.sleep" -Payload @{ duration_ms = 1500 }
        $gracefulHost = "semantics-graceful-$testID"
        Start-SemanticsWorker -ContainerName $gracefulContainer -TemporaryDirectory $temporaryDirectory `
            -DispatcherAddress $containerDispatcherAddress -HostName $gracefulHost -ShutdownTimeout "3s"
        $gracefulWorkerID = Wait-SemanticsWorker -HostName $gracefulHost -ContainerName $gracefulContainer
        $null = Wait-SemanticsAttemptRenewal -JobID $gracefulJobID -ContainerName $gracefulContainer
        $cancelledJobID = Submit-SemanticsJob -BaseURL $baseURL -Type "demo.echo" -Payload "must-not-run"
        Stop-SemanticsWorkerGracefully -ContainerName $gracefulContainer
        $null = Wait-SemanticsJobStatus -BaseURL $baseURL -JobID $gracefulJobID -ExpectedStatus "succeeded"
        $queued = Invoke-RestMethod -Method Get -Uri "$baseURL/v1/jobs/$cancelledJobID" -TimeoutSec 10
        if ($queued.status -ne "queued" -or $queued.attempt_count -ne 0) {
            throw "Graceful worker acquired work after SIGTERM."
        }
        $cancelled = Invoke-RestMethod -Method Post -Uri "$baseURL/v1/jobs/$cancelledJobID/cancel" -TimeoutSec 10
        if ($cancelled.status -ne "cancelled") {
            throw "Queued semantics job did not cancel."
        }

        $forcedJobID = Submit-SemanticsJob -BaseURL $baseURL -Type "demo.sleep" -Payload @{ duration_ms = 1500 }
        $forcedHost = "semantics-forced-$testID"
        Start-SemanticsWorker -ContainerName $forcedContainer -TemporaryDirectory $temporaryDirectory `
            -DispatcherAddress $containerDispatcherAddress -HostName $forcedHost -ShutdownTimeout "200ms"
        $forcedWorkerID = Wait-SemanticsWorker -HostName $forcedHost -ContainerName $forcedContainer
        $null = Wait-SemanticsAttemptRenewal -JobID $forcedJobID -ContainerName $forcedContainer
        Stop-SemanticsWorkerGracefully -ContainerName $forcedContainer
        $forcedRows = @(
            Invoke-PostgresRows -Query @"
SELECT jobs.status, jobs.attempt_count, jobs.cancel_requested_at IS NULL, attempts.status, attempts.finished_at IS NULL
FROM jobs
JOIN job_attempts AS attempts ON attempts.job_id = jobs.id
WHERE jobs.id = '$forcedJobID'::uuid AND attempts.attempt_no = 1;
"@ | ForEach-Object { $_.Trim() } | Where-Object { $_ -match '\|' }
        )
        if ($forcedRows.Count -ne 1 -or $forcedRows[0] -ne "running|1|t|running|t") {
            throw "Forced shutdown reported or cancelled unfinished attempt 1: '$($forcedRows -join ',')'."
        }

        $replacementHost = "semantics-replacement-$testID"
        Start-SemanticsWorker -ContainerName $replacementContainer -TemporaryDirectory $temporaryDirectory `
            -DispatcherAddress $containerDispatcherAddress -HostName $replacementHost -ShutdownTimeout "3s"
        $replacementWorkerID = Wait-SemanticsWorker -HostName $replacementHost -ContainerName $replacementContainer
        $finalState = Wait-SemanticsJobStatus -BaseURL $baseURL -JobID $forcedJobID -ExpectedStatus "succeeded" -TimeoutSeconds 30
        if ($finalState.attempt_count -ne 2 -or $finalState.result.slept_ms -ne 1500) {
            throw "Replacement attempt did not complete forced-shutdown job."
        }
        Stop-SemanticsWorkerGracefully -ContainerName $replacementContainer
        Assert-SemanticsRows -GracefulJobID $gracefulJobID -CancelledJobID $cancelledJobID `
            -ForcedJobID $forcedJobID -GracefulWorkerID $gracefulWorkerID `
            -ForcedWorkerID $forcedWorkerID -ReplacementWorkerID $replacementWorkerID
        Write-Host "Semantics process test passed: SIGTERM drain, acquisition stop, forced lease recovery, and PostgreSQL state verified."
    }
    finally {
        try {
            foreach ($container in $workerContainers) {
                Remove-SemanticsWorker -ContainerName $container
            }
            foreach ($process in @($dispatcherProcess, $apiProcess)) {
                if ($null -ne $process) {
                    Stop-DistributedProcess -Process $process
                }
            }
        }
        finally {
            try {
                Invoke-Docker -Arguments @("compose", "down", "--volumes")
            }
            finally {
                try {
                    Remove-DistributedTestDirectory -Directory $temporaryDirectory
                }
                finally {
                    $env:COMPOSE_PROJECT_NAME = $previousComposeProject
                    $env:QUARRY_POSTGRES_PORT = $previousPostgresPort
                }
            }
        }
    }

    Assert-RecoveryCleanup -ComposeProject $composeProject -TemporaryDirectory $temporaryDirectory -ProcessIDs $processIDs.ToArray()
    foreach ($container in $workerContainers) {
        $remaining = @(
            Invoke-Docker -Arguments @("ps", "--all", "--quiet", "--filter", "name=^/$container$") |
                Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
        )
        if ($remaining.Count -ne 0) {
            throw "Semantics-test worker container '$container' remains after cleanup."
        }
    }
    Write-Host "Semantics-test cleanup verified: processes, worker containers, temporary binaries, Compose resources, network, and volume removed."
}

function Test-Semantics {
    Test-SemanticsProcesses
    Invoke-Go -Arguments @(
        "test", "-count=1",
        "-run", "^(TestWorkerRetryableFailureRetriesUntilMaximumAttempts|TestWorkerPermanentFailureDoesNotRetry|TestWorkerTimeoutRetriesUntilMaximumAttempts|TestWorkerPanicRetriesUntilMaximumAttempts|TestPendingJobsCancelThroughHTTPAndPostgres|TestRunningJobCancelsThroughHTTPGRPCWorkerAndPostgres)$",
        "./internal/dispatcher"
    )
    Invoke-Go -Arguments @(
        "test", "-count=1",
        "-run", "^(TestDispatcherStoreSchedulesRetryableFailureWithExactDelay|TestAPIConcurrentIdempotentSubmissionsCreateOneJob)$",
        "./internal/store/postgres"
    )
}

function Test-RecoveryProcesses {
    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-recovery-$testID"
    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $apiBinary = Join-Path $temporaryDirectory "quarry-api$binaryExtension"
    $dispatcherBinary = Join-Path $temporaryDirectory "quarry-dispatcher$binaryExtension"
    $workerBinary = Join-Path $temporaryDirectory "quarry-worker$binaryExtension"
    $composeProject = "quarry-m3-$testID"
    $previousComposeProject = $env:COMPOSE_PROJECT_NAME
    $previousPostgresPort = $env:QUARRY_POSTGRES_PORT
    $apiProcess = $null
    $dispatcherProcess = $null
    $firstWorkerProcess = $null
    $secondWorkerProcess = $null
    $processIDs = [System.Collections.Generic.List[int]]::new()

    $env:COMPOSE_PROJECT_NAME = $composeProject
    $env:QUARRY_POSTGRES_PORT = [string](Get-AvailableLoopbackPort)
    $databaseURL = Get-PostgresConnectionString

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        Invoke-Go -Arguments @("build", "-o", $apiBinary, "./cmd/api")
        Invoke-Go -Arguments @("build", "-o", $dispatcherBinary, "./cmd/dispatcher")
        Invoke-Go -Arguments @("build", "-o", $workerBinary, "./cmd/worker")
        Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
        Invoke-Goose -MigrationCommand "up"

        $httpPort = Get-AvailableLoopbackPort
        $dispatcherPort = Get-AvailableLoopbackPort
        $httpAddress = "127.0.0.1:$httpPort"
        $dispatcherAddress = "127.0.0.1:$dispatcherPort"
        $baseURL = "http://$httpAddress"
        $apiProcess = Start-DistributedProcess -Binary $apiBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_HTTP_ADDR = $httpAddress
        }
        $processIDs.Add($apiProcess.Id)
        Wait-ApiReady -Process $apiProcess -BaseURL $baseURL

        $dispatcherProcess = Start-DistributedProcess -Binary $dispatcherBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_DISPATCHER_ADDR = $dispatcherAddress
            QUARRY_LEASE_DURATION = "2s"
            QUARRY_REAPER_INTERVAL = "200ms"
            QUARRY_REAPER_BATCH_SIZE = "10"
            QUARRY_WORKER_LIVENESS_TIMEOUT = "2s"
        }
        $processIDs.Add($dispatcherProcess.Id)
        Wait-TcpReady `
            -Process $dispatcherProcess `
            -HostName "127.0.0.1" `
            -Port $dispatcherPort `
            -ProcessName "Dispatcher"

        $body = @{
            type = "demo.sleep"
            payload = @{ duration_ms = 6000 }
            max_attempts = 3
            timeout_ms = 30000
        } | ConvertTo-Json -Compress -Depth 4
        $submitted = Invoke-RestMethod `
            -Method Post `
            -Uri "$BaseURL/v1/jobs" `
            -ContentType "application/json" `
            -Body $body `
            -TimeoutSec 10
        if ([string]::IsNullOrWhiteSpace($submitted.id) -or $submitted.status -ne 'queued') {
            throw "Recovery submission did not return a queued job with an ID."
        }

        $firstWorkerHostName = "recovery-worker-1-$testID"
        $firstWorkerProcess = Start-DistributedProcess -Binary $workerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $dispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "1"
            QUARRY_WORKER_HOSTNAME = $firstWorkerHostName
            QUARRY_WORKER_VERSION = "recovery-test"
            QUARRY_HEARTBEAT_INTERVAL = "250ms"
        }
        $processIDs.Add($firstWorkerProcess.Id)
        $firstWorkerIDs = @(Wait-DistributedWorkers `
            -HostNames @($firstWorkerHostName) `
            -Processes @($firstWorkerProcess))
        $firstWorkerID = $firstWorkerIDs[0]
        $renewedState = Wait-RecoveryAttemptOneRenewal `
            -JobID $submitted.id `
            -WorkerProcess $firstWorkerProcess
        if ($renewedState.WorkerID -ne $firstWorkerID) {
            throw "Attempt 1 ran on '$($renewedState.WorkerID)', expected worker 1 '$firstWorkerID'."
        }

        Stop-CrashedWorkerProcess -Process $firstWorkerProcess
        $firstWorkerProcess = $null
        $stoppedState = Get-RecoveryAttemptOneState -JobID $submitted.id
        if ($null -eq $stoppedState) {
            throw "Attempt 1 left running state before heartbeat stoppage could be observed."
        }
        Start-Sleep -Milliseconds 750
        $unchangedState = Get-RecoveryAttemptOneState -JobID $submitted.id
        if ($null -eq $unchangedState -or
            $unchangedState.LeaseExpiresAt -ne $stoppedState.LeaseExpiresAt -or
            $unchangedState.LastSeenAt -ne $stoppedState.LastSeenAt) {
            throw "Worker 1 lease or last_seen_at advanced after crash injection."
        }

        $secondWorkerHostName = "recovery-worker-2-$testID"
        $secondWorkerProcess = Start-DistributedProcess -Binary $workerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $dispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "1"
            QUARRY_WORKER_HOSTNAME = $secondWorkerHostName
            QUARRY_WORKER_VERSION = "recovery-test"
            QUARRY_HEARTBEAT_INTERVAL = "250ms"
        }
        $processIDs.Add($secondWorkerProcess.Id)
        $secondWorkerIDs = @(Wait-DistributedWorkers `
            -HostNames @($secondWorkerHostName) `
            -Processes @($secondWorkerProcess))
        $secondWorkerID = $secondWorkerIDs[0]

        $finalState = Wait-RecoveryJobSucceeded `
            -BaseURL $baseURL `
            -JobID $submitted.id `
            -Processes @($apiProcess, $dispatcherProcess, $secondWorkerProcess)
        Assert-RecoveryState `
            -BaseURL $baseURL `
            -JobID $submitted.id `
            -FirstWorkerID $firstWorkerID `
            -SecondWorkerID $secondWorkerID `
            -JobState $finalState
        Write-Host "Recovery test passed: worker 1 crashed after renewing attempt 1, worker 2 completed attempt 2, HTTP and PostgreSQL state verified."
    }
    finally {
        try {
            $stopErrors = @()
            foreach ($process in @($secondWorkerProcess, $firstWorkerProcess, $dispatcherProcess, $apiProcess)) {
                if ($null -eq $process) {
                    continue
                }
                try {
                    Stop-DistributedProcess -Process $process
                }
                catch {
                    $stopErrors += $_
                }
            }
            if ($stopErrors.Count -gt 0) {
                throw "Failed to stop $($stopErrors.Count) recovery-test processes."
            }
        }
        finally {
            try {
                Invoke-Docker -Arguments @("compose", "down", "--volumes")
            }
            finally {
                try {
                    Remove-DistributedTestDirectory -Directory $temporaryDirectory
                }
                finally {
                    $env:COMPOSE_PROJECT_NAME = $previousComposeProject
                    $env:QUARRY_POSTGRES_PORT = $previousPostgresPort
                }
            }
        }
    }

    Assert-RecoveryCleanup `
        -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory `
        -ProcessIDs $processIDs.ToArray()
    Write-Host "Recovery-test cleanup verified: processes, temporary binaries, containers, network, and volume removed."
}

function Test-Recovery {
    Test-RecoveryProcesses
    Invoke-Go -Arguments @(
        "test", "-count=1",
        "-run", "^TestStaleAttemptReportAfterRecoveryThroughGRPCAndPostgres$",
        "./internal/dispatcher"
    )
}

function Get-ConfiguredPort {
    param(
        [Parameter(Mandatory)]
        [string]$EnvironmentVariable,

        [Parameter(Mandatory)]
        [int]$Default
    )

    $configured = [Environment]::GetEnvironmentVariable($EnvironmentVariable)
    if ([string]::IsNullOrWhiteSpace($configured)) {
        return $Default
    }

    $port = 0
    if (-not [int]::TryParse($configured, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        throw "$EnvironmentVariable must be a TCP port from 1 through 65535."
    }
    return $port
}

function Wait-ObservabilityEndpoint {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [string]$URL
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-WebRequest -Uri $URL -TimeoutSec 2 -UseBasicParsing
            if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 300) {
                return
            }
        }
        catch {
            Start-Sleep -Milliseconds 500
        }
    }

    throw "$Name did not become ready at $URL within 60 seconds."
}

function Test-ObservabilityConfiguration {
    $prometheusConfig = (Resolve-Path -LiteralPath "deploy/observability/prometheus.yml").Path
    $collectorConfig = (Resolve-Path -LiteralPath "deploy/observability/otel-collector.yaml").Path

    Invoke-Docker -Arguments @("compose", "config", "--quiet")
    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--volume", "${prometheusConfig}:/etc/prometheus/prometheus.yml:ro",
        "--entrypoint", "promtool",
        "prom/prometheus:v3.12.0",
        "check", "config", "/etc/prometheus/prometheus.yml"
    )
    Invoke-Docker -Arguments @(
        "run", "--rm",
        "--volume", "${collectorConfig}:/etc/otelcol-contrib/config.yaml:ro",
        "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.153.0",
        "validate", "--config=/etc/otelcol-contrib/config.yaml"
    )
    Invoke-Go -Arguments @("test", "-count=1", "./deploy/observability")
}

function Start-ObservabilityInfrastructure {
    Test-ObservabilityConfiguration
    Invoke-Docker -Arguments @(
        "compose", "up", "--detach", "--wait",
        "prometheus", "jaeger", "otel-collector", "grafana"
    )

    $prometheusPort = Get-ConfiguredPort -EnvironmentVariable "QUARRY_PROMETHEUS_PORT" -Default 9091
    $grafanaPort = Get-ConfiguredPort -EnvironmentVariable "QUARRY_GRAFANA_PORT" -Default 3000
    $collectorHealthPort = Get-ConfiguredPort -EnvironmentVariable "QUARRY_OTEL_HEALTH_PORT" -Default 13133
    $jaegerPort = Get-ConfiguredPort -EnvironmentVariable "QUARRY_JAEGER_PORT" -Default 16686

    Wait-ObservabilityEndpoint -Name "Prometheus" -URL "http://127.0.0.1:$prometheusPort/-/ready"
    Wait-ObservabilityEndpoint -Name "Grafana" -URL "http://127.0.0.1:$grafanaPort/api/health"
    Wait-ObservabilityEndpoint -Name "OpenTelemetry Collector" -URL "http://127.0.0.1:$collectorHealthPort/"
    Wait-ObservabilityEndpoint -Name "Jaeger" -URL "http://127.0.0.1:$jaegerPort/api/services"

    Write-Host "Prometheus: http://127.0.0.1:$prometheusPort"
    Write-Host "Grafana: http://127.0.0.1:$grafanaPort/d/quarry-overview/quarry"
    Write-Host "Jaeger: http://127.0.0.1:$jaegerPort"
}

function Stop-ObservabilityInfrastructure {
    Invoke-Docker -Arguments @(
        "compose", "stop",
        "prometheus", "jaeger", "otel-collector", "grafana"
    )
    Invoke-Docker -Arguments @(
        "compose", "rm", "--force",
        "prometheus", "jaeger", "otel-collector", "grafana"
    )
}

function Start-ObservabilityProcess {
    param(
        [Parameter(Mandatory)]
        [string]$Binary,

        [Parameter(Mandatory)]
        [hashtable]$Environment
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $Binary
    $startInfo.WorkingDirectory = $repositoryRoot
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($entry in $Environment.GetEnumerator()) {
        $startInfo.Environment[$entry.Key] = $entry.Value
    }

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        $process.Dispose()
        throw "Failed to start observability-test process '$Binary'."
    }

    return [PSCustomObject]@{
        Process = $process
        StandardOutput = $process.StandardOutput.ReadToEndAsync()
        StandardError = $process.StandardError.ReadToEndAsync()
        Output = ""
        Stopped = $false
    }
}

function Stop-ObservabilityProcess {
    param(
        [Parameter(Mandatory)]
        [PSCustomObject]$Handle
    )

    if ($Handle.Stopped) {
        return $Handle.Output
    }

    $process = $Handle.Process
    try {
        if (-not $process.HasExited) {
            if (-not $IsWindows) {
                & kill -TERM $process.Id
                if ($LASTEXITCODE -ne 0 -or -not $process.WaitForExit(10000)) {
                    $process.Kill($true)
                }
            }
            else {
                $process.Kill($true)
            }
            if (-not $process.WaitForExit(10000)) {
                throw "Observability-test process $($process.Id) did not exit."
            }
        }

        $standardOutput = $Handle.StandardOutput.GetAwaiter().GetResult()
        $standardError = $Handle.StandardError.GetAwaiter().GetResult()
        $Handle.Output = "$standardOutput`n$standardError"
        $Handle.Stopped = $true
        return $Handle.Output
    }
    finally {
        $process.Dispose()
    }
}

function Wait-PrometheusTargets {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL
    )

    $requiredJobs = @("quarry-api", "quarry-dispatcher", "quarry-worker")
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-RestMethod -Uri "$BaseURL/api/v1/targets" -TimeoutSec 5
            $healthyJobs = @(
                $response.data.activeTargets |
                    Where-Object { $_.health -eq "up" } |
                    ForEach-Object { $_.labels.job }
            )
            $missingJobs = @($requiredJobs | Where-Object { $_ -notin $healthyJobs })
            if ($missingJobs.Count -eq 0) {
                return
            }
        }
        catch {
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Prometheus did not report all Quarry targets healthy within 45 seconds."
}

function Wait-PrometheusMetricFamilies {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL
    )

    $requiredFamilies = @(
        "quarry_jobs_submitted_total",
        "quarry_job_attempts_total",
        "quarry_job_execution_duration_seconds",
        "quarry_job_scheduling_delay_seconds",
        "quarry_queue_depth",
        "quarry_oldest_queued_job_age_seconds",
        "quarry_active_jobs",
        "quarry_active_workers",
        "quarry_stale_reports_total",
        "quarry_dispatch_claim_size"
    )
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    $missing = $requiredFamilies
    while ([DateTime]::UtcNow -lt $deadline) {
        $missing = @()
        foreach ($family in $requiredFamilies) {
            try {
                $encodedFamily = [Uri]::EscapeDataString($family)
                $response = Invoke-RestMethod -Uri "$BaseURL/api/v1/metadata?metric=$encodedFamily" -TimeoutSec 5
                if ($response.status -ne "success" -or
                    $response.data.PSObject.Properties.Name -notcontains $family) {
                    $missing += $family
                }
            }
            catch {
                $missing += $family
            }
        }
        if ($missing.Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Prometheus metadata is missing required metric families: $($missing -join ', ')."
}

function Wait-PrometheusValue {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$Expression,

        [Parameter(Mandatory)]
        [scriptblock]$Accept,

        [Parameter(Mandatory)]
        [string]$Description
    )

    $encodedExpression = [Uri]::EscapeDataString($Expression)
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    $lastValue = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-RestMethod -Uri "$BaseURL/api/v1/query?query=$encodedExpression" -TimeoutSec 5
            $results = @($response.data.result)
            if ($response.status -eq "success" -and $results.Count -gt 0) {
                $lastValue = [double]::Parse(
                    [string]$results[0].value[1],
                    [Globalization.CultureInfo]::InvariantCulture
                )
                if (& $Accept $lastValue) {
                    return $lastValue
                }
            }
        }
        catch {
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Prometheus did not report $Description within 45 seconds. Last value: $lastValue"
}

function Assert-GrafanaDashboard {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL
    )

    $response = Invoke-RestMethod -Uri "$BaseURL/api/dashboards/uid/quarry-overview" -TimeoutSec 10
    if ($response.dashboard.uid -ne "quarry-overview" -or
        $response.dashboard.title -ne "Quarry" -or
        @($response.dashboard.panels).Count -ne 13) {
        throw "Grafana did not return the provisioned 13-panel Quarry dashboard."
    }
}

function Wait-JaegerJobTrace {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID
    )

    $tags = [Uri]::EscapeDataString((@{ "job.id" = $JobID } | ConvertTo-Json -Compress))
    $requiredOperations = @(
        "POST",
        "db.insert_job",
        "dispatcher.claim",
        "db.claim_job",
        "worker.execute",
        "handler",
        "db.complete_attempt"
    )
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    $lastOperations = @()
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-RestMethod `
                -Uri "$BaseURL/api/traces?service=quarry-api&tags=$tags&limit=20" `
                -TimeoutSec 5
            foreach ($trace in @($response.data)) {
                $operations = @($trace.spans | ForEach-Object { $_.operationName })
                $lastOperations = $operations
                $missing = @($requiredOperations | Where-Object { $_ -notin $operations })
                $hasReport = @($operations | Where-Object { $_ -like "*ReportAttempt*" }).Count -gt 0
                if ($missing.Count -eq 0 -and $hasReport) {
                    return [string]$trace.traceID
                }
            }
        }
        catch {
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Jaeger did not return the complete job trace within 45 seconds. Last operations: $($lastOperations -join ', ')."
}

function Assert-ObservabilityLogs {
    param(
        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$TraceID,

        [Parameter(Mandatory)]
        [string]$ApiOutput,

        [Parameter(Mandatory)]
        [string]$DispatcherOutput,

        [Parameter(Mandatory)]
        [string]$WorkerOutput
    )

    $expectedMessages = @(
        @{ Name = "API submission"; Output = $ApiOutput; Message = '"msg":"job submitted"' },
        @{ Name = "dispatcher claim"; Output = $DispatcherOutput; Message = '"msg":"job claimed"' },
        @{ Name = "dispatcher completion"; Output = $DispatcherOutput; Message = '"msg":"attempt completed"' },
        @{ Name = "worker start"; Output = $WorkerOutput; Message = '"msg":"attempt started"' },
        @{ Name = "worker acknowledgement"; Output = $WorkerOutput; Message = '"msg":"attempt report acknowledged"' }
    )
    foreach ($expected in $expectedMessages) {
        if (-not $expected.Output.Contains($expected.Message) -or
            -not $expected.Output.Contains($JobID)) {
            throw "Observability-test logs do not contain the $($expected.Name) for job $JobID."
        }
    }

    $combined = "$ApiOutput`n$DispatcherOutput`n$WorkerOutput"
    if (-not $combined.Contains($TraceID) -or
        -not $combined.Contains('"job_outcome":"succeeded"')) {
        throw "Observability-test logs do not connect job $JobID, trace $TraceID, and the succeeded outcome."
    }
}

function Assert-ObservabilityApplicationPortsAvailable {
    foreach ($port in @(8080, 9090, 9464, 9465)) {
        $listener = [System.Net.Sockets.TcpListener]::new(
            [System.Net.IPAddress]::Loopback,
            $port
        )
        try {
            $listener.Start()
        }
        catch {
            throw "Observability-test requires loopback port $port, but another process is using it."
        }
        finally {
            $listener.Stop()
        }
    }
}

function Test-ObservabilityWorkflow {
    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-observability-$testID"
    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $apiBinary = Join-Path $temporaryDirectory "quarry-api$binaryExtension"
    $dispatcherBinary = Join-Path $temporaryDirectory "quarry-dispatcher$binaryExtension"
    $workerBinary = Join-Path $temporaryDirectory "quarry-worker$binaryExtension"
    $composeProject = "quarry-m5-$testID"
    $savedEnvironment = @{}
    $environmentNames = @(
        "COMPOSE_PROJECT_NAME",
        "QUARRY_POSTGRES_PORT",
        "QUARRY_PROMETHEUS_PORT",
        "QUARRY_GRAFANA_PORT",
        "QUARRY_OTEL_GRPC_PORT",
        "QUARRY_OTEL_HTTP_PORT",
        "QUARRY_OTEL_HEALTH_PORT",
        "QUARRY_JAEGER_PORT"
    )
    foreach ($name in $environmentNames) {
        $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name)
    }

    $ports = [System.Collections.Generic.List[int]]::new()
    while ($ports.Count -lt 7) {
        $candidate = Get-AvailableLoopbackPort
        if (-not $ports.Contains($candidate)) {
            $ports.Add($candidate)
        }
    }

    $env:COMPOSE_PROJECT_NAME = $composeProject
    $env:QUARRY_POSTGRES_PORT = [string]$ports[0]
    $env:QUARRY_PROMETHEUS_PORT = [string]$ports[1]
    $env:QUARRY_GRAFANA_PORT = [string]$ports[2]
    $env:QUARRY_OTEL_GRPC_PORT = [string]$ports[3]
    $env:QUARRY_OTEL_HTTP_PORT = [string]$ports[4]
    $env:QUARRY_OTEL_HEALTH_PORT = [string]$ports[5]
    $env:QUARRY_JAEGER_PORT = [string]$ports[6]

    $databaseURL = Get-PostgresConnectionString
    $collectorEndpoint = "http://127.0.0.1:$($env:QUARRY_OTEL_HTTP_PORT)"
    $prometheusURL = "http://127.0.0.1:$($env:QUARRY_PROMETHEUS_PORT)"
    $grafanaURL = "http://127.0.0.1:$($env:QUARRY_GRAFANA_PORT)"
    $jaegerURL = "http://127.0.0.1:$($env:QUARRY_JAEGER_PORT)"
    $apiHandle = $null
    $dispatcherHandle = $null
    $workerHandle = $null
    $processIDs = [System.Collections.Generic.List[int]]::new()

    try {
        Assert-ObservabilityApplicationPortsAvailable
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        Invoke-Go -Arguments @("build", "-o", $apiBinary, "./cmd/api")
        Invoke-Go -Arguments @("build", "-o", $dispatcherBinary, "./cmd/dispatcher")
        Invoke-Go -Arguments @("build", "-o", $workerBinary, "./cmd/worker")
        Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
        Invoke-Goose -MigrationCommand "up"
        Start-ObservabilityInfrastructure

        $apiHandle = Start-ObservabilityProcess -Binary $apiBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_HTTP_ADDR = "127.0.0.1:8080"
            OTEL_EXPORTER_OTLP_ENDPOINT = $collectorEndpoint
        }
        $processIDs.Add($apiHandle.Process.Id)
        Wait-ApiReady -Process $apiHandle.Process -BaseURL "http://127.0.0.1:8080"

        $dispatcherHandle = Start-ObservabilityProcess -Binary $dispatcherBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_DISPATCHER_ADDR = "127.0.0.1:9090"
            QUARRY_DISPATCHER_METRICS_ADDR = "127.0.0.1:9464"
            OTEL_EXPORTER_OTLP_ENDPOINT = $collectorEndpoint
        }
        $processIDs.Add($dispatcherHandle.Process.Id)
        Wait-TcpReady -Process $dispatcherHandle.Process -HostName "127.0.0.1" -Port 9090 -ProcessName "Dispatcher"

        $workerHostName = "observability-worker-$testID"
        $workerHandle = Start-ObservabilityProcess -Binary $workerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = "127.0.0.1:9090"
            QUARRY_WORKER_CONCURRENCY = "1"
            QUARRY_WORKER_HOSTNAME = $workerHostName
            QUARRY_WORKER_VERSION = "observability-test"
            QUARRY_WORKER_METRICS_ADDR = "127.0.0.1:9465"
            OTEL_EXPORTER_OTLP_ENDPOINT = $collectorEndpoint
        }
        $processIDs.Add($workerHandle.Process.Id)
        $null = Wait-DistributedWorkers -HostNames @($workerHostName) -Processes @($workerHandle.Process)

        Wait-PrometheusTargets -BaseURL $prometheusURL

        $body = @{
            type = "demo.echo"
            payload = @{ message = "observability-test" }
            max_attempts = 1
            timeout_ms = 30000
        } | ConvertTo-Json -Compress -Depth 4
        $submitted = Invoke-RestMethod `
            -Method Post `
            -Uri "http://127.0.0.1:8080/v1/jobs" `
            -ContentType "application/json" `
            -Body $body `
            -TimeoutSec 10
        if ([string]::IsNullOrWhiteSpace($submitted.id) -or $submitted.status -ne "queued") {
            throw "Observability-test submission did not return a queued job with an ID."
        }

        $deadline = [DateTime]::UtcNow.AddSeconds(30)
        $jobState = $null
        while ([DateTime]::UtcNow -lt $deadline) {
            $jobState = Invoke-RestMethod -Uri "http://127.0.0.1:8080/v1/jobs/$($submitted.id)" -TimeoutSec 5
            if ($jobState.status -eq "succeeded") {
                break
            }
            if ($jobState.status -in @("failed", "dead_lettered", "cancelled")) {
                throw "Observability-test job reached unexpected terminal status '$($jobState.status)'."
            }
            Start-Sleep -Milliseconds 100
        }
        if ($null -eq $jobState -or $jobState.status -ne "succeeded" -or
            $jobState.result.message -ne "observability-test") {
            throw "Observability-test job did not complete with the expected public API result."
        }
        $attempts = @((Invoke-RestMethod -Uri "http://127.0.0.1:8080/v1/jobs/$($submitted.id)/attempts" -TimeoutSec 5).attempts)
        if ($attempts.Count -ne 1 -or $attempts[0].status -ne "succeeded") {
            throw "Observability-test public API did not return one succeeded attempt."
        }

        Wait-PrometheusMetricFamilies -BaseURL $prometheusURL
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "sum(quarry_jobs_submitted_total)" -Accept { param($value) $value -ge 1 } `
            -Description "at least one submitted job"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression 'sum(quarry_job_attempts_total{outcome="succeeded"})' -Accept { param($value) $value -ge 1 } `
            -Description "at least one succeeded attempt"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression 'sum(quarry_job_execution_duration_seconds_count{outcome="succeeded"})' -Accept { param($value) $value -ge 1 } `
            -Description "at least one measured successful execution"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "sum(quarry_job_scheduling_delay_seconds_count)" -Accept { param($value) $value -ge 1 } `
            -Description "at least one measured scheduling delay"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "sum(quarry_dispatch_claim_size_count)" -Accept { param($value) $value -ge 1 } `
            -Description "at least one measured dispatcher claim"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "max(quarry_queue_snapshot_up)" -Accept { param($value) $value -eq 1 } `
            -Description "a successful queue snapshot"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "sum(quarry_queue_depth)" -Accept { param($value) $value -eq 0 } `
            -Description "zero pending jobs"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "max(quarry_oldest_queued_job_age_seconds)" -Accept { param($value) $value -eq 0 } `
            -Description "zero oldest eligible job age"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "max(quarry_active_jobs)" -Accept { param($value) $value -eq 0 } `
            -Description "zero active jobs"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression "max(quarry_active_workers)" -Accept { param($value) $value -ge 1 } `
            -Description "at least one active worker"

        Assert-GrafanaDashboard -BaseURL $grafanaURL
        $traceID = Wait-JaegerJobTrace -BaseURL $jaegerURL -JobID $submitted.id

        $workerOutput = Stop-ObservabilityProcess -Handle $workerHandle
        $dispatcherOutput = Stop-ObservabilityProcess -Handle $dispatcherHandle
        $apiOutput = Stop-ObservabilityProcess -Handle $apiHandle
        Assert-ObservabilityLogs -JobID $submitted.id -TraceID $traceID `
            -ApiOutput $apiOutput -DispatcherOutput $dispatcherOutput -WorkerOutput $workerOutput

        Write-Host "Observability test passed: job $($submitted.id), trace $traceID, API, logs, Prometheus, Grafana, and Jaeger verified."
    }
    finally {
        foreach ($handle in @($workerHandle, $dispatcherHandle, $apiHandle)) {
            if ($null -ne $handle -and -not $handle.Stopped) {
                try {
                    $null = Stop-ObservabilityProcess -Handle $handle
                }
                catch {
                    Write-Warning "Failed to stop observability-test process $($handle.Process.Id): $_"
                }
            }
        }
        try {
            Invoke-Docker -Arguments @("compose", "down", "--volumes", "--remove-orphans")
        }
        finally {
            try {
                Remove-DistributedTestDirectory -Directory $temporaryDirectory
            }
            finally {
                foreach ($name in $environmentNames) {
                    [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name])
                }
            }
        }
    }

    Assert-RecoveryCleanup -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory -ProcessIDs $processIDs.ToArray()
    Write-Host "Observability-test cleanup verified: processes, temporary binaries, containers, network, and volume removed."
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
            Test-BufGeneratedCode
            Test-GoVet
            Test-GoPackages
            Test-GoBuild
            Test-ObservabilityConfiguration
            Test-ObservabilityWorkflow
            Test-ComposeSmoke
            Test-DistributedProcesses
            Test-Recovery
            Test-Semantics
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
        "restart-test" {
            Invoke-Go -Arguments @(
                "test", "-count=1",
                "-run", "^TestAPIJobSurvivesServerAndPoolRestart$",
                "./internal/store/postgres"
            )
        }
        "generate" {
            Invoke-Sqlc -SqlcCommand "generate"
            Invoke-Buf -Arguments @("generate")
        }
        "generate-check" {
            Invoke-Sqlc -SqlcCommand "diff"
            Test-BufGeneratedCode
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
        "distributed-test" {
            Test-DistributedProcesses
        }
        "recovery-test" {
            Test-Recovery
        }
        "semantics-test" {
            Test-Semantics
        }
        "observability-config-test" {
            Test-ObservabilityConfiguration
        }
        "observability-test" {
            Test-ObservabilityWorkflow
        }
        "observability-up" {
            Start-ObservabilityInfrastructure
        }
        "observability-down" {
            Stop-ObservabilityInfrastructure
        }
    }
}
finally {
    Pop-Location
}
