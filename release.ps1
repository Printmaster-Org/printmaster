# PrintMaster Release Script
# Usage: .\release.ps1 [component] [bump-type]
# Components: agent, server, both
# Bump Types: patch, minor, major
# Example: .\release.ps1 agent patch

param(
    [Parameter(Position=0, Mandatory=$true)]
    [ValidateSet('agent', 'server', 'both')]
    [string]$Component,
    
    [Parameter(Position=1, Mandatory=$true)]
    [ValidateSet('patch', 'minor', 'major')]
    [string]$BumpType,
    
    [Parameter(Position=2)]
    [string]$Message = "",
    
    [Parameter()]
    [switch]$SkipTests,
    
    [Parameter()]
    [switch]$SkipPush,
    
    [Parameter()]
    [switch]$CreateGitHubRelease,
    
    [Parameter()]
    [bool]$FailOnEmptyChangelog = $true,

    [Parameter()]
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$ProjectRoot = $PSScriptRoot

# Reset test-pass environment variables at the start of a release
# These are used by build.ps1 to skip redundant tests when building multiple components
# We clear them here so tests run fresh at the start of each release, then persist
# across the agent+server builds to avoid running the same tests twice
$env:PRINTMASTER_JS_TESTS_PASSED = $null
$env:PRINTMASTER_PLAYWRIGHT_PASSED = $null
$env:PRINTMASTER_JS_SYNTAX_PASSED = $null

# ANSI color codes for consistent formatting
$ColorReset = "`e[0m"
$ColorDim = "`e[2m"
$ColorRed = "`e[31m"
$ColorGreen = "`e[32m"
$ColorYellow = "`e[33m"
$ColorBlue = "`e[34m"

function Write-Status {
    param([string]$Message, [string]$Level = "INFO")
    
    # ISO 8601 timestamp format (industry standard)
    $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
    
    # Determine color based on level
    # STEP displays as INFO so uses the same color for consistency
    $levelColor = switch ($Level) {
        "ERROR"   { $ColorRed }
        "WARN"    { $ColorYellow }
        default   { $ColorBlue }  # INFO and STEP
    }
    
    # Map STEP to INFO for standard log levels
    $displayLevel = if ($Level -eq "STEP") { "INFO" } else { $Level }
    
    # Format: dim-timestamp colored-[LEVEL] message
    $consoleMessage = "${ColorDim}${timestamp}${ColorReset} ${levelColor}[${displayLevel}]${ColorReset} ${Message}"
    Write-Host $consoleMessage
}

function Get-GitStatus {
    $status = git status --porcelain 2>&1
    return $status
}

function Test-GitClean {
    $status = Get-GitStatus
    return ($null -eq $status -or $status.Count -eq 0)
}

function Update-Version {
    param(
        [string]$VersionFile,
        [string]$BumpType
    )
    
    if (-not (Test-Path $VersionFile)) {
        throw "VERSION file not found: $VersionFile"
    }
    
    $currentVersion = (Get-Content $VersionFile -Raw).Trim()
    
    if ($currentVersion -notmatch '^(\d+)\.(\d+)\.(\d+)$') {
        throw "Invalid version format in $VersionFile : $currentVersion (expected x.y.z)"
    }
    
    $major = [int]$Matches[1]
    $minor = [int]$Matches[2]
    $patch = [int]$Matches[3]
    
    switch ($BumpType) {
        'major' {
            $major++
            $minor = 0
            $patch = 0
        }
        'minor' {
            $minor++
            $patch = 0
        }
        'patch' {
            $patch++
        }
    }
    
    $newVersion = "$major.$minor.$patch"
    
    if (-not $DryRun) {
        Set-Content -Path $VersionFile -Value $newVersion -NoNewline
    }
    
    return @{
        Old = $currentVersion
        New = $newVersion
    }
}

function Build-Component {
    param([string]$Component, [string]$Version)
    
    Write-Status "Building $Component..." "STEP"
    
    # Build with -Release flag for optimized, stripped binaries
    if ($VerbosePreference -eq 'Continue') {
        $null = & "$ProjectRoot\build.ps1" $Component -Release -VerboseBuild
    } else {
        $null = & "$ProjectRoot\build.ps1" $Component -Release
    }
    
    if ($LASTEXITCODE -ne 0) {
        throw "Build failed for $Component"
    }
    
    $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
    Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorBlue}[INFO]${ColorReset} ${ColorGreen}SUCCESS:${ColorReset} $Component built"
    
    # Create versioned release binary
    $componentDir = Join-Path $ProjectRoot $Component
    $sourceBinary = Join-Path $componentDir "printmaster-$Component.exe"
    $releaseBinary = Join-Path $componentDir "printmaster-$Component-v$Version.exe"
    
    if (Test-Path $sourceBinary) {
        Copy-Item $sourceBinary $releaseBinary -Force
        Write-Status "Created release binary: printmaster-$Component-v$Version.exe" "INFO"
    }
}

function Invoke-Tests {
    param([string]$Component)
    
    if ($SkipTests) {
        Write-Status "Skipping tests (--SkipTests flag)" "WARN"
        return
    }
    
    Write-Status "Running tests for $Component..." "STEP"
    
    Push-Location (Join-Path $ProjectRoot $Component)
    try {
        $testOutput = go test ./... -v 2>&1
        $testExitCode = $LASTEXITCODE
        
        if ($testExitCode -ne 0) {
            Write-Host $testOutput
            throw "Tests failed for $Component"
        }
        
        $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
        Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorBlue}[INFO]${ColorReset} ${ColorGreen}PASS:${ColorReset} $Component passed all tests"
    }
    finally {
        Pop-Location
    }
}

function Save-CommitAndTag {
    param(
        [string]$Component,
        [string]$Version
    )
    
    Write-Status "Committing version bump..." "STEP"
    
    if ($DryRun) {
        Write-Status "[DRY RUN] Would commit VERSION files" "WARN"
        Write-Status "[DRY RUN] Would tag as v$Version" "WARN"
        return
    }
    
    # Add/commit VERSION files
    if ($Component -eq 'both') {
        # Read both new versions first
        $agentVer = (Get-Content (Join-Path $ProjectRoot 'agent\VERSION') -Raw).Trim()
        $serverVer = (Get-Content (Join-Path $ProjectRoot 'server\VERSION') -Raw).Trim()

        # Stage both VERSION files together
        git add agent/VERSION server/VERSION 2>&1 | Out-Null
        
        # Create a single commit with both versions in the message
        if ($Message) {
            $commitMsg = "$Message - agent v$agentVer, server v$serverVer"
        } else {
            $commitMsg = "chore: Release agent v$agentVer, server v$serverVer"
        }
        git commit -m $commitMsg 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git commit failed" }
        
        # Capture the commit SHA - both tags will point here
        $commitSHA = (git rev-parse --verify HEAD).Trim()
        Write-Status "Committed: $commitMsg (sha: $commitSHA)" "INFO"

    } elseif ($Component -eq 'server') {
        git add server/VERSION 2>&1 | Out-Null
        if ($Message) {
            $commitMsg = "$Message - server v$Version"
        } else {
            $commitMsg = "chore: Release server v$Version"
        }
        git commit -m $commitMsg 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git commit failed" }
        Write-Status "Committed: $commitMsg" "INFO"
    } else {
        git add agent/VERSION 2>&1 | Out-Null
        if ($Message) {
            $commitMsg = "$Message - agent v$Version"
        } else {
            $commitMsg = "chore: Release agent v$Version"
        }
        git commit -m $commitMsg 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git commit failed" }
        Write-Status "Committed: $commitMsg" "INFO"
    }
    
    # Tag - create separate tags for each component
    if ($Component -eq 'both') {
        # Get both versions from files
        $agentVer = (Get-Content (Join-Path $ProjectRoot 'agent\VERSION') -Raw).Trim()
        $serverVer = (Get-Content (Join-Path $ProjectRoot 'server\VERSION') -Raw).Trim()
        
        # Both tags point to the same commit so CI/CD runs on the same commit
        $null = git tag -a "agent-v$agentVer" $commitSHA -m "Agent Release v$agentVer" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed for agent" }
        Write-Status "Tagged as agent-v$agentVer -> $commitSHA" "INFO"

        $null = git tag -a "server-v$serverVer" $commitSHA -m "Server Release v$serverVer" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed for server" }
        Write-Status "Tagged as server-v$serverVer -> $commitSHA" "INFO"
        
        # Create floating version tags for agent
        if ($agentVer -match '^(\d+)\.(\d+)\.(\d+)$') {
            $major = $Matches[1]
            $minor = $Matches[2]
            
            # agent-v0 (latest v0.x.x)
            $null = git tag -f -a "agent-v$major" $commitSHA -m "Latest Agent v$major (v$agentVer)" 2>&1
            Write-Status "Updated floating tag: agent-v$major -> v$agentVer" "INFO"
            
            # agent-v0.12 (latest v0.12.x)
            $null = git tag -f -a "agent-v$major.$minor" $commitSHA -m "Latest Agent v$major.$minor (v$agentVer)" 2>&1
            Write-Status "Updated floating tag: agent-v$major.$minor -> v$agentVer" "INFO"
        }
        
        # Create floating version tags for server
        if ($serverVer -match '^(\d+)\.(\d+)\.(\d+)$') {
            $major = $Matches[1]
            $minor = $Matches[2]
            
            # server-v0 (latest v0.x.x)
            $null = git tag -f -a "server-v$major" $commitSHA -m "Latest Server v$major (v$serverVer)" 2>&1
            Write-Status "Updated floating tag: server-v$major -> v$serverVer" "INFO"
            
            # server-v0.12 (latest v0.12.x)
            $null = git tag -f -a "server-v$major.$minor" $commitSHA -m "Latest Server v$major.$minor (v$serverVer)" 2>&1
            Write-Status "Updated floating tag: server-v$major.$minor -> v$serverVer" "INFO"
        }
        
        # Create/update moving latest tags
        $null = git tag -f -a "latest-agent" $commitSHA -m "Latest Agent Release (v$agentVer)" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed for latest-agent" }
        Write-Status "Updated moving tag: latest-agent -> $commitSHA" "INFO"
        
        $null = git tag -f -a "latest-server" $commitSHA -m "Latest Server Release (v$serverVer)" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed for latest-server" }
        Write-Status "Updated moving tag: latest-server -> $commitSHA" "INFO"
        
    }
    elseif ($Component -eq 'server') {
        $null = git tag -a "server-v$Version" -m "Server Release v$Version" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed" }
        Write-Status "Tagged as server-v$Version" "INFO"
        
        # Create floating version tags
        $commitSHA = git rev-parse HEAD
        if ($Version -match '^(\d+)\.(\d+)\.(\d+)$') {
            $major = $Matches[1]
            $minor = $Matches[2]
            
            # server-v0 (latest v0.x.x)
            $null = git tag -f -a "server-v$major" $commitSHA -m "Latest Server v$major (v$Version)" 2>&1
            Write-Status "Updated floating tag: server-v$major -> v$Version" "INFO"
            
            # server-v0.12 (latest v0.12.x)
            $null = git tag -f -a "server-v$major.$minor" $commitSHA -m "Latest Server v$major.$minor (v$Version)" 2>&1
            Write-Status "Updated floating tag: server-v$major.$minor -> v$Version" "INFO"
        }
        
        # Create/update moving latest-server tag
        $null = git tag -f -a "latest-server" $commitSHA -m "Latest Server Release (v$Version)" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed for latest-server" }
        Write-Status "Updated moving tag: latest-server -> $commitSHA" "INFO"
    }
    else {
        $null = git tag -a "agent-v$Version" -m "Agent Release v$Version" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed" }
        Write-Status "Tagged as agent-v$Version" "INFO"
        
        # Create floating version tags
        $commitSHA = git rev-parse HEAD
        if ($Version -match '^(\d+)\.(\d+)\.(\d+)$') {
            $major = $Matches[1]
            $minor = $Matches[2]
            
            # agent-v0 (latest v0.x.x)
            $null = git tag -f -a "agent-v$major" $commitSHA -m "Latest Agent v$major (v$Version)" 2>&1
            Write-Status "Updated floating tag: agent-v$major -> v$Version" "INFO"
            
            # agent-v0.12 (latest v0.12.x)
            $null = git tag -f -a "agent-v$major.$minor" $commitSHA -m "Latest Agent v$major.$minor (v$Version)" 2>&1
            Write-Status "Updated floating tag: agent-v$major.$minor -> v$Version" "INFO"
        }
        
        # Create/update moving latest-agent tag
        $null = git tag -f -a "latest-agent" $commitSHA -m "Latest Agent Release (v$Version)" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "Git tag failed for latest-agent" }
        Write-Status "Updated moving tag: latest-agent -> $commitSHA" "INFO"
    }
}

function Push-Release {
    if ($SkipPush) {
        Write-Status "Skipping push (--SkipPush flag)" "WARN"
        return
    }
    
    Write-Status "Pushing to GitHub..." "STEP"
    
    if ($DryRun) {
        Write-Status "[DRY RUN] Would push commits and tags" "WARN"
        return
    }
    
    # Push commits first
    git push
    if ($LASTEXITCODE -ne 0) {
        throw "Git push failed"
    }
    
    # Push the specific version tags FIRST (these trigger CD workflows)
    # Must be pushed individually to ensure GitHub Actions receives proper push events
    if ($Component -eq 'both') {
        $agentVer = (Get-Content (Join-Path $ProjectRoot 'agent\VERSION') -Raw).Trim()
        $serverVer = (Get-Content (Join-Path $ProjectRoot 'server\VERSION') -Raw).Trim()
        
        Write-Status "Pushing version tag: agent-v$agentVer" "INFO"
        git push origin "agent-v$agentVer"
        if ($LASTEXITCODE -ne 0) {
            throw "Git push agent version tag failed"
        }
        
        Write-Status "Pushing version tag: server-v$serverVer" "INFO"
        git push origin "server-v$serverVer"
        if ($LASTEXITCODE -ne 0) {
            throw "Git push server version tag failed"
        }
    } elseif ($Component -eq 'agent') {
        Write-Status "Pushing version tag: agent-v$finalVersion" "INFO"
        git push origin "agent-v$finalVersion"
        if ($LASTEXITCODE -ne 0) {
            throw "Git push agent version tag failed"
        }
    } else {
        Write-Status "Pushing version tag: server-v$finalVersion" "INFO"
        git push origin "server-v$finalVersion"
        if ($LASTEXITCODE -ne 0) {
            throw "Git push server version tag failed"
        }
    }
    
    # Now force-push floating/moving tags (these don't trigger CD, just for convenience)
    Write-Status "Updating floating tags..." "INFO"
    if ($Component -eq 'both' -or $Component -eq 'agent') {
        $null = git push -f origin latest-agent 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Status "Warning: Failed to push latest-agent tag" "WARN"
        } else {
            Write-Status "Pushed moving tag: latest-agent" "INFO"
        }
        
        # Push floating version tags (agent-v0, agent-v0.12, etc.)
        $agentVer = if ($Component -eq 'both') {
            (Get-Content (Join-Path $ProjectRoot 'agent\VERSION') -Raw).Trim()
        } else {
            $finalVersion
        }
        if ($agentVer -match '^(\d+)\.(\d+)\.(\d+)$') {
            $major = $Matches[1]
            $minor = $Matches[2]
            
            $null = git push -f origin "agent-v$major" 2>&1
            if ($LASTEXITCODE -eq 0) {
                Write-Status "Pushed floating tag: agent-v$major" "INFO"
            }
            
            $null = git push -f origin "agent-v$major.$minor" 2>&1
            if ($LASTEXITCODE -eq 0) {
                Write-Status "Pushed floating tag: agent-v$major.$minor" "INFO"
            }
        }
    }
    if ($Component -eq 'both' -or $Component -eq 'server') {
        $null = git push -f origin latest-server 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Status "Warning: Failed to push latest-server tag" "WARN"
        } else {
            Write-Status "Pushed moving tag: latest-server" "INFO"
        }
        
        # Push floating version tags (server-v0, server-v0.12, etc.)
        $serverVer = if ($Component -eq 'both') {
            (Get-Content (Join-Path $ProjectRoot 'server\VERSION') -Raw).Trim()
        } else {
            $finalVersion
        }
        if ($serverVer -match '^(\d+)\.(\d+)\.(\d+)$') {
            $major = $Matches[1]
            $minor = $Matches[2]
            
            $null = git push -f origin "server-v$major" 2>&1
            if ($LASTEXITCODE -eq 0) {
                Write-Status "Pushed floating tag: server-v$major" "INFO"
            }
            
            $null = git push -f origin "server-v$major.$minor" 2>&1
            if ($LASTEXITCODE -eq 0) {
                Write-Status "Pushed floating tag: server-v$major.$minor" "INFO"
            }
        }
    }
    
    $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
    Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorBlue}[INFO]${ColorReset} ${ColorGreen}SUCCESS:${ColorReset} Pushed to GitHub"
}

function Get-ChangelogSinceLastTag {
    param(
        [string]$Component,
        [string]$CurrentVersion
    )
    
    # Get the last tag for this component
    $tagPattern = if ($Component -eq 'server') { 'server-v*' } else { 'agent-v*' }
    $lastTag = git tag -l $tagPattern --sort=-version:refname | Select-Object -First 1
    
    if (-not $lastTag) {
        Write-Status "No previous tag found - this appears to be the first release" "INFO"
        $commitRange = "HEAD"
    } else {
        Write-Status "Generating changelog since $lastTag" "INFO"
        $commitRange = "$lastTag..HEAD"
    }
    
    # Get commits and parse them
    $commits = git log $commitRange --pretty=format:"%s|||%h" --no-merges 2>$null
    
    if (-not $commits) {
        return "No changes since last release."
    }
    
    # Group commits by type (conventional commits)
    $features = @()
    $fixes = @()
    $docs = @()
    $chores = @()
    $refactors = @()
    $tests = @()
    $other = @()
    
    $commits | ForEach-Object {
        $parts = $_ -split '\|\|\|'
        $message = $parts[0]
        $hash = $parts[1]
        
        # Parse conventional commit format
        if ($message -match '^feat(\([^)]+\))?: (.+)$') {
            $features += "- $($Matches[2]) ($hash)"
        }
        elseif ($message -match '^fix(\([^)]+\))?: (.+)$') {
            $fixes += "- $($Matches[2]) ($hash)"
        }
        elseif ($message -match '^docs(\([^)]+\))?: (.+)$') {
            $docs += "- $($Matches[2]) ($hash)"
        }
        elseif ($message -match '^chore(\([^)]+\))?: (.+)$') {
            $chores += "- $($Matches[2]) ($hash)"
        }
        elseif ($message -match '^refactor(\([^)]+\))?: (.+)$') {
            $refactors += "- $($Matches[2]) ($hash)"
        }
        elseif ($message -match '^test(\([^)]+\))?: (.+)$') {
            $tests += "- $($Matches[2]) ($hash)"
        }
        else {
            $other += "- $message ($hash)"
        }
    }
    
    # Build changelog sections
    $changelog = @()
    
    if ($features.Count -gt 0) {
        $changelog += "### ✨ Features`n"
        $changelog += $features -join "`n"
        $changelog += "`n"
    }
    
    if ($fixes.Count -gt 0) {
        $changelog += "### 🐛 Bug Fixes`n"
        $changelog += $fixes -join "`n"
        $changelog += "`n"
    }
    
    if ($refactors.Count -gt 0) {
        $changelog += "### ♻️ Refactoring`n"
        $changelog += $refactors -join "`n"
        $changelog += "`n"
    }
    
    if ($docs.Count -gt 0) {
        $changelog += "### 📚 Documentation`n"
        $changelog += $docs -join "`n"
        $changelog += "`n"
    }
    
    if ($tests.Count -gt 0) {
        $changelog += "### 🧪 Tests`n"
        $changelog += $tests -join "`n"
        $changelog += "`n"
    }
    
    if ($chores.Count -gt 0) {
        $changelog += "### 🔧 Maintenance`n"
        $changelog += $chores -join "`n"
        $changelog += "`n"
    }
    
    if ($other.Count -gt 0) {
        $changelog += "### 📝 Other Changes`n"
        $changelog += $other -join "`n"
        $changelog += "`n"
    }
    
    if ($changelog.Count -eq 0) {
        # Fallback: if we parsed nothing, return a raw commit list so releases always contain explicit entries
        $raw = git log $commitRange --pretty=format:"- %s (%h)" --no-merges 2>$null
        if (-not $raw) {
            return "No changes since last release."
        }
        return ($raw.Trim())
    }
    
    return ($changelog -join "`n").Trim()
}

function New-GitHubRelease {
    param(
        [string]$Tag,
        [string]$Title,
        [string]$Component,
        [string]$Version,
        [string]$ChangelogContent
    )
    
    Write-Status "Creating GitHub Release..." "STEP"
    
    if ($DryRun) {
        Write-Status "[DRY RUN] Would create GitHub release: $Title" "WARN"
        return
    }
    
    # Check if gh CLI is available
    $ghAvailable = Get-Command gh -ErrorAction SilentlyContinue
    if (-not $ghAvailable) {
        Write-Status "GitHub CLI (gh) not found - skipping release creation" "WARN"
        Write-Status "Install gh CLI from: https://cli.github.com/" "INFO"
        return
    }
    
    # Use pre-generated changelog
    $changelog = $ChangelogContent
    
    # Get the other component's latest version for cross-linking
    $otherComponent = if ($Component -eq 'server') { 'agent' } else { 'server' }
    $otherVersionFile = Join-Path $ProjectRoot "$otherComponent\VERSION"
    $otherVersion = ""
    if (Test-Path $otherVersionFile) {
        $otherVersion = (Get-Content $otherVersionFile -Raw).Trim()
    }
    
    # Build compatibility note with link to other component
    $compatibilityNote = ""
    if ($otherVersion -ne "") {
        $otherComponentTitle = $otherComponent.Substring(0,1).ToUpper() + $otherComponent.Substring(1)
        $compatibilityNote = @"

### 🔄 Compatibility
- **Compatible with $otherComponentTitle**: [v$otherVersion](https://github.com/printmaster-org/printmaster/releases/tag/$otherComponent-v$otherVersion) ([latest-$otherComponent](https://github.com/printmaster-org/printmaster/releases/tag/latest-$otherComponent))
- Use matching versions for best compatibility

"@
    }
    
    # Generate release notes
    $releaseNotes = @"
## PrintMaster $Component v$Version

$changelog
$compatibilityNote

---

### 📦 Installation

#### Docker (Recommended)
``````bash
# Pull the latest image (supports amd64, arm64, arm/v7)
docker pull ghcr.io/printmaster-org/printmaster-${Component}:${Version}
docker pull ghcr.io/printmaster-org/printmaster-${Component}:latest

# Run the container
docker run -d \
  --name printmaster-${Component} \
  -p 9090:9090 \
  -v printmaster-data:/var/lib/printmaster/${Component} \
  ghcr.io/printmaster-org/printmaster-${Component}:latest
``````

#### Binary Installation
1. Download the appropriate binary for your platform from the Assets section below
2. Extract the archive
3. Run the binary with ``--help`` to see available options

**Supported Platforms:**
- Windows (amd64)
- Linux (amd64, arm64)
- macOS (amd64, arm64)

### 🔗 Links
- [Documentation](https://github.com/printmaster-org/printmaster/tree/main/docs)
- [Docker Hub](https://github.com/printmaster-org/printmaster/pkgs/container/printmaster-${Component})
- [Issue Tracker](https://github.com/printmaster-org/printmaster/issues)
"@
    
    # Create release with gh CLI
        try {
        $null = gh release create $Tag `
            --title $Title `
            --notes $releaseNotes `
            --latest 2>&1
        
        if ($LASTEXITCODE -ne 0) {
            throw "GitHub release creation failed"
        }
        
        Write-Status "GitHub Release created: $Title" "INFO"
        Write-Status "View at: https://github.com/printmaster-org/printmaster/releases/tag/$Tag" "INFO"
    }
    catch {
        $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
        Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorRed}[ERROR]${ColorReset} ${ColorRed}FAIL:${ColorReset} GitHub release creation failed: $_"
        Write-Status "Continuing anyway - you can create it manually later" "WARN"
    }
}

# ============================================================================
# MAIN EXECUTION
# ============================================================================

Write-Host ""
Write-Host "╔══════════════════════════════════════════════════════╗" -ForegroundColor Cyan
Write-Host "║           PrintMaster Release Automation             ║" -ForegroundColor Cyan
Write-Host "╚══════════════════════════════════════════════════════╝" -ForegroundColor Cyan
Write-Host ""

Write-Status "Component: $Component" "INFO"
Write-Status "Bump Type: $BumpType" "INFO"
Write-Status "Dry Run: $DryRun" "INFO"
Write-Host ""

try {
    # Pre-flight checks
    Write-Status "Running pre-flight checks..." "STEP"
    
    # Check if we're in a git repository
    $isGitRepo = Test-Path (Join-Path $ProjectRoot ".git")
    if (-not $isGitRepo) {
        throw "Not in a git repository"
    }
    
    # Check for uncommitted changes
    if (-not (Test-GitClean)) {
        Write-Status "Uncommitted changes detected:" "WARN"
        Get-GitStatus | ForEach-Object { Write-Host "  $_" -ForegroundColor Yellow }
        
        $continue = Read-Host "`nContinue anyway? (y/N)"
        if ($continue -ne 'y') {
            throw "Release cancelled - commit or stash changes first"
        }
    } else {
        Write-Status "Working directory is clean" "INFO"
    }
    
    # Check if on main/master branch
    $currentBranch = git rev-parse --abbrev-ref HEAD
    if ($currentBranch -ne 'main' -and $currentBranch -ne 'master') {
        Write-Status "Currently on branch: $currentBranch" "WARN"
        $continue = Read-Host "Not on main branch. Continue? (y/N)"
        if ($continue -ne 'y') {
            throw "Release cancelled"
        }
    }
    
    # Bump version(s)
    Write-Status "Bumping version ($BumpType)..." "STEP"
    
    if ($Component -eq 'both') {
        $agentVersion = Update-Version -VersionFile (Join-Path $ProjectRoot "agent\VERSION") -BumpType $BumpType
        $serverVersion = Update-Version -VersionFile (Join-Path $ProjectRoot "server\VERSION") -BumpType $BumpType
        
        Write-Status "Agent: $($agentVersion.Old) → $($agentVersion.New)" "INFO"
        Write-Status "Server: $($serverVersion.Old) → $($serverVersion.New)" "INFO"
        
        $finalVersion = $agentVersion.New  # Use agent version for tag
    } 
    elseif ($Component -eq 'server') {
        $versionInfo = Update-Version -VersionFile (Join-Path $ProjectRoot "server\VERSION") -BumpType $BumpType
        Write-Status "Server: $($versionInfo.Old) → $($versionInfo.New)" "INFO"
        $finalVersion = $versionInfo.New
    }
    else {
        $versionInfo = Update-Version -VersionFile (Join-Path $ProjectRoot "agent\VERSION") -BumpType $BumpType
        Write-Status "Agent: $($versionInfo.Old) → $($versionInfo.New)" "INFO"
        $finalVersion = $versionInfo.New
    }
    
    # Run tests
    if ($Component -eq 'both') {
        Invoke-Tests -Component 'agent'
        Invoke-Tests -Component 'server'
    } else {
        Invoke-Tests -Component $Component
    }
    
    # Build release binaries
    if ($Component -eq 'both') {
        $agentVersionString = Get-Content (Join-Path $ProjectRoot 'agent\VERSION') -Raw
        $serverVersionString = Get-Content (Join-Path $ProjectRoot 'server\VERSION') -Raw
        Build-Component -Component 'agent' -Version $agentVersionString.Trim()
        Build-Component -Component 'server' -Version $serverVersionString.Trim()
    } else {
        Build-Component -Component $Component -Version $finalVersion
    }
    
    # Generate changelogs BEFORE tagging (so we capture commits up to this point)
    Write-Status "Generating release notes..." "STEP"
    if ($Component -eq 'both') {
        $agentChangelog = Get-ChangelogSinceLastTag -Component 'agent' -CurrentVersion $agentVersion.New
        $serverChangelog = Get-ChangelogSinceLastTag -Component 'server' -CurrentVersion $serverVersion.New
    } else {
        $changelog = Get-ChangelogSinceLastTag -Component $Component -CurrentVersion $finalVersion
    }

    # Validate changelogs: fail by default if changelog is empty/unhelpful
    function Is-ChangelogMeaningful {
        param([string]$Text)
        if (-not $Text) { return $false }
        # If there are explicit list items or section headers, consider it meaningful
        if ($Text -match '^- ' -or $Text -match '^###' ) { return $true }
        if ($Text.Length -gt 80) { return $true }
        return $false
    }

    if ($FailOnEmptyChangelog) {
        if ($Component -eq 'both') {
            if (-not (Is-ChangelogMeaningful $agentChangelog)) {
                if ($DryRun) {
                    Write-Status "[DRY RUN] Agent changelog would be considered empty or not meaningful - would abort release" "WARN"
                } else {
                    throw "Agent changelog is empty or not meaningful. Aborting release. To override, run with -FailOnEmptyChangelog:$false or provide release notes manually."
                }
            }
            if (-not (Is-ChangelogMeaningful $serverChangelog)) {
                if ($DryRun) {
                    Write-Status "[DRY RUN] Server changelog would be considered empty or not meaningful - would abort release" "WARN"
                } else {
                    throw "Server changelog is empty or not meaningful. Aborting release. To override, run with -FailOnEmptyChangelog:$false or provide release notes manually."
                }
            }
        } else {
            if (-not (Is-ChangelogMeaningful $changelog)) {
                if ($DryRun) {
                    Write-Status "[DRY RUN] Changelog would be considered empty or not meaningful - would abort release" "WARN"
                } else {
                    throw "Changelog is empty or not meaningful. Aborting release. To override, run with -FailOnEmptyChangelog:$false or provide release notes manually."
                }
            }
        }
    } else {
        Write-Status "FailOnEmptyChangelog disabled - continuing even if changelog is empty" "WARN"
    }
    
    # Commit and tag
    Save-CommitAndTag -Component $Component -Version $finalVersion
    
    # Push to GitHub
    Push-Release
    
    # Create GitHub Release (optional - CI/CD will create it with all assets)
    if ($CreateGitHubRelease) {
        Write-Status "Creating GitHub Release..." "INFO"
        if ($Component -eq "both") {
            # Create releases for both components using pre-generated changelogs
            New-GitHubRelease -Tag "agent-v$($agentVersion.New)" -Title "Agent v$($agentVersion.New)" -Component "agent" -Version $agentVersion.New -ChangelogContent $agentChangelog
            New-GitHubRelease -Tag "server-v$($serverVersion.New)" -Title "Server v$($serverVersion.New)" -Component "server" -Version $serverVersion.New -ChangelogContent $serverChangelog
        } elseif ($Component -eq "agent") {
            New-GitHubRelease -Tag "agent-v$finalVersion" -Title "Agent v$finalVersion" -Component "agent" -Version $finalVersion -ChangelogContent $changelog
        } else {
            New-GitHubRelease -Tag "server-v$finalVersion" -Title "Server v$finalVersion" -Component "server" -Version $finalVersion -ChangelogContent $changelog
        }
    } else {
        Write-Status "Skipping GitHub release creation - CI/CD will create it with all assets" "INFO"
        Write-Status "GitHub Actions will create the release after building Docker images and binaries" "INFO"
    }
    
    # Summary
    Write-Host ""
    Write-Host "╔══════════════════════════════════════════════════════╗" -ForegroundColor Green
    Write-Host "║                  Release Complete!                   ║" -ForegroundColor Green
    Write-Host "╚══════════════════════════════════════════════════════╝" -ForegroundColor Green
    Write-Host ""
    
    if ($Component -eq "both") {
        Write-Status "Agent Version: $($agentVersion.New)" "INFO"
        Write-Status "Server Version: $($serverVersion.New)" "INFO"
    } else {
        Write-Status "Version: $finalVersion" "INFO"
    }
    Write-Status "Component: $Component" "INFO"
    
    if (-not $SkipPush -and -not $DryRun) {
        $repoUrl = git remote get-url origin 2>$null
        if ($repoUrl) {
            Write-Status "View on GitHub: $repoUrl" "INFO"
        }
    }
    
    Write-Host ""
    
    exit 0
}
catch {
    $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
    Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorRed}[ERROR]${ColorReset} ${ColorRed}FAIL:${ColorReset} Release failed: $_"
    
    # Automatically revert VERSION file changes
    Write-Status "Reverting VERSION file changes..." "WARN"
    
    if (-not $DryRun) {
        if ($Component -eq 'both') {
            git restore agent/VERSION server/VERSION 2>$null
            Write-Status "Reverted VERSION files for agent and server" "INFO"
        } elseif ($Component -eq 'server') {
            git restore server/VERSION 2>$null
            Write-Status "Reverted VERSION file for server" "INFO"
        } else {
            git restore agent/VERSION 2>$null
            Write-Status "Reverted VERSION file for agent" "INFO"
        }
    }
    
    Write-Status "Fix the issue and try again" "WARN"
    exit 1
}
