param(
    [ValidateSet(
        "check", "test", "tools",
        "db-config", "db-up", "db-ready", "db-down",
        "migrate-up", "migrate-down", "migrate-status", "migration-test", "restart-test",
        "generate", "generate-check", "format-check", "vet", "build",
        "smoke-test", "distributed-test"
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
            Invoke-Docker -Arguments @("compose", "config", "--quiet")
            Test-ComposeSmoke
            Test-DistributedProcesses
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
    }
}
finally {
    Pop-Location
}
