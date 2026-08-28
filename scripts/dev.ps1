param(
    [ValidateSet(
        "check", "test", "tools",
        "db-config", "db-up", "db-ready", "db-down",
        "migrate-up", "migrate-down", "migrate-status", "migration-test", "restart-test",
        "generate", "generate-check", "format-check", "vet", "build",
        "smoke-test", "distributed-test", "recovery-test", "ack-loss-test", "failure-test", "semantics-test",
        "benchmark-smoke", "benchmark", "benchmark-verify",
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
        [hashtable]$Environment,

        [string[]]$Arguments = @(),

        [string]$StandardOutputPath,

        [string]$StandardErrorPath
    )

    $captureOutput = -not [string]::IsNullOrWhiteSpace($StandardOutputPath) -or
        -not [string]::IsNullOrWhiteSpace($StandardErrorPath)
    if ($captureOutput -and
        ([string]::IsNullOrWhiteSpace($StandardOutputPath) -or [string]::IsNullOrWhiteSpace($StandardErrorPath))) {
        throw "Distributed process output capture requires both output paths."
    }

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $Binary
    $startInfo.WorkingDirectory = $repositoryRoot
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $captureOutput
    $startInfo.RedirectStandardError = $captureOutput
    foreach ($argument in $Arguments) {
        $startInfo.ArgumentList.Add($argument)
    }
    foreach ($entry in $Environment.GetEnumerator()) {
        $startInfo.Environment[$entry.Key] = $entry.Value
    }

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) {
        $process.Dispose()
        throw "Failed to start distributed-test process '$Binary'."
    }

    if ($captureOutput) {
        $process | Add-Member -NotePropertyName QuarryStandardOutputPath -NotePropertyValue $StandardOutputPath
        $process | Add-Member -NotePropertyName QuarryStandardErrorPath -NotePropertyValue $StandardErrorPath
        $process | Add-Member -NotePropertyName QuarryStandardOutputTask -NotePropertyValue $process.StandardOutput.ReadToEndAsync()
        $process | Add-Member -NotePropertyName QuarryStandardErrorTask -NotePropertyValue $process.StandardError.ReadToEndAsync()
        $process | Add-Member -NotePropertyName QuarryCaptureCompleted -NotePropertyValue $false
    }

    return $process
}

function Complete-DistributedProcessCapture {
    param([Parameter(Mandatory)][System.Diagnostics.Process]$Process)

    if ($null -eq $Process.PSObject.Properties["QuarryCaptureCompleted"] -or $Process.QuarryCaptureCompleted) {
        return
    }
    if (-not $Process.HasExited) {
        throw "Cannot complete output capture before process $($Process.Id) exits."
    }

    Set-Content -LiteralPath $Process.QuarryStandardOutputPath -Encoding utf8 `
        -Value $Process.QuarryStandardOutputTask.GetAwaiter().GetResult()
    Set-Content -LiteralPath $Process.QuarryStandardErrorPath -Encoding utf8 `
        -Value $Process.QuarryStandardErrorTask.GetAwaiter().GetResult()
    $Process.QuarryCaptureCompleted = $true
}

function Get-DistributedProcessFailure {
    param(
        [Parameter(Mandatory)][System.Diagnostics.Process]$Process,
        [Parameter(Mandatory)][string]$Label
    )

    Complete-DistributedProcessCapture -Process $Process
    $detail = (Get-Content -LiteralPath $Process.QuarryStandardErrorPath -Raw).Trim()
    if ([string]::IsNullOrWhiteSpace($detail)) {
        return "$Label exited with code $($Process.ExitCode)."
    }
    return "$Label exited with code $($Process.ExitCode): $detail"
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

function Assert-ProcessTestCleanup {
    param(
        [Parameter(Mandatory)]
        [string]$TestName,

        [Parameter(Mandatory)]
        [string]$ComposeProject,

        [Parameter(Mandatory)]
        [string]$TemporaryDirectory,

        [Parameter(Mandatory)]
        [int[]]$ProcessIDs
    )

    if (Test-Path -LiteralPath $TemporaryDirectory) {
        throw "$TestName temporary directory still exists: $TemporaryDirectory"
    }
    foreach ($processID in $ProcessIDs) {
        if ($null -ne (Get-Process -Id $processID -ErrorAction SilentlyContinue)) {
            throw "$TestName process $processID is still running."
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
        throw "$TestName Compose resources remain after cleanup."
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

function ConvertTo-BenchmarkNanoseconds {
    param([Parameter(Mandatory)][TimeSpan]$Duration)

    return [long]($Duration.Ticks * 100)
}

function ConvertFrom-DockerSize {
    param([Parameter(Mandatory)][string]$Value)

    if ($Value -notmatch '^\s*([0-9]+(?:\.[0-9]+)?)\s*(B|kB|KB|KiB|MB|MiB|GB|GiB)\s*$') {
        throw "Unsupported Docker memory value '$Value'."
    }
    $number = [double]::Parse($Matches[1], [Globalization.CultureInfo]::InvariantCulture)
    $multiplier = switch ($Matches[2]) {
        "B" { 1 }
        { $_ -in @("kB", "KB") } { 1000 }
        "KiB" { 1024 }
        "MB" { 1000000 }
        "MiB" { 1048576 }
        "GB" { 1000000000 }
        "GiB" { 1073741824 }
    }
    return [uint64][math]::Round($number * $multiplier)
}

function Get-BenchmarkMachineMetadata {
    $cpuModel = $env:PROCESSOR_IDENTIFIER
    $memoryBytes = [uint64]0
    if ($IsWindows) {
        try {
            $cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
            $computer = Get-CimInstance Win32_ComputerSystem
            if (-not [string]::IsNullOrWhiteSpace($cpu.Name)) {
                $cpuModel = $cpu.Name.Trim()
            }
            $memoryBytes = [uint64]$computer.TotalPhysicalMemory
        }
        catch {
        }
    }
    elseif (Test-Path -LiteralPath "/proc/meminfo") {
        $memoryLine = Get-Content -LiteralPath "/proc/meminfo" | Where-Object { $_ -match '^MemTotal:' } | Select-Object -First 1
        if ($memoryLine -match '^MemTotal:\s+([0-9]+)\s+kB$') {
            $memoryBytes = [uint64]$Matches[1] * 1024
        }
    }
    if ([string]::IsNullOrWhiteSpace($cpuModel)) {
        $cpuModel = "unknown CPU"
    }
    if ($memoryBytes -eq 0) {
        $memoryBytes = [uint64][GC]::GetGCMemoryInfo().TotalAvailableMemoryBytes
    }
    return [ordered]@{
        os = [Runtime.InteropServices.RuntimeInformation]::OSDescription
        architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
        cpu_model = $cpuModel
        logical_cpu_count = [Environment]::ProcessorCount
        total_memory_bytes = $memoryBytes
    }
}

function Get-PrometheusProcessSample {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][int]$ProcessID,
        [Parameter(Mandatory)][string]$MetricsURL
    )

    $body = (Invoke-WebRequest -UseBasicParsing -TimeoutSec 10 -Uri $MetricsURL).Content
    $values = @{}
    foreach ($metric in @("process_cpu_seconds_total", "process_resident_memory_bytes")) {
        $match = [regex]::Match($body, "(?m)^$metric\s+([-+0-9.eE]+)\s*$")
        if (-not $match.Success) {
            throw "Metrics endpoint $MetricsURL omitted $metric."
        }
        $values[$metric] = [double]::Parse($match.Groups[1].Value, [Globalization.CultureInfo]::InvariantCulture)
    }
    return [ordered]@{
        name = $Name
        process_id = $ProcessID
        cpu_seconds = $values.process_cpu_seconds_total
        resident_memory_bytes = [uint64]$values.process_resident_memory_bytes
    }
}

function Write-BenchmarkResourceSample {
    param(
        [Parameter(Mandatory)][string]$RunID,
        [Parameter(Mandatory)][object[]]$ProcessMetrics,
        [Parameter(Mandatory)][string]$PostgresContainer,
        [Parameter(Mandatory)][string]$OutputPath,
        [switch]$AllowExitedProcesses
    )

    $processSamples = @()
    foreach ($metrics in $ProcessMetrics) {
        if ($AllowExitedProcesses -and $null -eq (Get-Process -Id $metrics.ProcessID -ErrorAction SilentlyContinue)) {
            continue
        }
        try {
            $processSamples += Get-PrometheusProcessSample `
                -Name $metrics.Name `
                -ProcessID $metrics.ProcessID `
                -MetricsURL $metrics.MetricsURL
        }
        catch {
            if ($AllowExitedProcesses -and $null -eq (Get-Process -Id $metrics.ProcessID -ErrorAction SilentlyContinue)) {
                continue
            }
            throw
        }
    }
    $statsLine = @(Invoke-Docker -Arguments @(
        "stats", $PostgresContainer, "--no-stream", "--format", "{{json .}}"
    )) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Last 1
    $stats = $statsLine | ConvertFrom-Json
    $cpuPercent = [double]::Parse(
        ([string]$stats.CPUPerc).Trim().TrimEnd('%'),
        [Globalization.CultureInfo]::InvariantCulture
    )
    $memoryValue = ([string]$stats.MemUsage).Split('/')[0].Trim()
    $connectionValue = @(
        Invoke-PostgresRows -Query "SELECT count(*) FROM pg_stat_activity WHERE datname = 'quarry';" |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ -match '^[0-9]+$' }
    ) | Select-Object -Last 1
    if ($null -eq $connectionValue) {
        throw "PostgreSQL connection sampling returned no count."
    }
    $sample = [ordered]@{
        schema_version = 1
        run_id = $RunID
        observed_at = [DateTime]::UtcNow.ToString("o")
        processes = @($processSamples)
        postgresql = [ordered]@{
            cpu_percent = $cpuPercent
            memory_bytes = ConvertFrom-DockerSize -Value $memoryValue
        }
        database_connections = [int]$connectionValue
    }
    Add-Content -LiteralPath $OutputPath -Encoding utf8 -Value ($sample | ConvertTo-Json -Depth 6 -Compress)
}

function Write-BenchmarkManifest {
    param(
        [Parameter(Mandatory)][System.Collections.IDictionary]$Manifest,
        [Parameter(Mandatory)][string]$Path
    )

    $Manifest.runs = @($Manifest.runs)
    Set-Content -LiteralPath $Path -Encoding utf8 -Value ($Manifest | ConvertTo-Json -Depth 8)
}

function New-BenchmarkRunRecord {
    param(
        [Parameter(Mandatory)][string]$RunID,
        [Parameter(Mandatory)][string]$Workload,
        [Parameter(Mandatory)][int]$WorkerProcesses,
        [Parameter(Mandatory)][int]$Repetition,
        [Parameter(Mandatory)][int]$MaxOutstanding,
        [Parameter(Mandatory)][TimeSpan]$Warmup,
        [Parameter(Mandatory)][TimeSpan]$Measurement,
        [Parameter(Mandatory)][TimeSpan]$Drain,
        [Parameter(Mandatory)][long]$Seed
    )

    $maxAttempts = if ($Workload -eq "c") { 3 } else { 1 }
    $jobTimeout = if ($Workload -eq "c") { [TimeSpan]::FromSeconds(30) } else { [TimeSpan]::FromSeconds(5) }
    return [ordered]@{
        run_id = $RunID
        directory = "runs/$RunID"
        repetition = $Repetition
        status = "invalid"
        failure_reason = "run has not completed"
        config = [ordered]@{
            workload = $Workload
            worker_processes = $WorkerProcesses
            worker_concurrency = 8
            max_outstanding = $MaxOutstanding
            http_concurrency = $MaxOutstanding
            warmup_duration = ConvertTo-BenchmarkNanoseconds -Duration $Warmup
            measurement_duration = ConvertTo-BenchmarkNanoseconds -Duration $Measurement
            drain_timeout = ConvertTo-BenchmarkNanoseconds -Duration $Drain
            poll_interval = ConvertTo-BenchmarkNanoseconds -Duration ([TimeSpan]::FromMilliseconds(10))
            seed = $Seed
            max_attempts = $maxAttempts
            job_timeout = ConvertTo-BenchmarkNanoseconds -Duration $jobTimeout
        }
    }
}

function Wait-BenchmarkRecoveryAttemptBatch {
    param(
        [Parameter(Mandatory)][string]$RunID,
        [Parameter(Mandatory)][string]$TargetWorkerID,
        [Parameter(Mandatory)][int]$ExpectedCount,
        [Parameter(Mandatory)][System.Diagnostics.Process]$LoadgenProcess
    )

    if ($RunID -notmatch '^[a-zA-Z0-9-]+$' -or $TargetWorkerID -notmatch '^[0-9a-f-]{36}$') {
        throw "Recovery benchmark received an invalid run or worker ID."
    }
    $query = @"
SELECT
    count(*) FILTER (WHERE job_attempts.worker_id = '$TargetWorkerID'::uuid),
    count(*) FILTER (WHERE job_attempts.worker_id <> '$TargetWorkerID'::uuid)
FROM jobs
JOIN job_attempts ON job_attempts.job_id = jobs.id
WHERE jobs.idempotency_key LIKE '$RunID-%'
  AND jobs.status = 'running'
  AND job_attempts.attempt_no = 1
  AND job_attempts.status = 'running';
"@
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($LoadgenProcess.HasExited) {
            throw (Get-DistributedProcessFailure `
                -Process $LoadgenProcess `
                -Label "Recovery load generator before the target worker owned the measured batch")
        }
        $row = @(
            Invoke-PostgresRows -Query $query |
                ForEach-Object { $_.Trim() } |
                Where-Object { $_ -match '^\d+\|\d+$' }
        ) | Select-Object -Last 1
        if ($null -ne $row) {
            $counts = $row.Split('|')
            if ([int]$counts[0] -eq $ExpectedCount -and [int]$counts[1] -eq 0) {
                return
            }
        }
        Start-Sleep -Milliseconds 100
    }
    throw "The target worker did not own $ExpectedCount running attempt-1 jobs within 30 seconds."
}

function Stop-BenchmarkTargetWorker {
    param([Parameter(Mandatory)][System.Diagnostics.Process]$Process)

    try {
        if ($Process.HasExited) {
            throw "Recovery benchmark target worker exited before termination with code $($Process.ExitCode)."
        }
        $Process.Kill($true)
        if (-not $Process.WaitForExit(10000)) {
            throw "Recovery benchmark target worker did not exit after forced termination."
        }
        return [DateTime]::UtcNow
    }
    finally {
        $Process.Dispose()
    }
}

function Invoke-BenchmarkConfiguration {
    param(
        [Parameter(Mandatory)][string]$WorkerBinary,
        [Parameter(Mandatory)][string]$LoadgenBinary,
        [Parameter(Mandatory)][string]$BenchmarkControllerBinary,
        [Parameter(Mandatory)][string]$BaseURL,
        [Parameter(Mandatory)][string]$DispatcherAddress,
        [Parameter(Mandatory)][System.Diagnostics.Process]$APIProcess,
        [Parameter(Mandatory)][System.Diagnostics.Process]$DispatcherProcess,
        [Parameter(Mandatory)][string]$DispatcherMetricsURL,
        [Parameter(Mandatory)][string]$PostgresContainer,
        [Parameter(Mandatory)][string]$CampaignRoot,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Manifest,
        [Parameter(Mandatory)][System.Collections.IDictionary]$RunRecord,
        [Parameter(Mandatory)][string]$HeartbeatInterval,
        [Parameter(Mandatory)][System.Collections.Generic.List[int]]$ProcessIDs
    )

    $manifestPath = Join-Path $CampaignRoot "manifest.json"
    $runDirectory = Join-Path $CampaignRoot ($RunRecord.directory -replace '/', [IO.Path]::DirectorySeparatorChar)
    $workerProcesses = @()
    $loadgenProcess = $null
    try {
        New-Item -ItemType Directory -Path $runDirectory | Out-Null
        $workerHostNames = @()
        $processMetrics = @(
            [pscustomobject]@{ Name = "api"; ProcessID = $APIProcess.Id; MetricsURL = "$BaseURL/metrics" },
            [pscustomobject]@{ Name = "dispatcher"; ProcessID = $DispatcherProcess.Id; MetricsURL = $DispatcherMetricsURL }
        )
        for ($index = 1; $index -le $RunRecord.config.worker_processes; $index++) {
            $metricsPort = Get-AvailableLoopbackPort
            $hostName = "$($RunRecord.run_id)-worker-$('{0:D2}' -f $index)"
            $worker = Start-DistributedProcess -Binary $WorkerBinary -Environment @{
                QUARRY_DISPATCHER_ADDR = $DispatcherAddress
                QUARRY_WORKER_CONCURRENCY = "8"
                QUARRY_WORKER_HOSTNAME = $hostName
                QUARRY_WORKER_VERSION = "benchmark"
                QUARRY_WORKER_METRICS_ADDR = "127.0.0.1:$metricsPort"
                QUARRY_HEARTBEAT_INTERVAL = $HeartbeatInterval
            }
            $workerProcesses += $worker
            $workerHostNames += $hostName
            $ProcessIDs.Add($worker.Id)
            $processMetrics += [pscustomobject]@{
                Name = "worker-$('{0:D2}' -f $index)"
                ProcessID = $worker.Id
                MetricsURL = "http://127.0.0.1:$metricsPort/metrics"
            }
        }
        $null = Wait-DistributedWorkers -HostNames $workerHostNames -Processes $workerProcesses

        $jobPath = Join-Path $runDirectory "jobs.jsonl.gz"
        $jobSummaryPath = Join-Path $runDirectory "job-summary.json"
        $resourcePath = Join-Path $runDirectory "resources.jsonl"
        $loadgenOutputPath = Join-Path $runDirectory "loadgen.stdout.log"
        $loadgenErrorPath = Join-Path $runDirectory "loadgen.stderr.log"
        $arguments = @(
            "-api-url", $BaseURL,
            "-output", $jobPath,
            "-summary", $jobSummaryPath,
            "-run-id", $RunRecord.run_id,
            "-workload", $RunRecord.config.workload,
            "-seed", [string]$RunRecord.config.seed,
            "-warmup", "$($RunRecord.config.warmup_duration)ns",
            "-measurement", "$($RunRecord.config.measurement_duration)ns",
            "-drain-timeout", "$($RunRecord.config.drain_timeout)ns",
            "-poll-interval", "$($RunRecord.config.poll_interval)ns",
            "-max-outstanding", [string]$RunRecord.config.max_outstanding,
            "-http-concurrency", [string]$RunRecord.config.http_concurrency,
            "-max-attempts", [string]$RunRecord.config.max_attempts,
            "-job-timeout", "$($RunRecord.config.job_timeout)ns"
        )
        $loadgenProcess = Start-DistributedProcess -Binary $LoadgenBinary -Environment @{} -Arguments $arguments `
            -StandardOutputPath $loadgenOutputPath -StandardErrorPath $loadgenErrorPath
        $ProcessIDs.Add($loadgenProcess.Id)
        do {
            Write-BenchmarkResourceSample `
                -RunID $RunRecord.run_id `
                -ProcessMetrics $processMetrics `
                -PostgresContainer $PostgresContainer `
                -OutputPath $resourcePath
        } while (-not $loadgenProcess.WaitForExit(100))
        Complete-DistributedProcessCapture -Process $loadgenProcess
        if ($loadgenProcess.ExitCode -ne 0) {
            throw (Get-DistributedProcessFailure -Process $loadgenProcess -Label "Load generator")
        }

        $RunRecord.status = "valid"
        $RunRecord.Remove("failure_reason")
        Write-BenchmarkManifest -Manifest $Manifest -Path $manifestPath
        & $BenchmarkControllerBinary "summarize-run" "-campaign-root" $CampaignRoot "-run-id" $RunRecord.run_id
        if ($LASTEXITCODE -ne 0) {
            throw "Benchmark summary regeneration failed with exit code $LASTEXITCODE."
        }
        $summary = Get-Content -LiteralPath (Join-Path $runDirectory "summary.json") -Raw | ConvertFrom-Json
        if ($summary.jobs.run_id -ne $RunRecord.run_id -or $summary.jobs.completed_count -le 0 -or
            $summary.resources.sample_count -lt 2 -or $summary.config.worker_processes -ne $RunRecord.config.worker_processes -or
            $summary.config.worker_concurrency -ne 8 -or $summary.config.max_outstanding -ne $RunRecord.config.max_outstanding) {
            throw "Benchmark run summary did not preserve the required configuration and measurements."
        }
        Write-Host "Benchmark run $($RunRecord.run_id) passed with $($summary.jobs.completed_count) measured completions and $($summary.resources.sample_count) resource samples."
    }
    catch {
        $RunRecord.status = "invalid"
        $RunRecord.failure_reason = $_.Exception.Message
        Write-BenchmarkManifest -Manifest $Manifest -Path $manifestPath
        throw
    }
    finally {
        $stopErrors = @()
        foreach ($process in @($loadgenProcess) + @($workerProcesses)) {
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
            throw "Failed to stop $($stopErrors.Count) benchmark configuration processes."
        }
    }
}

function Invoke-BenchmarkRecoveryConfiguration {
    param(
        [Parameter(Mandatory)][string]$WorkerBinary,
        [Parameter(Mandatory)][string]$LoadgenBinary,
        [Parameter(Mandatory)][string]$BenchmarkControllerBinary,
        [Parameter(Mandatory)][string]$BaseURL,
        [Parameter(Mandatory)][string]$DispatcherAddress,
        [Parameter(Mandatory)][System.Diagnostics.Process]$APIProcess,
        [Parameter(Mandatory)][System.Diagnostics.Process]$DispatcherProcess,
        [Parameter(Mandatory)][string]$DispatcherMetricsURL,
        [Parameter(Mandatory)][string]$PostgresContainer,
        [Parameter(Mandatory)][string]$CampaignRoot,
        [Parameter(Mandatory)][System.Collections.IDictionary]$Manifest,
        [Parameter(Mandatory)][System.Collections.IDictionary]$RunRecord,
        [Parameter(Mandatory)][string]$HeartbeatInterval,
        [Parameter(Mandatory)][System.Collections.Generic.List[int]]$ProcessIDs
    )

    $manifestPath = Join-Path $CampaignRoot "manifest.json"
    $runDirectory = Join-Path $CampaignRoot ($RunRecord.directory -replace '/', [IO.Path]::DirectorySeparatorChar)
    $targetWorkerProcess = $null
    $replacementWorkerProcess = $null
    $loadgenProcess = $null
    try {
        New-Item -ItemType Directory -Path $runDirectory | Out-Null
        $targetMetricsPort = Get-AvailableLoopbackPort
        $targetHostName = "$($RunRecord.run_id)-target"
        $targetWorkerProcess = Start-DistributedProcess -Binary $WorkerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $DispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "8"
            QUARRY_WORKER_HOSTNAME = $targetHostName
            QUARRY_WORKER_VERSION = "benchmark-recovery-target"
            QUARRY_WORKER_METRICS_ADDR = "127.0.0.1:$targetMetricsPort"
            QUARRY_HEARTBEAT_INTERVAL = $HeartbeatInterval
        }
        $ProcessIDs.Add($targetWorkerProcess.Id)
        $targetWorkerID = @(Wait-DistributedWorkers -HostNames @($targetHostName) -Processes @($targetWorkerProcess))[0]

        $jobPath = Join-Path $runDirectory "jobs.jsonl.gz"
        $jobSummaryPath = Join-Path $runDirectory "job-summary.json"
        $resourcePath = Join-Path $runDirectory "resources.jsonl"
        $recoveryEventPath = Join-Path $runDirectory "recovery-event.json"
        $loadgenOutputPath = Join-Path $runDirectory "loadgen.stdout.log"
        $loadgenErrorPath = Join-Path $runDirectory "loadgen.stderr.log"
        $arguments = @(
            "-api-url", $BaseURL,
            "-output", $jobPath,
            "-summary", $jobSummaryPath,
            "-recovery-event", $recoveryEventPath,
            "-run-id", $RunRecord.run_id,
            "-workload", "c",
            "-seed", [string]$RunRecord.config.seed,
            "-warmup", "$($RunRecord.config.warmup_duration)ns",
            "-measurement", "$($RunRecord.config.measurement_duration)ns",
            "-drain-timeout", "$($RunRecord.config.drain_timeout)ns",
            "-poll-interval", "$($RunRecord.config.poll_interval)ns",
            "-max-outstanding", [string]$RunRecord.config.max_outstanding,
            "-http-concurrency", [string]$RunRecord.config.http_concurrency,
            "-max-attempts", [string]$RunRecord.config.max_attempts,
            "-job-timeout", "$($RunRecord.config.job_timeout)ns"
        )
        $loadgenProcess = Start-DistributedProcess -Binary $LoadgenBinary -Environment @{} -Arguments $arguments `
            -StandardOutputPath $loadgenOutputPath -StandardErrorPath $loadgenErrorPath
        $ProcessIDs.Add($loadgenProcess.Id)

        if ($RunRecord.config.warmup_duration -gt 0) {
            $warmupMilliseconds = [math]::Ceiling([double]$RunRecord.config.warmup_duration / 1000000) + 6500
            if ($loadgenProcess.WaitForExit([int]$warmupMilliseconds)) {
                throw (Get-DistributedProcessFailure -Process $loadgenProcess -Label "Recovery load generator during warmup")
            }
        }
        Wait-BenchmarkRecoveryAttemptBatch `
            -RunID $RunRecord.run_id `
            -TargetWorkerID $targetWorkerID `
            -ExpectedCount $RunRecord.config.max_outstanding `
            -LoadgenProcess $loadgenProcess

        $replacementMetricsPort = Get-AvailableLoopbackPort
        $replacementHostName = "$($RunRecord.run_id)-replacement"
        $replacementWorkerProcess = Start-DistributedProcess -Binary $WorkerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $DispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "8"
            QUARRY_WORKER_HOSTNAME = $replacementHostName
            QUARRY_WORKER_VERSION = "benchmark-recovery-replacement"
            QUARRY_WORKER_METRICS_ADDR = "127.0.0.1:$replacementMetricsPort"
            QUARRY_HEARTBEAT_INTERVAL = $HeartbeatInterval
        }
        $ProcessIDs.Add($replacementWorkerProcess.Id)
        $replacementWorkerID = @(Wait-DistributedWorkers -HostNames @($replacementHostName) -Processes @($replacementWorkerProcess))[0]
        if ($replacementWorkerID -eq $targetWorkerID) {
            throw "Recovery benchmark replacement worker reused the target worker ID."
        }

        $processMetrics = @(
            [pscustomobject]@{ Name = "api"; ProcessID = $APIProcess.Id; MetricsURL = "$BaseURL/metrics" },
            [pscustomobject]@{ Name = "dispatcher"; ProcessID = $DispatcherProcess.Id; MetricsURL = $DispatcherMetricsURL },
            [pscustomobject]@{ Name = "worker-target"; ProcessID = $targetWorkerProcess.Id; MetricsURL = "http://127.0.0.1:$targetMetricsPort/metrics" },
            [pscustomobject]@{ Name = "worker-replacement"; ProcessID = $replacementWorkerProcess.Id; MetricsURL = "http://127.0.0.1:$replacementMetricsPort/metrics" }
        )
        Write-BenchmarkResourceSample `
            -RunID $RunRecord.run_id `
            -ProcessMetrics $processMetrics `
            -PostgresContainer $PostgresContainer `
            -OutputPath $resourcePath

        $workerTerminatedAt = Stop-BenchmarkTargetWorker -Process $targetWorkerProcess
        $targetWorkerProcess = $null
        $recoveryEvent = [ordered]@{
            killed_worker_id = $targetWorkerID
            worker_terminated_at = $workerTerminatedAt.ToString("o")
        }
        Set-Content -LiteralPath $recoveryEventPath -Encoding utf8 -Value ($recoveryEvent | ConvertTo-Json)

        do {
            Write-BenchmarkResourceSample `
                -RunID $RunRecord.run_id `
                -ProcessMetrics $processMetrics `
                -PostgresContainer $PostgresContainer `
                -OutputPath $resourcePath `
                -AllowExitedProcesses
        } while (-not $loadgenProcess.WaitForExit(100))
        Complete-DistributedProcessCapture -Process $loadgenProcess
        if ($loadgenProcess.ExitCode -ne 0) {
            throw (Get-DistributedProcessFailure -Process $loadgenProcess -Label "Recovery load generator")
        }

        $RunRecord.status = "valid"
        $RunRecord.Remove("failure_reason")
        Write-BenchmarkManifest -Manifest $Manifest -Path $manifestPath
        & $BenchmarkControllerBinary "summarize-run" "-campaign-root" $CampaignRoot "-run-id" $RunRecord.run_id
        if ($LASTEXITCODE -ne 0) {
            throw "Recovery benchmark summary regeneration failed with exit code $LASTEXITCODE."
        }
        $summary = Get-Content -LiteralPath (Join-Path $runDirectory "summary.json") -Raw | ConvertFrom-Json
        if ($summary.recovery.run_id -ne $RunRecord.run_id -or
            $summary.recovery.sample_count -ne $RunRecord.config.max_outstanding -or
            $summary.recovery.killed_worker_id -ne $targetWorkerID -or
            @($summary.recovery.replacement_worker_ids).Count -ne 1 -or
            $summary.recovery.replacement_worker_ids[0] -ne $replacementWorkerID -or
            $summary.recovery.kill_to_replacement_start.p50 -le 0 -or
            $summary.recovery.kill_to_success.p50 -le 0 -or
            $summary.resources.sample_count -lt 2 -or
            $summary.config.worker_processes -ne 2 -or $summary.config.worker_concurrency -ne 8) {
            throw "Recovery benchmark summary did not preserve the required process and attempt evidence."
        }
        Write-Host "Recovery benchmark run $($RunRecord.run_id) passed with $($summary.recovery.sample_count) affected jobs."
    }
    catch {
        $RunRecord.status = "invalid"
        $RunRecord.failure_reason = $_.Exception.Message
        Write-BenchmarkManifest -Manifest $Manifest -Path $manifestPath
        throw
    }
    finally {
        $stopErrors = @()
        foreach ($process in @($loadgenProcess, $replacementWorkerProcess, $targetWorkerProcess)) {
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
            throw "Failed to stop $($stopErrors.Count) recovery benchmark processes."
        }
    }
}

function Test-BenchmarkFailedConfigurationCleanup {
    param(
        [Parameter(Mandatory)][string]$WorkerBinary,
        [Parameter(Mandatory)][string]$DispatcherAddress,
        [Parameter(Mandatory)][System.Collections.IDictionary]$RunRecord,
        [Parameter(Mandatory)][string]$CampaignRoot,
        [Parameter(Mandatory)][System.Collections.Generic.List[int]]$ProcessIDs
    )

    $runDirectory = Join-Path $CampaignRoot ($RunRecord.directory -replace '/', [IO.Path]::DirectorySeparatorChar)
    New-Item -ItemType Directory -Path $runDirectory | Out-Null
    $workerProcess = $null
    $workerProcessID = 0
    $failureObserved = $false
    try {
        $metricsPort = Get-AvailableLoopbackPort
        $hostName = "$($RunRecord.run_id)-worker"
        $workerProcess = Start-DistributedProcess -Binary $WorkerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $DispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "8"
            QUARRY_WORKER_HOSTNAME = $hostName
            QUARRY_WORKER_VERSION = "benchmark-cleanup-probe"
            QUARRY_WORKER_METRICS_ADDR = "127.0.0.1:$metricsPort"
        }
        $ProcessIDs.Add($workerProcess.Id)
        $workerProcessID = $workerProcess.Id
        $null = Wait-DistributedWorkers -HostNames @($hostName) -Processes @($workerProcess)
        throw "intentional failed-configuration cleanup probe"
    }
    catch {
        if ($_.Exception.Message -ne "intentional failed-configuration cleanup probe") {
            throw
        }
        $failureObserved = $true
        $RunRecord.failure_reason = $_.Exception.Message
    }
    finally {
        if ($null -ne $workerProcess) {
            Stop-DistributedProcess -Process $workerProcess
        }
    }
    if (-not $failureObserved -or $null -ne (Get-Process -Id $workerProcessID -ErrorAction SilentlyContinue)) {
        throw "Failed benchmark configuration did not clean up its worker process."
    }
}

function Invoke-BenchmarkCampaign {
    param([switch]$Smoke)

    $testID = [Guid]::NewGuid().ToString("N")
    $campaignID = if ($Smoke) { "smoke-$testID" } else { "quarry-$([DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ'))" }
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-benchmark-$testID"
    $campaignRoot = if ($Smoke) {
        Join-Path $temporaryDirectory "campaign"
    }
    else {
        Join-Path $repositoryRoot "benchmarks/results/$campaignID"
    }
    if (-not $Smoke -and @(git status --porcelain --untracked-files=all).Count -ne 0) {
        throw "Publishable benchmark requires a clean Git worktree."
    }
    $initialGitCommit = (& git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "git rev-parse HEAD failed."
    }
    $initialGitState = if (@(git status --porcelain --untracked-files=all).Count -eq 0) { "clean" } else { "dirty" }

    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $apiBinary = Join-Path $temporaryDirectory "quarry-api$binaryExtension"
    $dispatcherBinary = Join-Path $temporaryDirectory "quarry-dispatcher$binaryExtension"
    $workerBinary = Join-Path $temporaryDirectory "quarry-worker$binaryExtension"
    $loadgenBinary = Join-Path $temporaryDirectory "quarry-loadgen$binaryExtension"
    $benchmarkControllerBinary = Join-Path $temporaryDirectory "quarry-benchmarkctl$binaryExtension"
    $previousComposeProject = $env:COMPOSE_PROJECT_NAME
    $previousPostgresPort = $env:QUARRY_POSTGRES_PORT
    $composeProject = "quarry-m6-benchmark-$testID"
    $apiProcess = $null
    $dispatcherProcess = $null
    $processIDs = [System.Collections.Generic.List[int]]::new()
    $env:COMPOSE_PROJECT_NAME = $composeProject
    $env:QUARRY_POSTGRES_PORT = [string](Get-AvailableLoopbackPort)
    $databaseURL = Get-PostgresConnectionString

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        New-Item -ItemType Directory -Path (Join-Path $campaignRoot "runs") -Force | Out-Null
        Invoke-Go -Arguments @("build", "-o", $apiBinary, "./cmd/api")
        Invoke-Go -Arguments @("build", "-o", $dispatcherBinary, "./cmd/dispatcher")
        Invoke-Go -Arguments @("build", "-o", $workerBinary, "./cmd/worker")
        Invoke-Go -Arguments @("build", "-o", $loadgenBinary, "./cmd/loadgen")
        Invoke-Go -Arguments @("build", "-o", $benchmarkControllerBinary, "./cmd/benchmarkctl")
        Invoke-Docker -Arguments @("compose", "up", "--detach", "--wait", "postgres")
        Invoke-Goose -MigrationCommand "up"
        $postgresContainer = @(Invoke-Docker -Arguments @("compose", "ps", "--quiet", "postgres"))[0].Trim()

        $httpPort = Get-AvailableLoopbackPort
        $dispatcherPort = Get-AvailableLoopbackPort
        $dispatcherMetricsPort = Get-AvailableLoopbackPort
        $httpAddress = "127.0.0.1:$httpPort"
        $dispatcherAddress = "127.0.0.1:$dispatcherPort"
        $baseURL = "http://$httpAddress"
        $leaseDuration = if ($Smoke) { [TimeSpan]::FromSeconds(2) } else { [TimeSpan]::FromSeconds(20) }
        $reaperInterval = if ($Smoke) { [TimeSpan]::FromMilliseconds(200) } else { [TimeSpan]::FromSeconds(1) }
        $heartbeatInterval = if ($Smoke) { [TimeSpan]::FromMilliseconds(250) } else { [TimeSpan]::FromSeconds(5) }
        $apiProcess = Start-DistributedProcess -Binary $apiBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_HTTP_ADDR = $httpAddress
        }
        $processIDs.Add($apiProcess.Id)
        Wait-ApiReady -Process $apiProcess -BaseURL $baseURL
        $dispatcherProcess = Start-DistributedProcess -Binary $dispatcherBinary -Environment @{
            QUARRY_DATABASE_URL = $databaseURL
            QUARRY_DISPATCHER_ADDR = $dispatcherAddress
            QUARRY_DISPATCHER_METRICS_ADDR = "127.0.0.1:$dispatcherMetricsPort"
            QUARRY_LEASE_DURATION = "$($leaseDuration.TotalMilliseconds)ms"
            QUARRY_REAPER_INTERVAL = "$($reaperInterval.TotalMilliseconds)ms"
            QUARRY_REAPER_BATCH_SIZE = "100"
        }
        $processIDs.Add($dispatcherProcess.Id)
        Wait-TcpReady -Process $dispatcherProcess -HostName "127.0.0.1" -Port $dispatcherPort -ProcessName "Dispatcher"

        $goVersion = (& $script:GoExecutable version).Trim()
        $dockerVersion = @(Invoke-Docker -Arguments @("version", "--format", "{{.Client.Version}}/{{.Server.Version}}"))[0].Trim()
        $postgresImage = @(Invoke-Docker -Arguments @("inspect", "--format", "{{.Config.Image}}", $postgresContainer))[0].Trim()
        $warmup = if ($Smoke) { [TimeSpan]::FromMilliseconds(750) } else { [TimeSpan]::FromSeconds(30) }
        $measurement = if ($Smoke) { [TimeSpan]::FromSeconds(6) } else { [TimeSpan]::FromSeconds(120) }
        $recoveryMeasurement = if ($Smoke) { [TimeSpan]::FromSeconds(20) } else { $measurement }
        $drain = if ($Smoke) { [TimeSpan]::FromSeconds(8) } else { [TimeSpan]::FromSeconds(30) }
        $recoveryWarmup = if ($Smoke) { [TimeSpan]::Zero } else { [TimeSpan]::FromSeconds(30) }
        $recoveryDrain = if ($Smoke) { [TimeSpan]::FromSeconds(15) } else { [TimeSpan]::FromSeconds(30) }
        $maxOutstanding = 8
        $workerCounts = if ($Smoke) { @(1, 2) } else { @(1, 2, 4, 8) }
        $repetitions = if ($Smoke) { @(1) } else { @(1, 2, 3) }
        $runs = [System.Collections.Generic.List[object]]::new()
        foreach ($workload in @("a", "b")) {
            foreach ($workerCount in $workerCounts) {
                foreach ($repetition in $repetitions) {
                    $runID = "$campaignID-$workload-w$workerCount-r$repetition"
                    $runs.Add((New-BenchmarkRunRecord `
                        -RunID $runID `
                        -Workload $workload `
                        -WorkerProcesses $workerCount `
                        -Repetition $repetition `
                        -MaxOutstanding $maxOutstanding `
                        -Warmup $warmup `
                        -Measurement $measurement `
                        -Drain $drain `
                        -Seed 20260827))
                }
            }
        }
        foreach ($repetition in $repetitions) {
            $runID = "$campaignID-c-w2-r$repetition"
            $runs.Add((New-BenchmarkRunRecord `
                -RunID $runID `
                -Workload "c" `
                -WorkerProcesses 2 `
                -Repetition $repetition `
                -MaxOutstanding $maxOutstanding `
                -Warmup $recoveryWarmup `
                -Measurement $recoveryMeasurement `
                -Drain $recoveryDrain `
                -Seed 20260827))
        }
        if ($Smoke) {
            $runs.Add((New-BenchmarkRunRecord `
                -RunID "$campaignID-cleanup-probe" `
                -Workload "a" `
                -WorkerProcesses 1 `
                -Repetition 2 `
                -MaxOutstanding $maxOutstanding `
                -Warmup $warmup `
                -Measurement $measurement `
                -Drain $drain `
                -Seed 20260827))
        }
        $manifest = [ordered]@{
            schema_version = 1
            campaign_id = $campaignID
            publishable = -not $Smoke
            created_at = [DateTime]::UtcNow.ToString("o")
            git = [ordered]@{ commit = $initialGitCommit; worktree_state = $initialGitState }
            machine = Get-BenchmarkMachineMetadata
            software = [ordered]@{
                go_version = $goVersion
                docker_version = $dockerVersion
                postgres_image = $postgresImage
            }
            quarry = [ordered]@{
                lease_duration = ConvertTo-BenchmarkNanoseconds -Duration $leaseDuration
                reaper_interval = ConvertTo-BenchmarkNanoseconds -Duration $reaperInterval
                reaper_batch_size = 100
                worker_heartbeat_interval = ConvertTo-BenchmarkNanoseconds -Duration $heartbeatInterval
            }
            runs = $runs
        }
        $manifestPath = Join-Path $campaignRoot "manifest.json"
        Write-BenchmarkManifest -Manifest $manifest -Path $manifestPath

        foreach ($runRecord in @($runs)) {
            if ($Smoke -and $runRecord.run_id.EndsWith("cleanup-probe")) {
                Test-BenchmarkFailedConfigurationCleanup `
                    -WorkerBinary $workerBinary `
                    -DispatcherAddress $dispatcherAddress `
                    -RunRecord $runRecord `
                    -CampaignRoot $campaignRoot `
                    -ProcessIDs $processIDs
                Write-BenchmarkManifest -Manifest $manifest -Path $manifestPath
                continue
            }
            if ($runRecord.config.workload -eq "c") {
                Invoke-BenchmarkRecoveryConfiguration `
                    -WorkerBinary $workerBinary `
                    -LoadgenBinary $loadgenBinary `
                    -BenchmarkControllerBinary $benchmarkControllerBinary `
                    -BaseURL $baseURL `
                    -DispatcherAddress $dispatcherAddress `
                    -APIProcess $apiProcess `
                    -DispatcherProcess $dispatcherProcess `
                    -DispatcherMetricsURL "http://127.0.0.1:$dispatcherMetricsPort/metrics" `
                    -PostgresContainer $postgresContainer `
                    -CampaignRoot $campaignRoot `
                    -Manifest $manifest `
                    -RunRecord $runRecord `
                    -HeartbeatInterval "$($heartbeatInterval.TotalMilliseconds)ms" `
                    -ProcessIDs $processIDs
            }
            else {
                Invoke-BenchmarkConfiguration `
                    -WorkerBinary $workerBinary `
                    -LoadgenBinary $loadgenBinary `
                    -BenchmarkControllerBinary $benchmarkControllerBinary `
                    -BaseURL $baseURL `
                    -DispatcherAddress $dispatcherAddress `
                    -APIProcess $apiProcess `
                    -DispatcherProcess $dispatcherProcess `
                    -DispatcherMetricsURL "http://127.0.0.1:$dispatcherMetricsPort/metrics" `
                    -PostgresContainer $postgresContainer `
                    -CampaignRoot $campaignRoot `
                    -Manifest $manifest `
                    -RunRecord $runRecord `
                    -HeartbeatInterval "$($heartbeatInterval.TotalMilliseconds)ms" `
                    -ProcessIDs $processIDs
            }
        }
        & $benchmarkControllerBinary "verify-runs" "-campaign-root" $campaignRoot
        if ($LASTEXITCODE -ne 0) {
            throw "Benchmark run verification failed with exit code $LASTEXITCODE."
        }
        if (-not $Smoke) {
            & $benchmarkControllerBinary "summarize-campaign" "-campaign-root" $campaignRoot
            if ($LASTEXITCODE -ne 0) {
                throw "Benchmark campaign aggregation failed with exit code $LASTEXITCODE."
            }
            & $benchmarkControllerBinary "verify" "-campaign-root" $campaignRoot
            if ($LASTEXITCODE -ne 0) {
                throw "Benchmark campaign verification failed with exit code $LASTEXITCODE."
            }
        }
    }
    finally {
        try {
            $stopErrors = @()
            foreach ($process in @($dispatcherProcess, $apiProcess)) {
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
                throw "Failed to stop $($stopErrors.Count) benchmark service processes."
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

    Assert-ProcessTestCleanup `
        -TestName $(if ($Smoke) { "Benchmark-smoke" } else { "Benchmark" }) `
        -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory `
        -ProcessIDs $processIDs.ToArray()
    if ($Smoke) {
        Write-Host "Benchmark smoke passed for Workloads A and B at 1 and 2 workers and Workload C with two workers. Temporary output was removed."
    }
    else {
        Write-Host "Benchmark campaign $campaignID passed and remains at $campaignRoot."
    }
}

function Test-BenchmarkSmoke {
    Invoke-BenchmarkCampaign -Smoke
}

function Test-Benchmark {
    Invoke-BenchmarkCampaign
}

function Test-BenchmarkVerification {
    Invoke-Go -Arguments @(
        "test", "-count=1", "-run", "^(TestBenchmarkController.*|TestAggregateCampaignUsesRecoveryMedians|TestSummarizeRecoveryRunFromRawSamples)$",
        "./cmd/benchmarkctl", "./internal/loadgen"
    )
    $resultsRoot = Join-Path $repositoryRoot "benchmarks/results"
    if (Test-Path -LiteralPath $resultsRoot) {
        foreach ($manifestPath in @(Get-ChildItem -LiteralPath $resultsRoot -Filter "manifest.json" -File -Recurse)) {
            Invoke-Go -Arguments @(
                "run", "./cmd/benchmarkctl", "verify", "-campaign-root", $manifestPath.Directory.FullName
            )
        }
    }
    Write-Host "Benchmark verification passed against deterministic campaign fixtures and committed results."
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
    $sleepDurationMilliseconds = 6000
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

        $gracefulJobID = Submit-SemanticsJob `
            -BaseURL $baseURL `
            -Type "demo.sleep" `
            -Payload @{ duration_ms = $sleepDurationMilliseconds }
        $gracefulHost = "semantics-graceful-$testID"
        Start-SemanticsWorker -ContainerName $gracefulContainer -TemporaryDirectory $temporaryDirectory `
            -DispatcherAddress $containerDispatcherAddress -HostName $gracefulHost -ShutdownTimeout "8s"
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

        $forcedJobID = Submit-SemanticsJob `
            -BaseURL $baseURL `
            -Type "demo.sleep" `
            -Payload @{ duration_ms = $sleepDurationMilliseconds }
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
            -DispatcherAddress $containerDispatcherAddress -HostName $replacementHost -ShutdownTimeout "8s"
        $replacementWorkerID = Wait-SemanticsWorker -HostName $replacementHost -ContainerName $replacementContainer
        $finalState = Wait-SemanticsJobStatus -BaseURL $baseURL -JobID $forcedJobID -ExpectedStatus "succeeded" -TimeoutSeconds 30
        if ($finalState.attempt_count -ne 2 -or
            $finalState.result.slept_ms -ne $sleepDurationMilliseconds) {
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

    Assert-ProcessTestCleanup -TestName "Semantics-test" -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory -ProcessIDs $processIDs.ToArray()
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

    Assert-ProcessTestCleanup `
        -TestName "Recovery-test" `
        -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory `
        -ProcessIDs $processIDs.ToArray()
    Write-Host "Recovery-test cleanup verified: processes, temporary binaries, containers, network, and volume removed."
}

function Test-StaleCompletion {
    Invoke-Go -Arguments @(
        "test", "-count=1",
        "-run", "^TestStaleAttemptReportAfterRecoveryThroughGRPCAndPostgres$",
        "./internal/dispatcher"
    )
}

function Test-Recovery {
    Test-RecoveryProcesses
    Test-StaleCompletion
}

function Wait-AcknowledgementLossFirstExecution {
    param(
        [Parameter(Mandatory)]
        [string]$MarkerPath,

        [Parameter(Mandatory)]
        [System.Diagnostics.Process]$WorkerProcess
    )

    $expectedMarker = "completed`n"
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (Test-Path -LiteralPath $MarkerPath) {
            $contents = [System.IO.File]::ReadAllText($MarkerPath)
            if ($contents -eq $expectedMarker) {
                if (-not $WorkerProcess.WaitForExit(10000)) {
                    throw "Fault-enabled worker did not exit after its handler returned success."
                }
                if ($WorkerProcess.ExitCode -eq 0) {
                    throw "Fault-enabled worker exited successfully instead of reporting the injected failure."
                }
                return
            }
            if ($contents.Length -gt $expectedMarker.Length -or
                (-not $expectedMarker.StartsWith($contents, [StringComparison]::Ordinal))) {
                throw "Fault-enabled worker wrote unexpected marker content '$contents'."
            }
        }
        elseif ($WorkerProcess.HasExited) {
            throw "Fault-enabled worker exited with code $($WorkerProcess.ExitCode) before writing its marker."
        }
        Start-Sleep -Milliseconds 50
    }

    throw "Fault-enabled worker did not write one marker and exit within 30 seconds."
}

function Get-AcknowledgementLossAttemptOneState {
    param(
        [Parameter(Mandatory)]
        [string]$JobID
    )

    if ($JobID -notmatch '^[0-9a-f-]{36}$') {
        throw "Acknowledgement-loss job has invalid ID '$JobID'."
    }
    $query = @"
SELECT
    jobs.status,
    jobs.attempt_count,
    jobs.current_worker_id::text,
    jobs.lease_expires_at::text,
    job_attempts.status,
    COALESCE(job_attempts.error_code, ''),
    job_attempts.finished_at IS NULL,
    workers.last_seen_at::text
FROM jobs
JOIN job_attempts ON job_attempts.job_id = jobs.id AND job_attempts.attempt_no = 1
JOIN workers ON workers.id = job_attempts.worker_id
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
        throw "PostgreSQL returned $($rows.Count) active acknowledgement-loss rows, expected one."
    }
    $columns = $rows[0].Split('|')
    if ($columns.Count -ne 8) {
        throw "PostgreSQL returned an unexpected acknowledgement-loss row: '$($rows[0])'."
    }
    return [PSCustomObject]@{
        JobStatus = $columns[0]
        AttemptCount = $columns[1]
        WorkerID = $columns[2]
        LeaseExpiresAt = $columns[3]
        AttemptStatus = $columns[4]
        ErrorCode = $columns[5]
        AttemptUnfinished = $columns[6]
        WorkerLastSeenAt = $columns[7]
    }
}

function Assert-AcknowledgementLossAttemptOneUnreported {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$WorkerID
    )

    $job = Invoke-RestMethod -Method Get -Uri "$BaseURL/v1/jobs/$JobID" -TimeoutSec 10
    if ($job.status -ne 'running' -or $job.attempt_count -ne 1 -or $null -ne $job.result) {
        throw "The public API did not keep the unreported job on running attempt 1."
    }
    $attemptResponse = Invoke-RestMethod `
        -Method Get `
        -Uri "$BaseURL/v1/jobs/$JobID/attempts" `
        -TimeoutSec 10
    $attempts = @($attemptResponse.attempts)
    if ($attempts.Count -ne 1) {
        throw "Acknowledgement-loss job has $($attempts.Count) HTTP attempts before recovery, expected one."
    }
    $attempt = $attempts[0]
    if ($attempt.attempt_no -ne 1 -or $attempt.worker_id -ne $WorkerID -or
        $attempt.status -ne 'running' -or $null -ne $attempt.finished_at -or
        $null -ne $attempt.error_code) {
        throw "The public API did not expose attempt 1 as running and unreported on the fault-enabled worker."
    }

    $state = Get-AcknowledgementLossAttemptOneState -JobID $JobID
    if ($null -eq $state -or $state.JobStatus -ne 'running' -or
        $state.AttemptCount -ne '1' -or $state.WorkerID -ne $WorkerID -or
        $state.AttemptStatus -ne 'running' -or $state.ErrorCode -ne '' -or
        $state.AttemptUnfinished -ne 't') {
        throw "PostgreSQL did not keep attempt 1 running and unreported on the fault-enabled worker."
    }
    return $state
}

function Wait-AcknowledgementLossJobSucceeded {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [System.Diagnostics.Process[]]$Processes
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        foreach ($process in $Processes) {
            if ($process.HasExited) {
                throw "Acknowledgement-loss process exited early with code $($process.ExitCode)."
            }
        }
        $state = Invoke-RestMethod -Method Get -Uri "$BaseURL/v1/jobs/$JobID" -TimeoutSec 10
        if ($state.status -eq 'succeeded') {
            return $state
        }
        if ($state.status -notin @('running', 'retry_wait')) {
            throw "Acknowledgement-loss job $JobID reached unexpected status '$($state.status)'."
        }
        Start-Sleep -Milliseconds 100
    }

    throw "Acknowledgement-loss job $JobID did not succeed within 30 seconds."
}

function Assert-AcknowledgementLossState {
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
        [string]$MarkerPath,

        [Parameter(Mandatory)]
        [object]$JobState
    )

    if ($JobState.attempt_count -ne 2 -or $JobState.result.marker -ne 'written') {
        throw "Acknowledgement-loss job did not return the expected attempt count and result."
    }
    $attemptResponse = Invoke-RestMethod `
        -Method Get `
        -Uri "$BaseURL/v1/jobs/$JobID/attempts" `
        -TimeoutSec 10
    $attempts = @($attemptResponse.attempts)
    if ($attempts.Count -ne 2) {
        throw "Acknowledgement-loss job has $($attempts.Count) HTTP attempts, expected two."
    }
    if ($attempts[0].attempt_no -ne 1 -or $attempts[0].worker_id -ne $FirstWorkerID -or
        $attempts[0].status -ne 'abandoned' -or $attempts[0].error_code -ne 'lease_expired' -or
        [string]::IsNullOrWhiteSpace($attempts[0].finished_at)) {
        throw "HTTP did not return attempt 1 as lease-expired and abandoned by the fault-enabled worker."
    }
    if ($attempts[1].attempt_no -ne 2 -or $attempts[1].worker_id -ne $SecondWorkerID -or
        $attempts[1].status -ne 'succeeded' -or $null -ne $attempts[1].error_code -or
        [string]::IsNullOrWhiteSpace($attempts[1].finished_at)) {
        throw "HTTP did not return attempt 2 as succeeded by the replacement worker."
    }

    $markerContents = [System.IO.File]::ReadAllText($MarkerPath)
    if ($markerContents -ne "completed`ncompleted`n") {
        throw "Acknowledgement-loss marker contains unexpected executions: '$markerContents'."
    }

    $query = @"
SELECT
    jobs.status,
    jobs.attempt_count,
    jobs.current_worker_id IS NULL,
    jobs.lease_expires_at IS NULL,
    jobs.result = '{"marker":"written"}'::jsonb,
    jobs.finished_at IS NOT NULL,
    job_attempts.attempt_no,
    job_attempts.worker_id::text,
    job_attempts.status,
    COALESCE(job_attempts.error_code, ''),
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
        throw "PostgreSQL returned $($rows.Count) acknowledgement-loss rows, expected two."
    }
    for ($index = 0; $index -lt 2; $index++) {
        $columns = $rows[$index].Split('|')
        if ($columns.Count -ne 14) {
            throw "PostgreSQL returned an unexpected acknowledgement-loss row: '$($rows[$index])'."
        }
        $attemptNumber = [string]($index + 1)
        $expectedWorkerID = if ($index -eq 0) { $FirstWorkerID } else { $SecondWorkerID }
        $expectedAttemptStatus = if ($index -eq 0) { 'abandoned' } else { 'succeeded' }
        $expectedErrorCode = if ($index -eq 0) { 'lease_expired' } else { '' }
        if ($columns[0] -ne 'succeeded' -or $columns[1] -ne '2' -or
            $columns[2] -ne 't' -or $columns[3] -ne 't' -or $columns[4] -ne 't' -or
            $columns[5] -ne 't' -or $columns[6] -ne $attemptNumber -or
            $columns[7] -ne $expectedWorkerID -or $columns[8] -ne $expectedAttemptStatus -or
            $columns[9] -ne $expectedErrorCode -or $columns[10] -ne 't' -or
            $columns[11] -ne 't' -or $columns[12] -ne 'lost' -or $columns[13] -ne 'active') {
            throw "PostgreSQL stored invalid acknowledgement-loss state: '$($rows[$index])'."
        }
    }
}

function Test-AcknowledgementLossProcesses {
    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-ack-loss-$testID"
    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $apiBinary = Join-Path $temporaryDirectory "quarry-api$binaryExtension"
    $dispatcherBinary = Join-Path $temporaryDirectory "quarry-dispatcher$binaryExtension"
    $workerBinary = Join-Path $temporaryDirectory "quarry-worker$binaryExtension"
    $markerPath = Join-Path $temporaryDirectory "side-effects.log"
    $composeProject = "quarry-m6-ack-$testID"
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
            QUARRY_REAPER_INTERVAL = "100ms"
            QUARRY_REAPER_BATCH_SIZE = "10"
            QUARRY_WORKER_LIVENESS_TIMEOUT = "2s"
        }
        $processIDs.Add($dispatcherProcess.Id)
        Wait-TcpReady `
            -Process $dispatcherProcess `
            -HostName "127.0.0.1" `
            -Port $dispatcherPort `
            -ProcessName "Dispatcher"

        $firstWorkerHostName = "ack-loss-worker-1-$testID"
        $firstWorkerProcess = Start-DistributedProcess -Binary $workerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $dispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "1"
            QUARRY_WORKER_HOSTNAME = $firstWorkerHostName
            QUARRY_WORKER_VERSION = "ack-loss-test"
            QUARRY_HEARTBEAT_INTERVAL = "250ms"
            QUARRY_TEST_SIDE_EFFECT_FILE = $markerPath
            QUARRY_TEST_EXIT_AFTER_HANDLER_SUCCESS = "true"
        }
        $processIDs.Add($firstWorkerProcess.Id)
        $firstWorkerIDs = @(Wait-DistributedWorkers `
            -HostNames @($firstWorkerHostName) `
            -Processes @($firstWorkerProcess))
        $firstWorkerID = $firstWorkerIDs[0]

        $body = @{
            type = "test.side_effect"
            payload = @{}
            max_attempts = 2
            timeout_ms = 30000
        } | ConvertTo-Json -Compress -Depth 4
        $submitted = Invoke-RestMethod `
            -Method Post `
            -Uri "$BaseURL/v1/jobs" `
            -ContentType "application/json" `
            -Body $body `
            -TimeoutSec 10
        if ([string]::IsNullOrWhiteSpace($submitted.id) -or $submitted.status -ne 'queued') {
            throw "Acknowledgement-loss submission did not return a queued job with an ID."
        }

        Wait-AcknowledgementLossFirstExecution `
            -MarkerPath $markerPath `
            -WorkerProcess $firstWorkerProcess
        $unreportedState = Assert-AcknowledgementLossAttemptOneUnreported `
            -BaseURL $baseURL `
            -JobID $submitted.id `
            -WorkerID $firstWorkerID
        Start-Sleep -Milliseconds 750
        $unchangedState = Get-AcknowledgementLossAttemptOneState -JobID $submitted.id
        if ($null -eq $unchangedState -or
            $unchangedState.LeaseExpiresAt -ne $unreportedState.LeaseExpiresAt -or
            $unchangedState.WorkerLastSeenAt -ne $unreportedState.WorkerLastSeenAt) {
            throw "The fault-enabled worker lease or last_seen_at advanced after its injected exit."
        }

        $secondWorkerHostName = "ack-loss-worker-2-$testID"
        $secondWorkerProcess = Start-DistributedProcess -Binary $workerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $dispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "1"
            QUARRY_WORKER_HOSTNAME = $secondWorkerHostName
            QUARRY_WORKER_VERSION = "ack-loss-test"
            QUARRY_HEARTBEAT_INTERVAL = "250ms"
            QUARRY_TEST_SIDE_EFFECT_FILE = $markerPath
            QUARRY_TEST_EXIT_AFTER_HANDLER_SUCCESS = ""
        }
        $processIDs.Add($secondWorkerProcess.Id)
        $secondWorkerIDs = @(Wait-DistributedWorkers `
            -HostNames @($secondWorkerHostName) `
            -Processes @($secondWorkerProcess))
        $secondWorkerID = $secondWorkerIDs[0]
        if ($secondWorkerID -eq $firstWorkerID) {
            throw "The replacement worker reused the fault-enabled worker ID."
        }

        $finalState = Wait-AcknowledgementLossJobSucceeded `
            -BaseURL $baseURL `
            -JobID $submitted.id `
            -Processes @($apiProcess, $dispatcherProcess, $secondWorkerProcess)
        Assert-AcknowledgementLossState `
            -BaseURL $baseURL `
            -JobID $submitted.id `
            -FirstWorkerID $firstWorkerID `
            -SecondWorkerID $secondWorkerID `
            -MarkerPath $markerPath `
            -JobState $finalState
        Write-Host "Acknowledgement-loss test passed: the first worker wrote a side effect without reporting, attempt 1 expired, and the replacement worker wrote the second side effect and completed attempt 2."
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
                throw "Failed to stop $($stopErrors.Count) acknowledgement-loss processes."
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

    Assert-ProcessTestCleanup `
        -TestName "Acknowledgement-loss test" `
        -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory `
        -ProcessIDs $processIDs.ToArray()
    Write-Host "Acknowledgement-loss cleanup verified: processes, marker file, temporary binaries, containers, network, and volume removed."
}

function Test-FailureSuite {
    Test-RecoveryProcesses
    Test-AcknowledgementLossProcesses
    Test-StaleCompletion
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
        "quarry_retries_scheduled_total",
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

function Submit-ObservabilityJob {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$Type,

        [Parameter(Mandatory)]
        [hashtable]$Payload,

        [Parameter(Mandatory)]
        [int]$MaxAttempts,

        [Parameter(Mandatory)]
        [int]$TimeoutMilliseconds
    )

    $body = @{
        type = $Type
        payload = $Payload
        max_attempts = $MaxAttempts
        timeout_ms = $TimeoutMilliseconds
    } | ConvertTo-Json -Compress -Depth 4
    $submitted = Invoke-RestMethod `
        -Method Post `
        -Uri "$BaseURL/v1/jobs" `
        -ContentType "application/json" `
        -Body $body `
        -TimeoutSec 10
    if ([string]::IsNullOrWhiteSpace($submitted.id) -or $submitted.status -ne "queued") {
        throw "Observability-test submission did not return a queued job with an ID."
    }
    return $submitted
}

function Wait-ObservabilityJobStatus {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$ExpectedStatus
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $jobState = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        $jobState = Invoke-RestMethod -Uri "$BaseURL/v1/jobs/$JobID" -TimeoutSec 5
        if ($jobState.status -eq $ExpectedStatus) {
            return $jobState
        }
        if ($jobState.status -in @("succeeded", "dead_lettered", "cancelled")) {
            throw "Observability-test job $JobID reached '$($jobState.status)', expected '$ExpectedStatus'."
        }
        Start-Sleep -Milliseconds 100
    }

    throw "Observability-test job $JobID did not reach '$ExpectedStatus' within 30 seconds. Last status: $($jobState.status)"
}

function Get-ObservabilityJobAttempts {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID
    )

    return @((Invoke-RestMethod -Uri "$BaseURL/v1/jobs/$JobID/attempts" -TimeoutSec 5).attempts)
}

function Wait-JaegerJobTrace {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [int]$MinimumAttemptSpans = 1
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
                $attemptOperations = @("dispatcher.claim", "db.claim_job", "worker.execute", "handler", "db.complete_attempt")
                $hasEveryAttempt = $true
                foreach ($operation in $attemptOperations) {
                    if (@($operations | Where-Object { $_ -eq $operation }).Count -lt $MinimumAttemptSpans) {
                        $hasEveryAttempt = $false
                    }
                }
                $reportCount = @($operations | Where-Object { $_ -like "*ReportAttempt*" }).Count
                if ($missing.Count -eq 0 -and $hasEveryAttempt -and $reportCount -ge $MinimumAttemptSpans) {
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

function Assert-ObservabilityRetryLogs {
    param(
        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$TraceID,

        [Parameter(Mandatory)]
        [string]$DispatcherOutput,

        [Parameter(Mandatory)]
        [string]$WorkerOutput
    )

    $combined = "$DispatcherOutput`n$WorkerOutput"
    foreach ($value in @(
        $JobID,
        $TraceID,
        '"msg":"retry scheduled"',
        '"job_outcome":"timed_out"',
        '"error_code":"execution_timeout"'
    )) {
        if (-not $combined.Contains($value)) {
            throw "Observability-test retry logs do not contain '$value'."
        }
    }
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
            QUARRY_RETRY_BASE_DELAY = "10ms"
            QUARRY_RETRY_MAX_DELAY = "10ms"
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

        $baseURL = "http://127.0.0.1:8080"
        $submitted = Submit-ObservabilityJob -BaseURL $baseURL -Type "demo.echo" `
            -Payload @{ message = "observability-test" } -MaxAttempts 1 -TimeoutMilliseconds 30000
        $jobState = Wait-ObservabilityJobStatus -BaseURL $baseURL -JobID $submitted.id -ExpectedStatus "succeeded"
        if ($jobState.result.message -ne "observability-test") {
            throw "Observability-test job did not complete with the expected public API result."
        }
        $attempts = Get-ObservabilityJobAttempts -BaseURL $baseURL -JobID $submitted.id
        if ($attempts.Count -ne 1 -or $attempts[0].status -ne "succeeded") {
            throw "Observability-test public API did not return one succeeded attempt."
        }

        $retryJob = Submit-ObservabilityJob -BaseURL $baseURL -Type "demo.sleep" `
            -Payload @{ duration_ms = 200 } -MaxAttempts 2 -TimeoutMilliseconds 25
        $retryState = Wait-ObservabilityJobStatus -BaseURL $baseURL -JobID $retryJob.id -ExpectedStatus "dead_lettered"
        if ($retryState.attempt_count -ne 2 -or $retryState.latest_failure.error_code -ne "execution_timeout") {
            throw "Observability-test retry job did not dead-letter after two timed-out attempts."
        }
        $retryAttempts = Get-ObservabilityJobAttempts -BaseURL $baseURL -JobID $retryJob.id
        if ($retryAttempts.Count -ne 2 -or
            @($retryAttempts | Where-Object { $_.status -eq "timed_out" }).Count -ne 2) {
            throw "Observability-test public API did not return two timed-out retry attempts."
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
            -Expression 'sum(quarry_job_attempts_total{outcome="timed_out"})' -Accept { param($value) $value -ge 2 } `
            -Description "two committed timed-out attempts"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression 'sum(quarry_job_execution_duration_seconds_count{outcome="timed_out"})' -Accept { param($value) $value -ge 2 } `
            -Description "two measured timed-out executions"
        $null = Wait-PrometheusValue -BaseURL $prometheusURL `
            -Expression 'sum(quarry_retries_scheduled_total{reason="timed_out"})' -Accept { param($value) $value -ge 1 } `
            -Description "one committed timeout retry"
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
        $retryTraceID = Wait-JaegerJobTrace -BaseURL $jaegerURL -JobID $retryJob.id -MinimumAttemptSpans 2

        Invoke-Docker -Arguments @("compose", "stop", "otel-collector")
        $isolationJob = Submit-ObservabilityJob -BaseURL $baseURL -Type "demo.echo" `
            -Payload @{ message = "collector-unavailable" } -MaxAttempts 1 -TimeoutMilliseconds 30000
        $isolationState = Wait-ObservabilityJobStatus `
            -BaseURL $baseURL -JobID $isolationJob.id -ExpectedStatus "succeeded"
        $isolationAttempts = Get-ObservabilityJobAttempts -BaseURL $baseURL -JobID $isolationJob.id
        if ($isolationState.result.message -ne "collector-unavailable" -or
            $isolationAttempts.Count -ne 1 -or $isolationAttempts[0].status -ne "succeeded") {
            throw "Job state changed when the OpenTelemetry Collector was unavailable."
        }

        $workerOutput = Stop-ObservabilityProcess -Handle $workerHandle
        $dispatcherOutput = Stop-ObservabilityProcess -Handle $dispatcherHandle
        $apiOutput = Stop-ObservabilityProcess -Handle $apiHandle
        Assert-ObservabilityLogs -JobID $submitted.id -TraceID $traceID `
            -ApiOutput $apiOutput -DispatcherOutput $dispatcherOutput -WorkerOutput $workerOutput
        Assert-ObservabilityRetryLogs -JobID $retryJob.id -TraceID $retryTraceID `
            -DispatcherOutput $dispatcherOutput -WorkerOutput $workerOutput
        foreach ($output in @($apiOutput, $dispatcherOutput, $workerOutput)) {
            if (-not $output.Contains($isolationJob.id)) {
                throw "Observability-test logs do not contain Collector-unavailable job $($isolationJob.id)."
            }
        }

        Write-Host "Observability test passed: success trace $traceID, retry trace $retryTraceID, Collector failure isolation, API, logs, Prometheus, Grafana, and Jaeger verified."
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

    Assert-ProcessTestCleanup -TestName "Observability-test" -ComposeProject $composeProject `
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
            Test-FailureSuite
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
        "ack-loss-test" {
            Test-AcknowledgementLossProcesses
        }
        "failure-test" {
            Test-FailureSuite
        }
        "semantics-test" {
            Test-Semantics
        }
        "benchmark-smoke" {
            Test-BenchmarkSmoke
        }
        "benchmark" {
            Test-Benchmark
        }
        "benchmark-verify" {
            Test-BenchmarkVerification
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
