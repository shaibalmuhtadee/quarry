param(
    [ValidateSet(
        "check", "test", "tools", "staticcheck", "workflow-check",
        "ci-go", "ci-race", "ci-packaging",
        "db-config", "db-up", "db-ready", "db-down",
        "migrate-up", "migrate-down", "migrate-status", "migration-test", "restart-test",
        "generate", "generate-check", "format-check", "vet", "build", "image-test", "compose-test",
        "compose-recovery", "compose-recovery-test", "compose-recovery-down", "k8s-config-test",
        "kind-up", "kind-test", "kind-scaling-test", "kind-down",
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

function Find-KubectlExecutable {
    $kubectlCommand = Get-Command kubectl -ErrorAction SilentlyContinue
    if ($null -ne $kubectlCommand) {
        return $kubectlCommand.Source
    }

    throw "kubectl is not available. Install kubectl with built-in Kustomize support and add it to PATH."
}

function Invoke-Kubectl {
    param(
        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    if ($null -eq $script:KubectlExecutable) {
        $script:KubectlExecutable = Find-KubectlExecutable
    }

    & $script:KubectlExecutable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "kubectl $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
}

function Find-KindExecutable {
    $kindCommand = Get-Command kind -ErrorAction SilentlyContinue
    if ($null -ne $kindCommand) {
        return $kindCommand.Source
    }

    throw "kind is not available. Install kind v0.32.0 or newer and add it to PATH."
}

function Invoke-Kind {
    param(
        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    if ($null -eq $script:KindExecutable) {
        $script:KindExecutable = Find-KindExecutable
    }

    & $script:KindExecutable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "kind $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
    }
}

function Test-Tools {
    Invoke-Go -Arguments @("version")
    Invoke-Go -Arguments @("tool", "actionlint", "-version")
    Invoke-Go -Arguments @("tool", "buf", "--version")
    Invoke-Go -Arguments @("tool", "goose", "-version")
    Invoke-Go -Arguments @("tool", "sqlc", "version")
    Invoke-Go -Arguments @("tool", "staticcheck", "-version")
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

function Test-Staticcheck {
    Invoke-Go -Arguments @("tool", "staticcheck", "./...")
}

function Test-GitHubWorkflows {
    Invoke-Go -Arguments @("tool", "actionlint")
}

function Test-GoRace {
    Invoke-Go -Arguments @("test", "-count=1", "-race", "./...")
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

        [int[]]$ProcessIDs = @()
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
        [Parameter(Mandatory)][object[]]$Workers,
        [Parameter(Mandatory)][int]$ExpectedCount,
        [Parameter(Mandatory)][System.Diagnostics.Process]$LoadgenProcess
    )

    if ($RunID -notmatch '^[a-zA-Z0-9-]+$' -or $Workers.Count -ne 2 -or $ExpectedCount -le 0) {
        throw "Recovery benchmark received an invalid run, worker set, or expected count."
    }
    $workerIDs = @($Workers | ForEach-Object { $_.ID })
    if (@($workerIDs | Where-Object { $_ -notmatch '^[0-9a-f-]{36}$' }).Count -ne 0 -or
        @($workerIDs | Select-Object -Unique).Count -ne 2) {
        throw "Recovery benchmark workers require two distinct UUIDs."
    }
    $query = @"
SELECT
    job_attempts.worker_id::text,
    count(*)
FROM jobs
JOIN job_attempts ON job_attempts.job_id = jobs.id
WHERE jobs.idempotency_key LIKE '$RunID-%'
  AND jobs.status = 'running'
  AND job_attempts.attempt_no = 1
  AND job_attempts.status = 'running'
GROUP BY job_attempts.worker_id
ORDER BY job_attempts.worker_id;
"@
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($LoadgenProcess.HasExited) {
            throw (Get-DistributedProcessFailure `
                -Process $LoadgenProcess `
                -Label "Recovery load generator before a ready worker owned the measured batch")
        }
        foreach ($worker in $Workers) {
            if ($worker.Process.HasExited) {
                throw "Recovery benchmark worker $($worker.ID) exited before crash injection with code $($worker.Process.ExitCode)."
            }
        }
        $rows = @(
            Invoke-PostgresRows -Query $query |
                ForEach-Object { $_.Trim() } |
                Where-Object { $_ -match '^[0-9a-f-]{36}\|\d+$' }
        )
        if ($rows.Count -eq 1) {
            $columns = $rows[0].Split('|')
            if ($columns[0] -in $workerIDs -and [int]$columns[1] -eq $ExpectedCount) {
                return $columns[0]
            }
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Neither ready worker owned all $ExpectedCount running attempt-1 jobs within 30 seconds."
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
    $firstWorkerProcess = $null
    $secondWorkerProcess = $null
    $loadgenProcess = $null
    try {
        New-Item -ItemType Directory -Path $runDirectory | Out-Null
        $firstMetricsPort = Get-AvailableLoopbackPort
        $firstHostName = "$($RunRecord.run_id)-worker-01"
        $firstWorkerProcess = Start-DistributedProcess -Binary $WorkerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $DispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "8"
            QUARRY_WORKER_HOSTNAME = $firstHostName
            QUARRY_WORKER_VERSION = "benchmark-recovery"
            QUARRY_WORKER_METRICS_ADDR = "127.0.0.1:$firstMetricsPort"
            QUARRY_HEARTBEAT_INTERVAL = $HeartbeatInterval
        }
        $ProcessIDs.Add($firstWorkerProcess.Id)
        $firstWorkerID = @(Wait-DistributedWorkers -HostNames @($firstHostName) -Processes @($firstWorkerProcess))[0]

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
        $secondMetricsPort = Get-AvailableLoopbackPort
        $secondHostName = "$($RunRecord.run_id)-worker-02"
        $secondWorkerProcess = Start-DistributedProcess -Binary $WorkerBinary -Environment @{
            QUARRY_DISPATCHER_ADDR = $DispatcherAddress
            QUARRY_WORKER_CONCURRENCY = "8"
            QUARRY_WORKER_HOSTNAME = $secondHostName
            QUARRY_WORKER_VERSION = "benchmark-recovery"
            QUARRY_WORKER_METRICS_ADDR = "127.0.0.1:$secondMetricsPort"
            QUARRY_HEARTBEAT_INTERVAL = $HeartbeatInterval
        }
        $ProcessIDs.Add($secondWorkerProcess.Id)
        $secondWorkerID = @(Wait-DistributedWorkers -HostNames @($secondHostName) -Processes @($secondWorkerProcess))[0]
        if ($secondWorkerID -eq $firstWorkerID) {
            throw "Recovery benchmark workers reused one worker ID."
        }

        $workers = @(
            [pscustomobject]@{
                ID = $firstWorkerID
                Process = $firstWorkerProcess
                MetricsURL = "http://127.0.0.1:$firstMetricsPort/metrics"
            },
            [pscustomobject]@{
                ID = $secondWorkerID
                Process = $secondWorkerProcess
                MetricsURL = "http://127.0.0.1:$secondMetricsPort/metrics"
            }
        )
        $processMetrics = @(
            [pscustomobject]@{ Name = "api"; ProcessID = $APIProcess.Id; MetricsURL = "$BaseURL/metrics" },
            [pscustomobject]@{ Name = "dispatcher"; ProcessID = $DispatcherProcess.Id; MetricsURL = $DispatcherMetricsURL },
            [pscustomobject]@{ Name = "worker-01"; ProcessID = $firstWorkerProcess.Id; MetricsURL = $workers[0].MetricsURL },
            [pscustomobject]@{ Name = "worker-02"; ProcessID = $secondWorkerProcess.Id; MetricsURL = $workers[1].MetricsURL }
        )
        Write-BenchmarkResourceSample `
            -RunID $RunRecord.run_id `
            -ProcessMetrics $processMetrics `
            -PostgresContainer $PostgresContainer `
            -OutputPath $resourcePath

        # Force at least one Workload C batch turnover before selecting the
        # owner. This keeps slow external resource sampling outside the
        # ownership-check-to-kill interval.
        Start-Sleep -Milliseconds 6500
        $targetWorkerID = Wait-BenchmarkRecoveryAttemptBatch `
            -RunID $RunRecord.run_id `
            -Workers $workers `
            -ExpectedCount $RunRecord.config.max_outstanding `
            -LoadgenProcess $loadgenProcess
        $targetWorker = $workers | Where-Object ID -eq $targetWorkerID | Select-Object -First 1
        $replacementWorker = $workers | Where-Object ID -ne $targetWorkerID | Select-Object -First 1
        $replacementWorkerID = $replacementWorker.ID

        $workerTerminatedAt = Stop-BenchmarkTargetWorker -Process $targetWorker.Process
        if ($targetWorkerID -eq $firstWorkerID) {
            $firstWorkerProcess = $null
        }
        else {
            $secondWorkerProcess = $null
        }
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
        foreach ($process in @($loadgenProcess, $secondWorkerProcess, $firstWorkerProcess)) {
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

function Test-KubernetesConfiguration {
    Invoke-Go -Arguments @("test", "-count=1", "./deploy/k8s")
    if ($null -eq $script:KubectlExecutable) {
        $script:KubectlExecutable = Find-KubectlExecutable
    }

    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path `
        ([System.IO.Path]::GetTempPath()) `
        "quarry-k8s-config-$testID"
    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $discoveryBinary = Join-Path $temporaryDirectory "quarry-k8s-discovery$binaryExtension"
    $addressFile = Join-Path $temporaryDirectory "discovery-address"
    $kubeconfigPath = Join-Path $temporaryDirectory "kubeconfig.yaml"
    $cacheDirectory = Join-Path $temporaryDirectory "kubectl-cache"
    $discoveryProcess = $null
    $kustomizations = @(
        "deploy/k8s/base/postgres",
        "deploy/k8s/base/migration",
        "deploy/k8s/base/applications",
        "deploy/k8s/base",
        "deploy/k8s/overlays/kind/postgres",
        "deploy/k8s/overlays/kind/migration",
        "deploy/k8s/overlays/kind/applications",
        "deploy/k8s/overlays/kind"
    )

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        New-Item -ItemType Directory -Path $cacheDirectory | Out-Null
        Invoke-Go -Arguments @(
            "build", "-o", $discoveryBinary, "./deploy/k8s/testdiscovery"
        )
        $discoveryProcess = Start-DistributedProcess `
            -Binary $discoveryBinary `
            -Environment @{} `
            -Arguments @("-address-file", $addressFile)

        $deadline = [DateTime]::UtcNow.AddSeconds(10)
        while ([DateTime]::UtcNow -lt $deadline -and -not (Test-Path -LiteralPath $addressFile)) {
            if ($discoveryProcess.HasExited) {
                throw "Kubernetes discovery fixture exited with code $($discoveryProcess.ExitCode)."
            }
            Start-Sleep -Milliseconds 100
        }
        if (-not (Test-Path -LiteralPath $addressFile)) {
            throw "Kubernetes discovery fixture did not publish its address within 10 seconds."
        }
        $discoveryURL = (Get-Content -LiteralPath $addressFile -Raw).Trim()
        if ([string]::IsNullOrWhiteSpace($discoveryURL)) {
            throw "Kubernetes discovery fixture published an empty address."
        }
        $kubeconfig = @"
apiVersion: v1
kind: Config
clusters:
  - name: quarry-config-test
    cluster:
      server: $discoveryURL
contexts:
  - name: quarry-config-test
    context:
      cluster: quarry-config-test
      user: quarry-config-test
current-context: quarry-config-test
users:
  - name: quarry-config-test
    user: {}
"@
        [System.IO.File]::WriteAllText(
            $kubeconfigPath,
            ($kubeconfig + [Environment]::NewLine),
            [System.Text.UTF8Encoding]::new($false)
        )

        foreach ($kustomization in $kustomizations) {
            $rendered = @(& $script:KubectlExecutable kustomize $kustomization)
            if ($LASTEXITCODE -ne 0) {
                throw "kubectl kustomize $kustomization failed with exit code $LASTEXITCODE."
            }
            if ($rendered.Count -eq 0) {
                throw "kubectl kustomize $kustomization returned no resources."
            }

            $renderedPath = Join-Path `
                $temporaryDirectory `
                "$($kustomization.Replace('/', '-'))-rendered.yaml"
            [System.IO.File]::WriteAllText(
                $renderedPath,
                (($rendered -join [Environment]::NewLine) + [Environment]::NewLine),
                [System.Text.UTF8Encoding]::new($false)
            )
            Invoke-Kubectl -Arguments @(
                "--kubeconfig", $kubeconfigPath,
                "--cache-dir", $cacheDirectory,
                "apply", "--dry-run=client", "--validate=false",
                "--filename", $renderedPath
            ) | Out-Null
        }
    }
    finally {
        try {
            if ($null -ne $discoveryProcess) {
                Stop-DistributedProcess -Process $discoveryProcess
            }
        }
        finally {
            Remove-DistributedTestDirectory -Directory $temporaryDirectory
        }
    }

    if (Test-Path -LiteralPath $temporaryDirectory) {
        throw "Kubernetes configuration test temporary directory remains after cleanup."
    }
    Write-Host "K8s-config-test passed: base and kind stages rendered, client dry-runs passed, focused assertions passed, and temporary files were removed."
}

function Assert-KindVersion {
    if ($null -eq $script:KindExecutable) {
        $script:KindExecutable = Find-KindExecutable
    }

    $output = @(& $script:KindExecutable version)
    if ($LASTEXITCODE -ne 0) {
        throw "kind version failed with exit code $LASTEXITCODE."
    }
    $versionMatch = [regex]::Match(($output -join " "), 'kind v(?<version>\d+\.\d+\.\d+)')
    if (-not $versionMatch.Success) {
        throw "kind returned an unrecognized version: $($output -join ' ')"
    }
    $version = [version]$versionMatch.Groups['version'].Value
    if ($version -lt [version]'0.32.0') {
        throw "kind v$version is too old. Quarry requires kind v0.32.0 or newer."
    }
    return "v$version"
}

function Get-KindClusters {
    return @(
        Invoke-Kind -Arguments @("get", "clusters") |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
}

function Test-KindClusterExists {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    return (Get-KindClusters) -contains $ClusterName
}

function Invoke-KindKubectl {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName,

        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    Invoke-Kubectl -Arguments (@("--context", "kind-$ClusterName") + $Arguments)
}

function Get-KindKubectlJSON {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName,

        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    $output = @(Invoke-KindKubectl -ClusterName $ClusterName -Arguments ($Arguments + @("-o", "json")))
    return (($output -join [Environment]::NewLine) | ConvertFrom-Json)
}

function Remove-KindCluster {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    if ($null -eq $script:KubectlExecutable) {
        $script:KubectlExecutable = Find-KubectlExecutable
    }
    if (Test-KindClusterExists -ClusterName $ClusterName) {
        Invoke-Kind -Arguments @("delete", "cluster", "--name", $ClusterName)
    }
    if (Test-KindClusterExists -ClusterName $ClusterName) {
        throw "kind cluster '$ClusterName' remains after deletion."
    }

    $containers = @(
        Invoke-Docker -Arguments @(
            "ps", "--all", "--quiet",
            "--filter", "label=io.x-k8s.kind.cluster=$ClusterName"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($containers.Count -ne 0) {
        throw "kind cluster '$ClusterName' still has Docker node containers after deletion."
    }

    $contexts = @(
        & $script:KubectlExecutable config get-contexts --output name 2>$null |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($LASTEXITCODE -ne 0) {
        throw "kubectl config get-contexts failed with exit code $LASTEXITCODE."
    }
    if ($contexts -contains "kind-$ClusterName") {
        throw "kubectl context 'kind-$ClusterName' remains after cluster deletion."
    }
}

function Get-KindImages {
    return @(
        [PSCustomObject]@{ Target = "api"; Tag = "quarry-api:kind" },
        [PSCustomObject]@{ Target = "dispatcher"; Tag = "quarry-dispatcher:kind" },
        [PSCustomObject]@{ Target = "worker"; Tag = "quarry-worker:kind" },
        [PSCustomObject]@{ Target = "migration"; Tag = "quarry-migration:kind" }
    )
}

function Build-AndLoadKindImages {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    $images = @(Get-KindImages)
    foreach ($image in $images) {
        Invoke-Docker -Arguments @(
            "build", "--target", $image.Target, "--tag", $image.Tag, "."
        ) | Out-Host
    }

    foreach ($image in $images) {
        Invoke-Kind -Arguments @(
            "load", "docker-image", "--name", $ClusterName, $image.Tag
        ) | Out-Host
    }

    $nodes = @(
        Invoke-Docker -Arguments @(
            "ps", "--quiet",
            "--filter", "label=io.x-k8s.kind.cluster=$ClusterName",
            "--filter", "label=io.x-k8s.kind.role=control-plane"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($nodes.Count -ne 1) {
        throw "kind cluster '$ClusterName' has $($nodes.Count) control-plane containers, expected one."
    }
    $loadedImages = @(
        Invoke-Docker -Arguments @("exec", $nodes[0], "crictl", "images", "--output", "json")
    ) -join [Environment]::NewLine
    foreach ($tag in @($images.Tag)) {
        if (-not $loadedImages.Contains($tag)) {
            throw "kind node does not contain loaded image '$tag'."
        }
    }
}

function Assert-KindPodsReady {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName,

        [Parameter(Mandatory)]
        [string]$Component,

        [Parameter(Mandatory)]
        [int]$ExpectedCount
    )

    $response = Get-KindKubectlJSON `
        -ClusterName $ClusterName `
        -Arguments @(
            "get", "pods", "--namespace", "quarry",
            "--selector", "app.kubernetes.io/component=$Component"
        )
    $pods = @(
        $response.items | Where-Object {
            $deletionTimestamp = $_.metadata.PSObject.Properties["deletionTimestamp"]
            ($null -eq $deletionTimestamp -or
                [string]::IsNullOrWhiteSpace([string]$deletionTimestamp.Value)) -and
                $_.status.phase -notin @("Succeeded", "Failed")
        }
    )
    if ($pods.Count -ne $ExpectedCount) {
        throw "kind component '$Component' has $($pods.Count) active pods, expected $ExpectedCount."
    }
    foreach ($pod in $pods) {
        $readyCondition = @($pod.status.conditions | Where-Object { $_.type -eq "Ready" })
        $containerStatuses = @($pod.status.containerStatuses)
        if ($readyCondition.Count -ne 1 -or $readyCondition[0].status -ne "True" -or
            $containerStatuses.Count -ne 1 -or -not $containerStatuses[0].ready) {
            throw "kind pod '$($pod.metadata.name)' is not Ready with one ready container."
        }
    }
    return $pods
}

function Assert-KindDeploymentReady {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName,

        [Parameter(Mandatory)]
        [string]$Deployment,

        [Parameter(Mandatory)]
        [int]$ExpectedReplicas
    )

    $resource = Get-KindKubectlJSON `
        -ClusterName $ClusterName `
        -Arguments @("get", "deployment/$Deployment", "--namespace", "quarry")
    if ([int]$resource.spec.replicas -ne $ExpectedReplicas -or
        [int]$resource.status.readyReplicas -ne $ExpectedReplicas -or
        [int]$resource.status.availableReplicas -ne $ExpectedReplicas) {
        throw "kind deployment '$Deployment' is not ready at $ExpectedReplicas replicas."
    }
    return $resource
}

function Deploy-KindStages {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    $existingNamespace = @(
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "get", "namespace", "quarry", "--ignore-not-found", "--output", "name"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($existingNamespace.Count -ne 0) {
        throw "fresh kind cluster '$ClusterName' already contains Quarry resources."
    }

    Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
        "apply", "--kustomize", "deploy/k8s/overlays/kind/postgres"
    ) | Out-Host
    Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
        "rollout", "status", "statefulset/postgres", "--namespace", "quarry", "--timeout", "180s"
    ) | Out-Host
    $postgresPods = @(Assert-KindPodsReady `
        -ClusterName $ClusterName -Component "database" -ExpectedCount 1)
    $postgresReadyCondition = @(
        $postgresPods[0].status.conditions | Where-Object { $_.type -eq "Ready" }
    )[0]
    $postgresReadyAt = [DateTimeOffset]::Parse([string]$postgresReadyCondition.lastTransitionTime)

    $jobsBeforeMigration = @(
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "get", "jobs", "--namespace", "quarry", "--output", "name"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($jobsBeforeMigration.Count -ne 0) {
        throw "kind migration Job exists before the migration stage was applied."
    }

    Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
        "apply", "--kustomize", "deploy/k8s/overlays/kind/migration"
    ) | Out-Host
    Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
        "wait", "--for=condition=complete", "job/quarry-migration",
        "--namespace", "quarry", "--timeout", "180s"
    ) | Out-Host
    $migration = Get-KindKubectlJSON `
        -ClusterName $ClusterName `
        -Arguments @("get", "job/quarry-migration", "--namespace", "quarry")
    if ([int]$migration.status.succeeded -ne 1) {
        throw "kind migration Job did not record one successful completion."
    }
    $migrationStartedAt = [DateTimeOffset]::Parse([string]$migration.status.startTime)
    $migrationCompletedAt = [DateTimeOffset]::Parse([string]$migration.status.completionTime)
    if ($migrationStartedAt -lt $postgresReadyAt) {
        throw "kind migration started before PostgreSQL became Ready."
    }

    $deploymentsBeforeApplications = @(
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "get", "deployments", "--namespace", "quarry", "--output", "name"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($deploymentsBeforeApplications.Count -ne 0) {
        throw "kind application Deployments exist before the application stage was applied."
    }

    Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
        "apply", "--kustomize", "deploy/k8s/overlays/kind/applications"
    ) | Out-Host
    foreach ($deployment in @("quarry-api", "quarry-dispatcher", "quarry-worker")) {
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "rollout", "status", "deployment/$deployment",
            "--namespace", "quarry", "--timeout", "180s"
        ) | Out-Host
    }

    $api = Assert-KindDeploymentReady `
        -ClusterName $ClusterName -Deployment "quarry-api" -ExpectedReplicas 1
    $dispatcher = Assert-KindDeploymentReady `
        -ClusterName $ClusterName -Deployment "quarry-dispatcher" -ExpectedReplicas 2
    $worker = Assert-KindDeploymentReady `
        -ClusterName $ClusterName -Deployment "quarry-worker" -ExpectedReplicas 3
    foreach ($deployment in @($api, $dispatcher, $worker)) {
        $createdAt = [DateTimeOffset]::Parse([string]$deployment.metadata.creationTimestamp)
        if ($createdAt -lt $migrationCompletedAt) {
            throw "kind application deployment '$($deployment.metadata.name)' predates migration completion."
        }
    }

    $null = @(Assert-KindPodsReady -ClusterName $ClusterName -Component "api" -ExpectedCount 1)
    $null = @(Assert-KindPodsReady -ClusterName $ClusterName -Component "dispatcher" -ExpectedCount 2)
    $null = @(Assert-KindPodsReady -ClusterName $ClusterName -Component "worker" -ExpectedCount 3)

    $version = Get-KindKubectlJSON -ClusterName $ClusterName -Arguments @("version")
    return [PSCustomObject]@{
        KubernetesVersion = [string]$version.serverVersion.gitVersion
        PostgresReadyAt = $postgresReadyAt
        MigrationCompletedAt = $migrationCompletedAt
    }
}

function Start-KindAPIPortForward {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path `
        ([System.IO.Path]::GetTempPath()) `
        "quarry-kind-port-forward-$testID"
    $standardOutput = Join-Path $temporaryDirectory "port-forward.stdout.log"
    $standardError = Join-Path $temporaryDirectory "port-forward.stderr.log"
    $portForward = $null
    $port = Get-AvailableLoopbackPort
    $baseURL = "http://127.0.0.1:$port"

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        $portForward = Start-DistributedProcess `
            -Binary $script:KubectlExecutable `
            -Environment @{} `
            -Arguments @(
                "--context", "kind-$ClusterName",
                "--namespace", "quarry",
                "port-forward", "--address", "127.0.0.1",
                "service/quarry-api", "${port}:8080"
            ) `
            -StandardOutputPath $standardOutput `
            -StandardErrorPath $standardError

        $deadline = [DateTime]::UtcNow.AddSeconds(30)
        $ready = $false
        while ([DateTime]::UtcNow -lt $deadline) {
            if ($portForward.HasExited) {
                throw (Get-DistributedProcessFailure `
                    -Process $portForward -Label "kind API port-forward")
            }
            try {
                $response = Invoke-WebRequest -Uri "$baseURL/readyz" -TimeoutSec 2
                if ($response.StatusCode -eq 200) {
                    $ready = $true
                    break
                }
            }
            catch {
            }
            Start-Sleep -Milliseconds 200
        }
        if (-not $ready) {
            throw "kind API port-forward did not reach readiness within 30 seconds."
        }
        $health = Invoke-WebRequest -Uri "$baseURL/healthz" -TimeoutSec 5
        if ($health.StatusCode -ne 200) {
            throw "kind API liveness returned HTTP $($health.StatusCode)."
        }

        return [PSCustomObject]@{
            Process = $portForward
            ProcessID = $portForward.Id
            TemporaryDirectory = $temporaryDirectory
            BaseURL = $baseURL
        }
    }
    catch {
        try {
            if ($null -ne $portForward) {
                Stop-DistributedProcess -Process $portForward
            }
        }
        finally {
            Remove-DistributedTestDirectory -Directory $temporaryDirectory
        }
        throw
    }
}

function Stop-KindAPIPortForward {
    param(
        [Parameter(Mandatory)]
        [object]$Forward
    )

    try {
        Stop-DistributedProcess -Process $Forward.Process
    }
    finally {
        Remove-DistributedTestDirectory -Directory $Forward.TemporaryDirectory
    }
    if ($null -ne (Get-Process -Id $Forward.ProcessID -ErrorAction SilentlyContinue)) {
        throw "kind API port-forward process $($Forward.ProcessID) remains after cleanup."
    }
    if (Test-Path -LiteralPath $Forward.TemporaryDirectory) {
        throw "kind API port-forward temporary directory remains after cleanup."
    }
}

function Invoke-KindPublicAPIProof {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    $forward = $null
    try {
        $forward = Start-KindAPIPortForward -ClusterName $ClusterName
        $baseURL = $forward.BaseURL

        $submitted = Submit-ObservabilityJob -BaseURL $baseURL -Type "demo.echo" `
            -Payload @{ message = "kind-test" } -MaxAttempts 1 -TimeoutMilliseconds 30000
        $jobState = Wait-ObservabilityJobStatus `
            -BaseURL $baseURL -JobID $submitted.id -ExpectedStatus "succeeded"
        $attempts = @(Get-ObservabilityJobAttempts -BaseURL $baseURL -JobID $submitted.id)
        if ($jobState.result.message -ne "kind-test" -or
            $attempts.Count -ne 1 -or $attempts[0].status -ne "succeeded" -or
            [string]::IsNullOrWhiteSpace($attempts[0].worker_id)) {
            throw "kind public API did not return the expected result and one succeeded worker attempt."
        }
        return [PSCustomObject]@{
            JobID = [string]$submitted.id
            WorkerID = [string]$attempts[0].worker_id
        }
    }
    finally {
        if ($null -ne $forward) {
            Stop-KindAPIPortForward -Forward $forward
        }
    }
}

function Write-KindDiagnostics {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    if (-not (Test-KindClusterExists -ClusterName $ClusterName)) {
        return
    }
    Write-Warning "kind validation failed; collecting cluster state before cleanup."
    $namespace = @(
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "get", "namespace", "quarry", "--ignore-not-found", "--output", "name"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($namespace.Count -eq 0) {
        Write-Warning "The failed kind cluster contains no Quarry namespace."
        return
    }
    try {
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "get", "all,pvc", "--namespace", "quarry", "--output", "wide"
        )
    }
    catch {
        Write-Warning "Could not read kind cluster diagnostics: $_"
    }
    try {
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "describe", "pods", "--namespace", "quarry"
        )
    }
    catch {
        Write-Warning "Could not describe kind pods: $_"
    }
    try {
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "logs", "job/quarry-migration", "--namespace", "quarry"
        )
    }
    catch {
        Write-Warning "Could not read kind migration logs: $_"
    }
}

function Invoke-KindDeploymentProof {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    $kindVersion = Assert-KindVersion
    if (Test-KindClusterExists -ClusterName $ClusterName) {
        throw "kind cluster '$ClusterName' already exists."
    }

    Invoke-Kind -Arguments @(
        "create", "cluster",
        "--name", $ClusterName,
        "--image", $script:KindNodeImage,
        "--wait", "180s"
    ) | Out-Host
    if (-not (Test-KindClusterExists -ClusterName $ClusterName)) {
        throw "kind did not report newly created cluster '$ClusterName'."
    }

    $null = Build-AndLoadKindImages -ClusterName $ClusterName
    $deployment = Deploy-KindStages -ClusterName $ClusterName
    $apiProof = Invoke-KindPublicAPIProof -ClusterName $ClusterName
    return [PSCustomObject]@{
        KindVersion = $kindVersion
        KubernetesVersion = $deployment.KubernetesVersion
        JobID = $apiProof.JobID
        WorkerID = $apiProof.WorkerID
    }
}

function Write-KindResult {
    param(
        [Parameter(Mandatory)]
        [object]$Result,

        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    Write-Host "kind deployment proof passed."
    Write-Host "Cluster: $ClusterName"
    Write-Host "kind: $($Result.KindVersion)"
    Write-Host "Kubernetes: $($Result.KubernetesVersion)"
    Write-Host "Ready replicas: API 1, dispatcher 2, worker 3"
    Write-Host "Job ID: $($Result.JobID)"
    Write-Host "Succeeded attempt worker: $($Result.WorkerID)"
}

function Start-KindDemonstration {
    $clusterName = "quarry-demo"
    if (Test-KindClusterExists -ClusterName $clusterName) {
        throw "kind cluster '$clusterName' already exists. Run 'pwsh ./scripts/dev.ps1 kind-down' first."
    }

    try {
        $result = Invoke-KindDeploymentProof -ClusterName $clusterName
    }
    catch {
        try {
            Write-KindDiagnostics -ClusterName $clusterName
        }
        finally {
            Remove-KindCluster -ClusterName $clusterName
        }
        throw
    }

    Write-KindResult -Result $result -ClusterName $clusterName
    Write-Host "The cluster remains running for inspection."
    Write-Host "Forward the API with: kubectl --context kind-$clusterName --namespace quarry port-forward service/quarry-api 8080:8080"
    Write-Host "Remove it with: pwsh ./scripts/dev.ps1 kind-down"
}

function Stop-KindDemonstration {
    Remove-KindCluster -ClusterName "quarry-demo"
    Write-Host "kind demonstration cluster removed."
}

function Test-KindDeployment {
    $clusterName = "quarry-m7-$([Guid]::NewGuid().ToString('N').Substring(0, 12))"
    try {
        $result = Invoke-KindDeploymentProof -ClusterName $clusterName
        Write-KindResult -Result $result -ClusterName $clusterName
    }
    catch {
        Write-KindDiagnostics -ClusterName $clusterName
        throw
    }
    finally {
        Remove-KindCluster -ClusterName $clusterName
    }
    Write-Host "Kind-test cleanup verified: port-forward, temporary files, cluster, node container, and kubectl context removed."
}

function Assert-KindWorkerState {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName,

        [Parameter(Mandatory)]
        [int]$ExpectedReplicas,

        [Parameter(Mandatory)]
        [int]$ExpectedConcurrency
    )

    $deployment = Assert-KindDeploymentReady `
        -ClusterName $ClusterName `
        -Deployment "quarry-worker" `
        -ExpectedReplicas $ExpectedReplicas
    $null = @(Assert-KindPodsReady `
        -ClusterName $ClusterName `
        -Component "worker" `
        -ExpectedCount $ExpectedReplicas)

    $workerContainers = @(
        $deployment.spec.template.spec.containers |
            Where-Object { $_.name -eq "worker" }
    )
    if ($workerContainers.Count -ne 1) {
        throw "kind worker Deployment does not contain exactly one worker container."
    }
    $concurrencyVariables = @(
        $workerContainers[0].env |
            Where-Object { $_.name -eq "QUARRY_WORKER_CONCURRENCY" }
    )
    if ($concurrencyVariables.Count -ne 1 -or
        [string]$concurrencyVariables[0].value -ne [string]$ExpectedConcurrency) {
        throw "kind worker Deployment concurrency is not $ExpectedConcurrency."
    }
}

function Invoke-KindScalingConfiguration {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName,

        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$LoadgenBinary,

        [Parameter(Mandatory)]
        [string]$OutputDirectory,

        [Parameter(Mandatory)]
        [string]$RunPrefix,

        [Parameter(Mandatory)]
        [int]$WorkerReplicas,

        [Parameter(Mandatory)]
        [int]$MaxOutstanding,

        [Parameter(Mandatory)]
        [TimeSpan]$Warmup,

        [Parameter(Mandatory)]
        [TimeSpan]$Measurement,

        [Parameter(Mandatory)]
        [TimeSpan]$Drain
    )

    Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
        "scale", "deployment/quarry-worker",
        "--namespace", "quarry",
        "--replicas", [string]$WorkerReplicas
    ) | Out-Host
    Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
        "rollout", "status", "deployment/quarry-worker",
        "--namespace", "quarry", "--timeout", "180s"
    ) | Out-Host
    Assert-KindWorkerState `
        -ClusterName $ClusterName `
        -ExpectedReplicas $WorkerReplicas `
        -ExpectedConcurrency 1

    $runID = "$RunPrefix-w$WorkerReplicas"
    $runDirectory = Join-Path $OutputDirectory $runID
    New-Item -ItemType Directory -Path $runDirectory | Out-Null
    $samplesPath = Join-Path $runDirectory "jobs.jsonl.gz"
    $summaryPath = Join-Path $runDirectory "summary.json"
    $arguments = @(
        "-api-url", $BaseURL,
        "-output", $samplesPath,
        "-summary", $summaryPath,
        "-run-id", $runID,
        "-workload", "b",
        "-seed", "20260830",
        "-warmup", "$([long]$Warmup.TotalMilliseconds)ms",
        "-measurement", "$([long]$Measurement.TotalMilliseconds)ms",
        "-drain-timeout", "$([long]$Drain.TotalMilliseconds)ms",
        "-poll-interval", "10ms",
        "-max-outstanding", [string]$MaxOutstanding,
        "-http-concurrency", [string]$MaxOutstanding,
        "-max-attempts", "1",
        "-job-timeout", "5s"
    )
    $loadgenOutput = @(& $LoadgenBinary @arguments)
    if ($LASTEXITCODE -ne 0) {
        throw "kind scaling load generator failed for $WorkerReplicas workers with exit code $LASTEXITCODE. Output: $($loadgenOutput -join ' ')"
    }
    if (-not (Test-Path -LiteralPath $summaryPath -PathType Leaf)) {
        throw "kind scaling load generator did not write a summary for $WorkerReplicas workers."
    }

    $summary = Get-Content -LiteralPath $summaryPath -Raw | ConvertFrom-Json
    $measurementStartedAt = [DateTimeOffset]::Parse([string]$summary.measurement_started_at)
    $measurementEndedAt = [DateTimeOffset]::Parse([string]$summary.measurement_ended_at)
    $measuredDuration = $measurementEndedAt - $measurementStartedAt
    if ($summary.run_id -ne $runID -or
        [int]$summary.completed_count -le 0 -or
        [int]$summary.completed_count -ne [int]$summary.successful_count -or
        [int]$summary.terminal_failure_count -ne 0 -or
        [int]$summary.submission_failure_count -ne 0 -or
        [int]$summary.incomplete_count -ne 0 -or
        [double]$summary.completed_per_second -le 0 -or
        [math]::Abs(($measuredDuration - $Measurement).TotalMilliseconds) -gt 1) {
        throw "kind scaling Workload B summary failed validation for $WorkerReplicas workers."
    }

    return [PSCustomObject]@{
        Publishable = $false
        Workload = "b"
        WorkerReplicas = $WorkerReplicas
        WorkerConcurrency = 1
        MaxOutstanding = $MaxOutstanding
        HTTPConcurrency = $MaxOutstanding
        WarmupSeconds = [int]$Warmup.TotalSeconds
        MeasurementSeconds = [int]$Measurement.TotalSeconds
        DrainSeconds = [int]$Drain.TotalSeconds
        CompletedJobs = [int]$summary.completed_count
        CompletedJobsPerSecond = [double]$summary.completed_per_second
    }
}

function Write-KindScalingResults {
    param(
        [Parameter(Mandatory)]
        [object[]]$Results
    )

    if (($Results.WorkerReplicas -join ",") -ne "1,4,8" -or
        @($Results.WorkerConcurrency | Select-Object -Unique).Count -ne 1 -or
        @($Results.MaxOutstanding | Select-Object -Unique).Count -ne 1 -or
        @($Results.HTTPConcurrency | Select-Object -Unique).Count -ne 1 -or
        @($Results.WarmupSeconds | Select-Object -Unique).Count -ne 1 -or
        @($Results.MeasurementSeconds | Select-Object -Unique).Count -ne 1 -or
        @($Results.DrainSeconds | Select-Object -Unique).Count -ne 1 -or
        @($Results | Where-Object { $_.Publishable }).Count -ne 0) {
        throw "kind scaling results do not preserve the required 1, 4, and 8 matrix and fixed load settings."
    }

    Write-Host "NON-PUBLISHABLE kind scaling measurements. Do not add these values to benchmarks/results or use them as benchmark claims."
    foreach ($result in $Results) {
        $rate = $result.CompletedJobsPerSecond.ToString(
            "F2",
            [Globalization.CultureInfo]::InvariantCulture
        )
        Write-Host (
            "workload=b workers=$($result.WorkerReplicas) worker_concurrency=1 " +
            "max_outstanding=$($result.MaxOutstanding) http_concurrency=$($result.HTTPConcurrency) " +
            "warmup=$($result.WarmupSeconds)s measurement=$($result.MeasurementSeconds)s " +
            "drain=$($result.DrainSeconds)s completed_jobs=$($result.CompletedJobs) " +
            "completed_jobs_per_second=$rate"
        )
    }
}

function Invoke-KindScalingDemonstration {
    param(
        [Parameter(Mandatory)]
        [string]$ClusterName
    )

    $testID = [Guid]::NewGuid().ToString("N")
    $temporaryDirectory = Join-Path `
        ([System.IO.Path]::GetTempPath()) `
        "quarry-kind-scaling-$testID"
    $binaryExtension = if ($IsWindows) { ".exe" } else { "" }
    $loadgenBinary = Join-Path $temporaryDirectory "quarry-loadgen$binaryExtension"
    $forward = $null
    $results = [System.Collections.Generic.List[object]]::new()

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        Invoke-Go -Arguments @("build", "-o", $loadgenBinary, "./cmd/loadgen") | Out-Host

        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "set", "env", "deployment/quarry-worker",
            "--namespace", "quarry",
            "QUARRY_WORKER_CONCURRENCY=1"
        ) | Out-Host
        Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
            "rollout", "status", "deployment/quarry-worker",
            "--namespace", "quarry", "--timeout", "180s"
        ) | Out-Host
        Assert-KindWorkerState `
            -ClusterName $ClusterName `
            -ExpectedReplicas 3 `
            -ExpectedConcurrency 1

        $forward = Start-KindAPIPortForward -ClusterName $ClusterName
        $warmup = [TimeSpan]::FromSeconds(2)
        $measurement = [TimeSpan]::FromSeconds(8)
        $drain = [TimeSpan]::FromSeconds(10)
        $maxOutstanding = 8
        $runPrefix = "kind-scale-$testID"
        foreach ($workerReplicas in @(1, 4, 8)) {
            $results.Add((Invoke-KindScalingConfiguration `
                -ClusterName $ClusterName `
                -BaseURL $forward.BaseURL `
                -LoadgenBinary $loadgenBinary `
                -OutputDirectory $temporaryDirectory `
                -RunPrefix $runPrefix `
                -WorkerReplicas $workerReplicas `
                -MaxOutstanding $maxOutstanding `
                -Warmup $warmup `
                -Measurement $measurement `
                -Drain $drain))
        }
        Write-KindScalingResults -Results $results.ToArray()
        return $results.ToArray()
    }
    finally {
        try {
            Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
                "apply", "--kustomize", "deploy/k8s/overlays/kind/applications"
            ) | Out-Host
            Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
                "scale", "deployment/quarry-worker",
                "--namespace", "quarry", "--replicas", "3"
            ) | Out-Host
            Invoke-KindKubectl -ClusterName $ClusterName -Arguments @(
                "rollout", "status", "deployment/quarry-worker",
                "--namespace", "quarry", "--timeout", "180s"
            ) | Out-Host
            Assert-KindWorkerState `
                -ClusterName $ClusterName `
                -ExpectedReplicas 3 `
                -ExpectedConcurrency 4
            Write-Host "Restored the documented worker default: replicas=3, concurrency=4."
        }
        finally {
            try {
                if ($null -ne $forward) {
                    Stop-KindAPIPortForward -Forward $forward
                }
            }
            finally {
                Remove-DistributedTestDirectory -Directory $temporaryDirectory
            }
        }
        if (Test-Path -LiteralPath $temporaryDirectory) {
            throw "kind scaling temporary directory remains after cleanup."
        }
    }
}

function Test-KindScaling {
    $clusterName = "quarry-m7-scale-$([Guid]::NewGuid().ToString('N').Substring(0, 10))"
    try {
        $deploymentResult = Invoke-KindDeploymentProof -ClusterName $clusterName
        Write-KindResult -Result $deploymentResult -ClusterName $clusterName
        $results = @(Invoke-KindScalingDemonstration -ClusterName $clusterName)
        if ($results.Count -ne 3) {
            throw "kind scaling demonstration returned $($results.Count) results, expected three."
        }
        Assert-KindWorkerState `
            -ClusterName $clusterName `
            -ExpectedReplicas 3 `
            -ExpectedConcurrency 4
    }
    catch {
        Write-KindDiagnostics -ClusterName $clusterName
        throw
    }
    finally {
        Remove-KindCluster -ClusterName $clusterName
    }

    Write-Host "Kind-scaling-test cleanup verified. Defaults were restored before cluster deletion. The port-forward, output, cluster, node container, and kubectl context were removed."
}

function Test-ObservabilityConfiguration {
    $prometheusConfig = (Resolve-Path -LiteralPath "deploy/observability/prometheus.yml").Path
    $composePrometheusConfig = (Resolve-Path -LiteralPath "deploy/observability/prometheus-compose.yml").Path
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
        "--volume", "${composePrometheusConfig}:/etc/prometheus/prometheus.yml:ro",
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
        "compose",
        "--file", "compose.yaml",
        "--file", "deploy/observability/compose.host-observability.yaml",
        "up", "--detach", "--wait", "--no-deps",
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

function Get-ComposeServiceContainers {
    param(
        [Parameter(Mandatory)]
        [string]$ComposeProject,

        [Parameter(Mandatory)]
        [string]$Service,

        [switch]$Running
    )

    $arguments = @("ps", "--quiet")
    if ($Running) {
        $arguments += @("--filter", "status=running")
    }
    else {
        $arguments = @("ps", "--all", "--quiet")
    }
    $arguments += @(
        "--filter", "label=com.docker.compose.project=$ComposeProject",
        "--filter", "label=com.docker.compose.service=$Service"
    )

    return @(
        Invoke-Docker -Arguments $arguments |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
}

function Wait-PrometheusWorkerTargets {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [int]$MinimumCount
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    $lastCount = 0
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-RestMethod -Uri "$BaseURL/api/v1/targets" -TimeoutSec 5
            $lastCount = @(
                $response.data.activeTargets |
                    Where-Object { $_.health -eq "up" -and $_.labels.job -eq "quarry-worker" }
            ).Count
            if ($lastCount -ge $MinimumCount) {
                return
            }
        }
        catch {
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Prometheus reported $lastCount healthy worker targets, expected at least $MinimumCount."
}

function Test-ComposeWorkflow {
    $testID = [Guid]::NewGuid().ToString("N")
    $composeProject = "quarry-m7-compose-$testID"
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-compose-$testID"
    $savedEnvironment = @{}
    $environmentNames = @(
        "COMPOSE_PROJECT_NAME",
        "QUARRY_POSTGRES_PORT",
        "QUARRY_API_PORT",
        "QUARRY_DISPATCHER_PORT",
        "QUARRY_DISPATCHER_METRICS_PORT",
        "QUARRY_PROMETHEUS_PORT",
        "QUARRY_GRAFANA_PORT",
        "QUARRY_OTEL_GRPC_PORT",
        "QUARRY_OTEL_HTTP_PORT",
        "QUARRY_OTEL_HEALTH_PORT",
        "QUARRY_JAEGER_PORT",
        "QUARRY_WORKER_CONCURRENCY"
    )
    foreach ($name in $environmentNames) {
        $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name)
    }

    $ports = [System.Collections.Generic.List[int]]::new()
    while ($ports.Count -lt 10) {
        $candidate = Get-AvailableLoopbackPort
        if (-not $ports.Contains($candidate)) {
            $ports.Add($candidate)
        }
    }

    $env:COMPOSE_PROJECT_NAME = $composeProject
    $env:QUARRY_POSTGRES_PORT = [string]$ports[0]
    $env:QUARRY_API_PORT = [string]$ports[1]
    $env:QUARRY_DISPATCHER_PORT = [string]$ports[2]
    $env:QUARRY_DISPATCHER_METRICS_PORT = [string]$ports[3]
    $env:QUARRY_PROMETHEUS_PORT = [string]$ports[4]
    $env:QUARRY_GRAFANA_PORT = [string]$ports[5]
    $env:QUARRY_OTEL_GRPC_PORT = [string]$ports[6]
    $env:QUARRY_OTEL_HTTP_PORT = [string]$ports[7]
    $env:QUARRY_OTEL_HEALTH_PORT = [string]$ports[8]
    $env:QUARRY_JAEGER_PORT = [string]$ports[9]
    $env:QUARRY_WORKER_CONCURRENCY = "1"

    $apiURL = "http://127.0.0.1:$($env:QUARRY_API_PORT)"
    $prometheusURL = "http://127.0.0.1:$($env:QUARRY_PROMETHEUS_PORT)"
    $grafanaURL = "http://127.0.0.1:$($env:QUARRY_GRAFANA_PORT)"
    $jaegerURL = "http://127.0.0.1:$($env:QUARRY_JAEGER_PORT)"

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        Invoke-Docker -Arguments @("compose", "config", "--quiet")
        Invoke-Docker -Arguments @("compose", "up", "--build", "--detach", "--scale", "worker=2")

        Wait-ObservabilityEndpoint -Name "Compose API readiness" -URL "$apiURL/readyz"
        Wait-ObservabilityEndpoint `
            -Name "Compose OpenTelemetry Collector" `
            -URL "http://127.0.0.1:$($env:QUARRY_OTEL_HEALTH_PORT)/"
        Wait-ObservabilityEndpoint -Name "Compose Prometheus" -URL "$prometheusURL/-/ready"
        Wait-ObservabilityEndpoint -Name "Compose Grafana" -URL "$grafanaURL/api/health"
        Wait-ObservabilityEndpoint -Name "Compose Jaeger" -URL "$jaegerURL/api/services"

        $migrationContainers = @(Get-ComposeServiceContainers `
            -ComposeProject $composeProject -Service "migration")
        if ($migrationContainers.Count -ne 1) {
            throw "Compose created $($migrationContainers.Count) migration containers, expected exactly one."
        }
        $migrationState = @(
            Invoke-Docker -Arguments @(
                "inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", $migrationContainers[0]
            )
        )[0].Trim()
        if ($migrationState -ne "exited 0") {
            throw "Compose migration state was '$migrationState', expected 'exited 0'."
        }
        $migrationStartedAt = @(
            Invoke-Docker -Arguments @(
                "inspect", "--format", "{{.State.StartedAt}}", $migrationContainers[0]
            )
        )[0].Trim()

        $submitted = Submit-ObservabilityJob -BaseURL $apiURL -Type "demo.echo" `
            -Payload @{ message = "compose-test" } -MaxAttempts 1 -TimeoutMilliseconds 30000
        $jobState = Wait-ObservabilityJobStatus `
            -BaseURL $apiURL -JobID $submitted.id -ExpectedStatus "succeeded"
        $attempts = Get-ObservabilityJobAttempts -BaseURL $apiURL -JobID $submitted.id
        if ($jobState.result.message -ne "compose-test" -or
            $attempts.Count -ne 1 -or $attempts[0].status -ne "succeeded") {
            throw "Compose public API did not return the expected result and one succeeded attempt."
        }

        Wait-PrometheusTargets -BaseURL $prometheusURL
        Wait-PrometheusWorkerTargets -BaseURL $prometheusURL -MinimumCount 2
        Assert-GrafanaDashboard -BaseURL $grafanaURL
        $traceID = Wait-JaegerJobTrace -BaseURL $jaegerURL -JobID $submitted.id

        Invoke-Docker -Arguments @(
            "compose", "up", "--detach", "--no-deps", "--scale", "worker=3", "worker"
        )
        $deadline = [DateTime]::UtcNow.AddSeconds(45)
        do {
            $runningWorkers = @(Get-ComposeServiceContainers `
                -ComposeProject $composeProject -Service "worker" -Running)
            if ($runningWorkers.Count -eq 3) {
                break
            }
            Start-Sleep -Milliseconds 500
        } while ([DateTime]::UtcNow -lt $deadline)
        if ($runningWorkers.Count -ne 3) {
            throw "Compose worker service has $($runningWorkers.Count) running containers, expected 3."
        }
        Wait-PrometheusWorkerTargets -BaseURL $prometheusURL -MinimumCount 3
        $migrationStartedAtAfterScale = @(
            Invoke-Docker -Arguments @(
                "inspect", "--format", "{{.State.StartedAt}}", $migrationContainers[0]
            )
        )[0].Trim()
        if ($migrationStartedAtAfterScale -ne $migrationStartedAt) {
            throw "Compose restarted the one-shot migration while scaling workers."
        }

        Write-Host "Compose test passed: one migration, ready API, successful job $($submitted.id), trace $traceID, observability targets, and three scaled workers verified."
    }
    finally {
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

    Assert-ProcessTestCleanup -TestName "Compose-test" -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory
    Write-Host "Compose-test cleanup verified: containers, network, volume, and temporary directory removed."
}

function Get-ComposeRecoveryArguments {
    param(
        [Parameter(Mandatory)]
        [string]$ComposeProject
    )

    return @(
        "compose",
        "--project-name", $ComposeProject,
        "--file", "compose.yaml",
        "--file", "deploy/compose.recovery.yaml"
    )
}

function Test-ComposeProjectExists {
    param(
        [Parameter(Mandatory)]
        [string]$ComposeProject
    )

    $containers = @(
        Invoke-Docker -Arguments @(
            "ps", "--all", "--quiet",
            "--filter", "label=com.docker.compose.project=$ComposeProject"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    $networks = @(
        Invoke-Docker -Arguments @(
            "network", "ls", "--quiet",
            "--filter", "label=com.docker.compose.project=$ComposeProject"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    $volumes = @(
        Invoke-Docker -Arguments @(
            "volume", "ls", "--quiet",
            "--filter", "label=com.docker.compose.project=$ComposeProject"
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    return $containers.Count -gt 0 -or $networks.Count -gt 0 -or $volumes.Count -gt 0
}

function Remove-ComposeRecoveryStack {
    param(
        [Parameter(Mandatory)]
        [string]$ComposeProject
    )

    $composeArguments = @(Get-ComposeRecoveryArguments -ComposeProject $ComposeProject)
    Invoke-Docker -Arguments ($composeArguments + @("down", "--volumes", "--remove-orphans"))
    if (Test-ComposeProjectExists -ComposeProject $ComposeProject) {
        throw "Compose recovery project '$ComposeProject' still has Docker resources after cleanup."
    }
}

function Assert-ComposeRecoveryTarget {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerID,

        [Parameter(Mandatory)]
        [string]$ComposeProject
    )

    $inspection = @(
        Invoke-Docker -Arguments @(
            "inspect", "--format",
            "{{json .Config.Labels}}|{{.State.Running}}", $ContainerID
        )
    )[0].Trim()
    $separator = $inspection.LastIndexOf('|')
    if ($separator -le 0) {
        throw "Compose recovery target '$ContainerID' returned invalid inspection data."
    }
    $labels = $inspection.Substring(0, $separator) | ConvertFrom-Json
    $running = $inspection.Substring($separator + 1)
    $projectLabel = $labels.PSObject.Properties['com.docker.compose.project'].Value
    $serviceLabel = $labels.PSObject.Properties['com.docker.compose.service'].Value
    if ($projectLabel -ne $ComposeProject -or $serviceLabel -ne "worker" -or $running -ne "true") {
        throw "Refusing to kill container '$ContainerID': expected running $ComposeProject/worker labels."
    }
}

function Wait-ComposeRecoveryAttemptOne {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        $attempts = @(Get-ObservabilityJobAttempts -BaseURL $BaseURL -JobID $JobID)
        if ($attempts.Count -eq 1 -and $attempts[0].status -eq "running" -and
            -not [string]::IsNullOrWhiteSpace($attempts[0].worker_id)) {
            return $attempts[0]
        }
        if ($attempts.Count -gt 1 -or
            ($attempts.Count -eq 1 -and $attempts[0].status -ne "running")) {
            throw "Compose recovery attempt 1 left running state before worker termination."
        }
        Start-Sleep -Milliseconds 100
    }

    throw "Compose recovery attempt 1 did not start within 30 seconds."
}

function Wait-ComposeRecoverySucceeded {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID
    )

    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    $lastStatus = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        $job = Invoke-RestMethod -Uri "$BaseURL/v1/jobs/$JobID" -TimeoutSec 5
        $lastStatus = $job.status
        if ($lastStatus -eq "succeeded") {
            return $job
        }
        if ($lastStatus -notin @("queued", "running", "retry_wait")) {
            throw "Compose recovery job reached unexpected status '$lastStatus'."
        }
        Start-Sleep -Milliseconds 100
    }

    throw "Compose recovery job did not succeed within 60 seconds. Last status: $lastStatus"
}

function Assert-ComposeRecoveryAttempts {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID,

        [Parameter(Mandatory)]
        [string]$FirstWorkerID,

        [Parameter(Mandatory)]
        [object]$JobState
    )

    if ($JobState.status -ne "succeeded" -or $JobState.attempt_count -ne 2 -or
        $JobState.result.slept_ms -ne 6000) {
        throw "Compose recovery job did not finish with two attempts and the expected result."
    }
    $attempts = @(Get-ObservabilityJobAttempts -BaseURL $BaseURL -JobID $JobID)
    if ($attempts.Count -ne 2) {
        throw "Compose recovery job has $($attempts.Count) attempts, expected two."
    }
    if ($attempts[0].attempt_no -ne 1 -or $attempts[0].worker_id -ne $FirstWorkerID -or
        $attempts[0].status -ne "abandoned" -or $attempts[0].error_code -ne "lease_expired" -or
        [string]::IsNullOrWhiteSpace($attempts[0].finished_at)) {
        throw "Compose recovery attempt 1 is not abandoned with lease_expired on the killed worker."
    }
    if ($attempts[1].attempt_no -ne 2 -or $attempts[1].status -ne "succeeded" -or
        $null -ne $attempts[1].error_code -or
        [string]::IsNullOrWhiteSpace($attempts[1].finished_at)) {
        throw "Compose recovery attempt 2 is not a finished success."
    }
    if ($attempts[1].worker_id -eq $FirstWorkerID -or
        [string]::IsNullOrWhiteSpace($attempts[1].worker_id)) {
        throw "Compose recovery attempt 2 did not use a distinct replacement worker identity."
    }
    return $attempts
}

function Wait-JaegerJobObservation {
    param(
        [Parameter(Mandatory)]
        [string]$BaseURL,

        [Parameter(Mandatory)]
        [string]$JobID
    )

    $tags = [Uri]::EscapeDataString((@{ "job.id" = $JobID } | ConvertTo-Json -Compress))
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-RestMethod `
                -Uri "$BaseURL/api/traces?service=quarry-api&tags=$tags&limit=20" `
                -TimeoutSec 5
            $traces = @($response.data)
            if ($traces.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace($traces[0].traceID)) {
                return [string]$traces[0].traceID
            }
        }
        catch {
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Jaeger did not return a trace for Compose recovery job $JobID within 45 seconds."
}

function Invoke-ComposeRecoveryFlow {
    param(
        [Parameter(Mandatory)]
        [string]$ComposeProject
    )

    $composeArguments = @(Get-ComposeRecoveryArguments -ComposeProject $ComposeProject)
    Invoke-Docker -Arguments ($composeArguments + @("config", "--quiet"))
    Invoke-Docker -Arguments (
        $composeArguments + @("up", "--build", "--detach", "--scale", "worker=1")
    ) | Out-Null

    $apiPort = Get-ConfiguredPort -EnvironmentVariable "QUARRY_API_PORT" -Default 8080
    $grafanaPort = Get-ConfiguredPort -EnvironmentVariable "QUARRY_GRAFANA_PORT" -Default 3000
    $jaegerPort = Get-ConfiguredPort -EnvironmentVariable "QUARRY_JAEGER_PORT" -Default 16686
    $apiURL = "http://127.0.0.1:$apiPort"
    $grafanaURL = "http://127.0.0.1:$grafanaPort"
    $jaegerURL = "http://127.0.0.1:$jaegerPort"

    Wait-ObservabilityEndpoint -Name "Compose recovery API" -URL "$apiURL/readyz"
    Wait-ObservabilityEndpoint -Name "Compose recovery Grafana" -URL "$grafanaURL/api/health"
    Wait-ObservabilityEndpoint -Name "Compose recovery Jaeger" -URL "$jaegerURL/api/services"

    $workers = @(Get-ComposeServiceContainers `
        -ComposeProject $ComposeProject -Service "worker" -Running)
    if ($workers.Count -ne 1) {
        throw "Compose recovery requires one running worker container, found $($workers.Count)."
    }
    $targetContainer = $workers[0].Trim()
    Assert-ComposeRecoveryTarget -ContainerID $targetContainer -ComposeProject $ComposeProject

    $submitted = Submit-ObservabilityJob -BaseURL $apiURL -Type "demo.sleep" `
        -Payload @{ duration_ms = 6000 } -MaxAttempts 3 -TimeoutMilliseconds 30000
    $attemptOne = Wait-ComposeRecoveryAttemptOne `
        -BaseURL $apiURL -JobID $submitted.id
    Start-Sleep -Milliseconds 500
    Assert-ComposeRecoveryTarget -ContainerID $targetContainer -ComposeProject $ComposeProject
    Invoke-Docker -Arguments @("kill", $targetContainer) | Out-Null
    Invoke-Docker -Arguments (
        $composeArguments + @("up", "--detach", "--no-deps", "worker")
    ) | Out-Null

    $jobState = Wait-ComposeRecoverySucceeded -BaseURL $apiURL -JobID $submitted.id
    $attempts = @(Assert-ComposeRecoveryAttempts `
        -BaseURL $apiURL `
        -JobID $submitted.id `
        -FirstWorkerID $attemptOne.worker_id `
        -JobState $jobState)
    $replacementWorkers = @(Get-ComposeServiceContainers `
        -ComposeProject $ComposeProject -Service "worker" -Running)
    if ($replacementWorkers.Count -ne 1) {
        throw "Compose recovery has $($replacementWorkers.Count) replacement worker containers, expected one."
    }
    Assert-ComposeRecoveryTarget `
        -ContainerID $replacementWorkers[0].Trim() -ComposeProject $ComposeProject

    Wait-ObservabilityEndpoint -Name "Compose recovery Grafana after recovery" -URL "$grafanaURL/api/health"
    Wait-ObservabilityEndpoint -Name "Compose recovery Jaeger after recovery" -URL "$jaegerURL/api/services"
    Assert-GrafanaDashboard -BaseURL $grafanaURL
    $traceID = Wait-JaegerJobObservation -BaseURL $jaegerURL -JobID $submitted.id

    return [PSCustomObject]@{
        JobID = [string]$submitted.id
        AttemptOne = $attempts[0]
        AttemptTwo = $attempts[1]
        TraceID = $traceID
        GrafanaURL = "$grafanaURL/d/quarry-overview/quarry"
        JaegerURL = $jaegerURL
    }
}

function Write-ComposeRecoveryResult {
    param(
        [Parameter(Mandatory)]
        [object]$Result,

        [Parameter(Mandatory)]
        [string]$ComposeProject
    )

    Write-Host "Compose recovery demonstration passed."
    Write-Host "Job ID: $($Result.JobID)"
    Write-Host "Attempt 1: worker $($Result.AttemptOne.worker_id), abandoned, lease_expired"
    Write-Host "Attempt 2: worker $($Result.AttemptTwo.worker_id), succeeded"
    Write-Host "Grafana: $($Result.GrafanaURL)"
    Write-Host "Jaeger: $($Result.JaegerURL) (trace $($Result.TraceID))"
    Write-Host "Compose project: $ComposeProject"
}

function Start-ComposeRecoveryDemonstration {
    $composeProject = "quarry-recovery-demo"
    if (Test-ComposeProjectExists -ComposeProject $composeProject) {
        throw "Compose recovery project '$composeProject' already exists. Run 'pwsh ./scripts/dev.ps1 compose-recovery-down' first."
    }

    try {
        $result = Invoke-ComposeRecoveryFlow -ComposeProject $composeProject
    }
    catch {
        try {
            Remove-ComposeRecoveryStack -ComposeProject $composeProject
        }
        catch {
            Write-Warning "Failed to clean up unsuccessful Compose recovery demonstration: $_"
        }
        throw
    }

    Write-ComposeRecoveryResult -Result $result -ComposeProject $composeProject
    Write-Host "The stack remains running for inspection."
    Write-Host "Stop it with: pwsh ./scripts/dev.ps1 compose-recovery-down"
}

function Stop-ComposeRecoveryDemonstration {
    $composeProject = "quarry-recovery-demo"
    Remove-ComposeRecoveryStack -ComposeProject $composeProject
    Write-Host "Compose recovery demonstration removed: containers, network, and volume deleted."
}

function Test-ComposeRecoveryDemonstration {
    $testID = [Guid]::NewGuid().ToString("N")
    $composeProject = "quarry-m7-recovery-$testID"
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) "quarry-compose-recovery-$testID"
    $savedEnvironment = @{}
    $environmentNames = @(
        "QUARRY_POSTGRES_PORT",
        "QUARRY_API_PORT",
        "QUARRY_DISPATCHER_PORT",
        "QUARRY_DISPATCHER_METRICS_PORT",
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
    while ($ports.Count -lt 10) {
        $candidate = Get-AvailableLoopbackPort
        if (-not $ports.Contains($candidate)) {
            $ports.Add($candidate)
        }
    }
    for ($index = 0; $index -lt $environmentNames.Count; $index++) {
        [Environment]::SetEnvironmentVariable($environmentNames[$index], [string]$ports[$index])
    }

    try {
        New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
        $result = Invoke-ComposeRecoveryFlow -ComposeProject $composeProject
        Write-ComposeRecoveryResult -Result $result -ComposeProject $composeProject
    }
    finally {
        try {
            Remove-ComposeRecoveryStack -ComposeProject $composeProject
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

    Assert-ProcessTestCleanup `
        -TestName "Compose-recovery-test" `
        -ComposeProject $composeProject `
        -TemporaryDirectory $temporaryDirectory
    Write-Host "Compose-recovery-test cleanup verified: containers, network, volume, and temporary directory removed."
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

function Wait-ImageTestPostgres {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds(45)
    do {
        & $script:DockerExecutable exec $ContainerName `
            pg_isready --username quarry --dbname quarry *> $null
        if ($LASTEXITCODE -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 500
    } while ([DateTimeOffset]::UtcNow -lt $deadline)

    $logs = @(& $script:DockerExecutable logs $ContainerName 2>&1) -join "`n"
    throw "Image-test PostgreSQL did not become ready.`n$logs"
}

function Wait-ImageTestApplication {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName,

        [Parameter(Mandatory)]
        [string]$StartMessage
    )

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds(30)
    do {
        $running = @(
            & $script:DockerExecutable inspect --format "{{.State.Running}}" $ContainerName 2>$null
        )
        if ($LASTEXITCODE -ne 0) {
            throw "Image-test container '$ContainerName' could not be inspected."
        }

        $logs = @(& $script:DockerExecutable logs $ContainerName 2>&1) -join "`n"
        if ($running.Count -eq 1 -and $running[0].Trim() -eq "true" -and $logs.Contains($StartMessage)) {
            return
        }
        if ($running.Count -eq 1 -and $running[0].Trim() -ne "true") {
            throw "Image-test container '$ContainerName' exited before startup.`n$logs"
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTimeOffset]::UtcNow -lt $deadline)

    throw "Image-test container '$ContainerName' did not log '$StartMessage'.`n$logs"
}

function Assert-ContainerImageMetadata {
    param(
        [Parameter(Mandatory)]
        [string]$Image,

        [Parameter(Mandatory)]
        [string]$EntryPoint
    )

    $configurationJSON = @(
        Invoke-Docker -Arguments @("image", "inspect", "--format", "{{json .Config}}", $Image)
    )[0]
    $configuration = $configurationJSON | ConvertFrom-Json
    $entryPointValues = @($configuration.Entrypoint)
    if ($entryPointValues.Count -ne 1 -or $entryPointValues[0] -ne $EntryPoint) {
        throw "Image '$Image' entry point is '$($entryPointValues -join ' ')', expected '$EntryPoint'."
    }
    if ([string]::IsNullOrWhiteSpace($configuration.User) -or $configuration.User -in @("0", "root")) {
        throw "Image '$Image' must configure a non-root user, found '$($configuration.User)'."
    }
}

function Remove-ImageTestContainer {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    $containerIDs = @(
        & $script:DockerExecutable ps --all --quiet --filter "name=^/$ContainerName$" 2>$null |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($containerIDs.Count -gt 0) {
        Invoke-Docker -Arguments @("rm", "--force", $ContainerName) | Out-Null
    }
}

function Test-ContainerImages {
    $suffix = [Guid]::NewGuid().ToString("N")
    $networkName = "quarry-image-test-$suffix"
    $postgresContainer = "quarry-image-test-postgres-$suffix"
    $apiContainer = "quarry-image-test-api-$suffix"
    $dispatcherContainer = "quarry-image-test-dispatcher-$suffix"
    $workerContainer = "quarry-image-test-worker-$suffix"
    $containers = @($workerContainer, $dispatcherContainer, $apiContainer, $postgresContainer)
    $images = @(
        @{ Target = "api"; Tag = "quarry-api:dev"; EntryPoint = "/quarry-api" },
        @{ Target = "dispatcher"; Tag = "quarry-dispatcher:dev"; EntryPoint = "/quarry-dispatcher" },
        @{ Target = "worker"; Tag = "quarry-worker:dev"; EntryPoint = "/quarry-worker" },
        @{ Target = "migration"; Tag = "quarry-migration:dev"; EntryPoint = "/goose" }
    )
    $databaseURL = "postgres://quarry:quarry@postgres:5432/quarry?sslmode=disable"

    try {
        foreach ($image in $images) {
            Invoke-Docker -Arguments @(
                "build", "--target", $image.Target, "--tag", $image.Tag, "."
            )
            Assert-ContainerImageMetadata -Image $image.Tag -EntryPoint $image.EntryPoint
        }

        Invoke-Docker -Arguments @("network", "create", $networkName) | Out-Null
        Invoke-Docker -Arguments @(
            "run", "--detach",
            "--name", $postgresContainer,
            "--network", $networkName,
            "--network-alias", "postgres",
            "--env", "POSTGRES_DB=quarry",
            "--env", "POSTGRES_USER=quarry",
            "--env", "POSTGRES_PASSWORD=quarry",
            "postgres:18.6"
        ) | Out-Null
        Wait-ImageTestPostgres -ContainerName $postgresContainer

        Invoke-Docker -Arguments @(
            "run", "--rm",
            "--network", $networkName,
            "--env", "GOOSE_DBSTRING=$databaseURL",
            "quarry-migration:dev"
        )
        $migrationVersion = @(
            Invoke-Docker -Arguments @(
                "exec", $postgresContainer,
                "psql", "--username", "quarry", "--dbname", "quarry",
                "--tuples-only", "--no-align",
                "--command", "SELECT MAX(version_id) FROM goose_db_version WHERE is_applied;"
            )
        )[0].Trim()
        if ($migrationVersion -ne "8") {
            throw "Migration image applied version '$migrationVersion', expected '8'."
        }

        Invoke-Docker -Arguments @(
            "run", "--detach",
            "--name", $apiContainer,
            "--network", $networkName,
            "--env", "QUARRY_DATABASE_URL=$databaseURL",
            "--env", "QUARRY_HTTP_ADDR=:8080",
            "quarry-api:dev"
        ) | Out-Null
        Wait-ImageTestApplication -ContainerName $apiContainer -StartMessage '"msg":"api starting"'

        Invoke-Docker -Arguments @(
            "run", "--detach",
            "--name", $dispatcherContainer,
            "--network", $networkName,
            "--network-alias", "dispatcher",
            "--env", "QUARRY_DATABASE_URL=$databaseURL",
            "--env", "QUARRY_DISPATCHER_ADDR=:9090",
            "--env", "QUARRY_DISPATCHER_METRICS_ADDR=:9464",
            "quarry-dispatcher:dev"
        ) | Out-Null
        Wait-ImageTestApplication `
            -ContainerName $dispatcherContainer -StartMessage '"msg":"dispatcher starting"'

        Invoke-Docker -Arguments @(
            "run", "--detach",
            "--name", $workerContainer,
            "--network", $networkName,
            "--env", "QUARRY_DISPATCHER_ADDR=dispatcher:9090",
            "--env", "QUARRY_WORKER_HOSTNAME=image-test",
            "--env", "QUARRY_WORKER_METRICS_ADDR=:9465",
            "quarry-worker:dev"
        ) | Out-Null
        Wait-ImageTestApplication -ContainerName $workerContainer -StartMessage '"msg":"worker starting"'

        Write-Host "Image test passed: four non-root targets, migration version 8, and API, dispatcher, and worker startup verified."
    }
    finally {
        foreach ($container in $containers) {
            try {
                Remove-ImageTestContainer -ContainerName $container
            }
            catch {
                Write-Warning "Failed to remove image-test container '$container': $_"
            }
        }
        $networks = @(
            & $script:DockerExecutable network ls --quiet --filter "name=^$networkName$" 2>$null |
                Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
        )
        if ($networks.Count -gt 0) {
            Invoke-Docker -Arguments @("network", "rm", $networkName) | Out-Null
        }
    }

    foreach ($container in $containers) {
        $remaining = @(
            & $script:DockerExecutable ps --all --quiet --filter "name=^/$container$" 2>$null |
                Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
        )
        if ($remaining.Count -ne 0) {
            throw "Image-test container '$container' remains after cleanup."
        }
    }
    $remainingNetwork = @(
        & $script:DockerExecutable network ls --quiet --filter "name=^$networkName$" 2>$null |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
    if ($remainingNetwork.Count -ne 0) {
        throw "Image-test network '$networkName' remains after cleanup."
    }
    Write-Host "Image-test cleanup verified: containers and network removed."
}

function Test-ContainerImageBuilds {
    $images = @(
        @{ Target = "api"; Tag = "quarry-api:ci" },
        @{ Target = "dispatcher"; Tag = "quarry-dispatcher:ci" },
        @{ Target = "worker"; Tag = "quarry-worker:ci" },
        @{ Target = "migration"; Tag = "quarry-migration:ci" }
    )

    foreach ($image in $images) {
        Invoke-Docker -Arguments @(
            "build", "--target", $image.Target, "--tag", $image.Tag, "."
        )
    }

    Write-Host "Container image build check passed: api, dispatcher, worker, and migration targets built."
}

$script:GoExecutable = Find-GoExecutable
$script:GoFmtExecutable = Find-GoFmtExecutable
$script:DockerExecutable = $null
$script:KubectlExecutable = $null
$script:KindExecutable = $null
$script:KindNodeImage = "kindest/node:v1.33.12@sha256:3f5c8443c620245e4d355cfe09e96a91ead32ceaa569d3f1ca9edf0cb2fe2ff4"
$repositoryRoot = Split-Path -Parent $PSScriptRoot

Push-Location $repositoryRoot
try {
    switch ($Command) {
        "tools" {
            Test-Tools
        }
        "staticcheck" {
            Test-Staticcheck
        }
        "workflow-check" {
            Test-GitHubWorkflows
        }
        "ci-go" {
            Invoke-Go -Arguments @("mod", "tidy", "-diff")
            Test-GoFormatting
            Test-Tools
            Invoke-Sqlc -SqlcCommand "diff"
            Test-BufGeneratedCode
            Test-GitHubWorkflows
            Test-Staticcheck
            Test-GoVet
            Test-GoPackages
            Test-GoBuild
        }
        "ci-race" {
            Test-GoRace
        }
        "ci-packaging" {
            Test-ContainerImageBuilds
            Invoke-Docker -Arguments @("compose", "config", "--quiet")
            Test-KubernetesConfiguration
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
            Test-GitHubWorkflows
            Test-Staticcheck
            Test-GoVet
            Test-GoPackages
            Test-GoBuild
            Test-ContainerImages
            Test-KubernetesConfiguration
            Test-KindScaling
            Test-ComposeWorkflow
            Test-ComposeRecoveryDemonstration
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
        "image-test" {
            Test-ContainerImages
        }
        "compose-test" {
            Test-ComposeWorkflow
        }
        "compose-recovery" {
            Start-ComposeRecoveryDemonstration
        }
        "compose-recovery-test" {
            Test-ComposeRecoveryDemonstration
        }
        "compose-recovery-down" {
            Stop-ComposeRecoveryDemonstration
        }
        "k8s-config-test" {
            Test-KubernetesConfiguration
        }
        "kind-up" {
            Start-KindDemonstration
        }
        "kind-test" {
            Test-KindDeployment
        }
        "kind-scaling-test" {
            Test-KindScaling
        }
        "kind-down" {
            Stop-KindDemonstration
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
