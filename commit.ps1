<#
.SYNOPSIS
    Build and commit script for PrintMaster
.DESCRIPTION
    Runs build.ps1 both before attempting to commit. Only commits if the build succeeds.
.PARAMETER Message
    The commit message (required, Conventional Commits: type(scope): summary)
.PARAMETER Push
    Optional switch to push after committing
.EXAMPLE
    .\commit.ps1 -Message "fix(scanner): handle SNMP timeout on slow devices"
    .\commit.ps1 "fix(scanner): handle SNMP timeout on slow devices" -Push
#>

param(
    [Parameter(Mandatory=$true, Position=0)]
    [string]$Message,
    
    [switch]$Push
)

$ErrorActionPreference = "Stop"

Write-Host "=== PrintMaster Commit Script ===" -ForegroundColor Cyan
Write-Host ""

# Step 1: Run build
Write-Host "[1/3] Building agent and server..." -ForegroundColor Yellow
try {
    & "$PSScriptRoot\build.ps1" both
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Build failed with exit code $LASTEXITCODE" -ForegroundColor Red
        exit 1
    }
} catch {
    Write-Host "Build failed: $_" -ForegroundColor Red
    exit 1
}
Write-Host "Build succeeded!" -ForegroundColor Green
Write-Host ""

# Step 2: Stage and commit
Write-Host "[2/3] Staging and committing changes..." -ForegroundColor Yellow
git add -A
if ($LASTEXITCODE -ne 0) {
    Write-Host "Failed to stage changes" -ForegroundColor Red
    exit 1
}

git commit -m $Message
if ($LASTEXITCODE -ne 0) {
    Write-Host "Commit failed (nothing to commit?)" -ForegroundColor Red
    exit 1
}
Write-Host "Committed successfully!" -ForegroundColor Green
Write-Host ""

# Step 3: Push (optional)
if ($Push) {
    Write-Host "[3/3] Pushing to remote..." -ForegroundColor Yellow
    git push
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Push failed" -ForegroundColor Red
        exit 1
    }
    Write-Host "Pushed successfully!" -ForegroundColor Green
} else {
    Write-Host "[3/3] Skipping push (use -Push to push automatically)" -ForegroundColor DarkGray
}

Write-Host ""
Write-Host "=== Done ===" -ForegroundColor Cyan
