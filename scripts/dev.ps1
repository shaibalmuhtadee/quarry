param(
    [ValidateSet("check", "test", "tools")]
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

    Invoke-Go -Arguments @("test", "./...")
}

$script:GoExecutable = Find-GoExecutable
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
            Test-Tools
            Test-GoPackages
        }
    }
}
finally {
    Pop-Location
}
