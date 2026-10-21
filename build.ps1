# PrintMaster Build Script
# Usage: .\build.ps1 [target] [options]
# Targets: agent, server, both, all, clean, test, test-storage, test-all
# Options: -Release, -VerboseBuild, -IncrementVersion, -SkipTests, -AllTests, -TestOnly, -TestArgs

param(
    [Parameter(Position=0)]
    [ValidateSet('agent', 'server', 'both', 'all', 'clean', 'test', 'test-storage', 'test-all')]
    [string]$Target = 'agent',
    
    [Parameter()]
    [switch]$Release,
    
    [Parameter()]
    [switch]$VerboseBuild,
    
    [Parameter()]
    [switch]$IncrementVersion,
    [switch]$SkipTests,
    [switch]$AllTests,
    [switch]$TestOnly,
    [string[]]$TestArgs = @()
)

$ErrorActionPreference = 'Continue'
$ProjectRoot = $PSScriptRoot
$LogDir = Join-Path $ProjectRoot "logs"
$LogFile = $null  # Will be set dynamically with version info
$MaxLogFiles = 10

# Ensure logs directory exists
if (-not (Test-Path $LogDir)) {
    New-Item -ItemType Directory -Path $LogDir -Force | Out-Null
}

# ANSI color codes
$ColorReset = "`e[0m"
$ColorDim = "`e[2m"
$ColorRed = "`e[31m"
$ColorGreen = "`e[32m"
$ColorYellow = "`e[33m"
$ColorBlue = "`e[34m"

function Write-BuildLog {
    param([string]$Message, [string]$Level = "INFO")
    
    # Use default log if $LogFile not set yet
    if (-not $script:LogFile) {
        $script:LogFile = Join-Path $LogDir "build.log"
    }
    
    # ISO 8601 timestamp format (industry standard)
    $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
    
    # Determine color based on level
    $levelColor = switch ($Level) {
        "ERROR"   { $ColorRed }
        "WARN"    { $ColorYellow }
        default   { $ColorBlue }  # INFO uses blue
    }
    
    # Format: dim-timestamp colored-[LEVEL] message
    $consoleMessage = "${ColorDim}${timestamp}${ColorReset} ${levelColor}[${Level}]${ColorReset} ${Message}"
    $logMessage = "[$timestamp] [$Level] $Message"
    
    # Write to console
    Write-Host $consoleMessage
    
    # Append to log file (plain text)
    Add-Content -Path $script:LogFile -Value $logMessage
}

function Set-BuildLogFile {
    param(
        [string]$Component,
        [string]$Version,
        [int]$BuildNumber
    )
    
    $timestamp = Get-Date -Format "yyyyMMdd_HHmmss"
    $logFileName = "build_${Component}_${Version}.${BuildNumber}_${timestamp}.log"
    $script:LogFile = Join-Path $LogDir $logFileName
    
    Write-BuildLog "=== Build Log ===" "INFO"
    Write-BuildLog "Component: $Component" "INFO"
    Write-BuildLog "Version: $Version.$BuildNumber" "INFO"
    Write-BuildLog "Log File: $logFileName" "INFO"
    
    # Clean up old log files, keep only recent ones
    Get-ChildItem $LogDir -Filter "build_*.log" | 
        Sort-Object LastWriteTime -Descending | 
        Select-Object -Skip $MaxLogFiles | 
        Remove-Item -Force -ErrorAction SilentlyContinue
}

function Remove-BuildArtifacts {
    Write-BuildLog "Cleaning build artifacts..."
    
    $patterns = @(
        "agent\*.exe",
        "agent\*.syso",
        "ui\*.exe"
    )
    
    foreach ($pattern in $patterns) {
        $path = Join-Path $ProjectRoot $pattern
        Get-ChildItem $path -ErrorAction SilentlyContinue | ForEach-Object {
            Write-BuildLog "Removing $($_.Name)" "INFO"
            Remove-Item $_.FullName -Force
        }
    }
    
    Write-BuildLog "Clean complete" "INFO"
}

function Test-Prerequisites {
    Write-BuildLog "Checking build prerequisites..." "INFO"
    
    # Check Go installation
    $goVersion = & go version 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-BuildLog "Go is not installed or not in PATH" "ERROR"
        Write-BuildLog "Install Go from: https://go.dev/dl/" "ERROR"
        return $false
    }
    Write-BuildLog "Found: $goVersion" "INFO"
    
    # Check git (for version injection)
    $gitVersion = & git --version 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-BuildLog "Git not found - version info will be limited" "WARN"
    } else {
        Write-BuildLog "Found: $gitVersion" "INFO"
    }
    
    return $true
}

function Build-Component {
    param(
        [Parameter(Mandatory=$true)]
        [ValidateSet('agent', 'server')]
        [string]$Component,
        [bool]$IsRelease = $false,
        [switch]$IncrementVersion = $false
    )
    
    $buildType = if ($IsRelease) { "RELEASE" } else { "DEV" }
    $displayName = if ($Component -eq 'agent') { "Agent" } else { "Server" }
    $exeName = "printmaster-$Component.exe"
    
    Write-BuildLog "Building PrintMaster $displayName ($buildType)..."
    Write-BuildLog "Working directory: $(Get-Location)"
    
    Push-Location (Join-Path $ProjectRoot $Component)
    
    try {
        if ($Component -eq 'agent' -and $IsRelease) {
            Push-Location (Join-Path $ProjectRoot 'common')
            try {
                go run .\cmd\update-keys check-root -root ..\agent\update_publisher_root.json
                if ($LASTEXITCODE -ne 0) {
                    throw 'Agent release requires an owner-provisioned publisher root'
                }
            } finally {
                Pop-Location
            }
        }
        # Read version from VERSION file
        $versionFile = Join-Path $ProjectRoot "$Component\VERSION"
        if (Test-Path $versionFile) {
            $version = (Get-Content $versionFile -Raw).Trim()
        } else {
            $version = "0.0.0"
            Write-BuildLog "VERSION file not found, using default: $version" "WARN"
        }
        
        # Auto-increment version for release builds if requested
        if ($IsRelease -and $IncrementVersion) {
            if ($version -match '^(\d+)\.(\d+)\.(\d+)$') {
                $major = [int]$Matches[1]
                $minor = [int]$Matches[2]
                $patch = [int]$Matches[3]
                $patch++
                $version = "$major.$minor.$patch"
                Set-Content -Path $versionFile -Value $version -NoNewline
                Write-BuildLog "$displayName version incremented to: $version" "INFO"
            } else {
                Write-BuildLog "Invalid version format in VERSION file, expected x.y.z" "WARN"
            }
        }
        
        # Get or increment build number (reset on version change)
        $buildNumberFile = Join-Path $ProjectRoot "$Component\.buildnumber"
        $lastVersionFile = Join-Path $ProjectRoot "$Component\.lastversion"
        
        $lastVersion = ""
        if (Test-Path $lastVersionFile) {
            $lastVersion = (Get-Content $lastVersionFile -Raw).Trim()
        }
        
        if ($lastVersion -ne $version) {
            $buildNumber = 1
            Write-BuildLog "Version changed from $lastVersion to $version, resetting build number" "INFO"
        } else {
            if (Test-Path $buildNumberFile) {
                $buildNumber = [int](Get-Content $buildNumberFile -Raw).Trim()
                $buildNumber++
            } else {
                $buildNumber = 1
            }
        }
        
        Set-Content -Path $buildNumberFile -Value $buildNumber -NoNewline
        Set-Content -Path $lastVersionFile -Value $version -NoNewline
        
        # Create version string
        if ($IsRelease) {
            $versionString = "$version"
        } else {
            $versionString = "$(($version -split '-')[0]).$buildNumber-dev"
            if ($script:BranchSuffix) { $versionString = "$versionString$script:BranchSuffix" }
        }
        
        # Set versioned log file
        Set-BuildLogFile -Component $Component -Version $version -BuildNumber $buildNumber

        # Get build metadata
        $buildTime = Get-Date -Format "yyyy-MM-ddTHH:mm:ssZ"
        $gitCommit = (git rev-parse --short HEAD 2>$null) -join ""
        if (-not $gitCommit) { $gitCommit = "unknown" }
        
        # Build ldflags for version injection
        $buildTypeString = if (-not $IsRelease) { "dev" } elseif ($version -like '*-beta.*') { "beta" } else { "release" }
        $ldflags = "-X 'main.Version=$versionString' -X 'main.BuildTime=$buildTime' -X 'main.GitCommit=$gitCommit' -X 'main.BuildType=$buildTypeString' -X 'main.GitBranch=$script:GitBranch'"
        
        # Generate Windows version resource (only on Windows)
        if ($IsWindows -or $env:OS -eq "Windows_NT") {
            Write-BuildLog "Generating Windows version resource..."
            $winverScript = Join-Path $ProjectRoot "tools\generate-winver.ps1"
            if (Test-Path $winverScript) {
                try {
                    & $winverScript -Component $Component -Version $versionString -GitCommit $gitCommit -BuildTime $buildTime 2>&1 | ForEach-Object {
                        Write-BuildLog $_.ToString() "INFO"
                    }
                }
                catch {
                    Write-BuildLog "Warning: Failed to generate version resource: $_" "WARN"
                }
            }
        }
        
        # Build arguments
        $buildArgs = @("build")
        
        if ($IsRelease) {
            Write-BuildLog "Building optimized release binary..."
            $ldflags += " -s -w"
            $buildArgs += "-trimpath"
        } else {
            Write-BuildLog "Building development binary (with debug info)..."
        }
        
        $buildArgs += "-ldflags", $ldflags
        
        if ($VerboseBuild) {
            $buildArgs += "-v"
        }
        
        $buildArgs += "-o", $exeName
        $buildArgs += "."
        
        Write-BuildLog "Version: $versionString$(if (-not $IsRelease) { " (build #$buildNumber)" })"
        Write-BuildLog "Command: go $($buildArgs -join ' ')"
        Write-BuildLog "Build Time: $buildTime"
        Write-BuildLog "Git Commit: $gitCommit"
        
        # Execute build (CGO_ENABLED=0 for agent only - pure Go build)
        if ($Component -eq 'agent') {
            $env:CGO_ENABLED = 0
        }
        $buildOutput = & go @buildArgs 2>&1
        $buildExitCode = $LASTEXITCODE
        
        if ($buildOutput) {
            $buildOutput | ForEach-Object { 
                Write-BuildLog $_.ToString()
            }
        }
        
        if ($buildExitCode -eq 0) {
            if (Test-Path $exeName) {
                $fileSize = (Get-Item $exeName).Length
                $fileSizeMB = [math]::Round($fileSize / 1MB, 2)
                $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
                Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorBlue}[INFO]${ColorReset} ${ColorGreen}SUCCESS:${ColorReset} $exeName ($fileSizeMB MB)"
                Add-Content -Path $script:LogFile -Value "[$timestamp] [INFO] SUCCESS: $exeName ($fileSizeMB MB)"
                Write-BuildLog "Binary location: $(Join-Path (Get-Location) $exeName)" "INFO"
            } else {
                Write-BuildLog "Build reported success but executable not found!" "ERROR"
                return $false
            }
            return $true
        } else {
            Write-BuildLog "Build failed with exit code $buildExitCode" "ERROR"
            return $false
        }
    }
    finally {
        Pop-Location
    }
}

# Wrapper functions for backward compatibility
function Build-Agent {
    param(
        [bool]$IsRelease = $false,
        [switch]$IncrementVersion = $false
    )
    Build-Component -Component 'agent' -IsRelease:$IsRelease -IncrementVersion:$IncrementVersion
}

function Build-Server {
    param(
        [bool]$IsRelease = $false,
        [switch]$IncrementVersion = $false
    )
    Build-Component -Component 'server' -IsRelease:$IsRelease -IncrementVersion:$IncrementVersion
}

function Test-Storage {
    Write-BuildLog "Running storage tests..."
    Write-BuildLog "Working directory: $(Get-Location)"
    
    Push-Location (Join-Path $ProjectRoot "agent")
    
    try {
        $testArgs = @((Join-Path $ProjectRoot 'scripts\run-tests.js'), '--suite', 'go', '--module', 'agent', '--package', './storage')
        
        Write-BuildLog "Command: node $($testArgs -join ' ')"
        
        $testOutput = & node @testArgs 2>&1
        $testExitCode = $LASTEXITCODE
        
        # Log all output
        $testOutput | ForEach-Object { 
            $line = $_.ToString()
            if ($line -match "FAIL|ERROR") {
                Write-BuildLog $line "ERROR"
            } elseif ($line -match "PASS|ok") {
                Write-BuildLog $line "INFO"
            } else {
                Write-BuildLog $line
            }
        }
        
        if ($testExitCode -eq 0) {
            Write-BuildLog "Storage tests passed" "INFO"
            return $true
        } else {
            Write-BuildLog "Storage tests failed with exit code $testExitCode" "ERROR"
            return $false
        }
    }
    finally {
        Pop-Location
    }
}

function Test-All {
    Write-BuildLog "Running all discovered Go module tests..."
    Write-BuildLog "Working directory: $(Get-Location)"
    
    Push-Location (Join-Path $ProjectRoot "agent")
    
    try {
        $testArgs = @((Join-Path $ProjectRoot 'scripts\run-tests.js'), '--suite', 'go')
        
        Write-BuildLog "Command: node $($testArgs -join ' ')"
        
        $testOutput = & node @testArgs 2>&1
        $testExitCode = $LASTEXITCODE
        
        # Log all output
        $testOutput | ForEach-Object { 
            $line = $_.ToString()
            if ($line -match "FAIL|ERROR") {
                Write-BuildLog $line "ERROR"
            } elseif ($line -match "PASS|ok") {
                Write-BuildLog $line "INFO"
            } else {
                Write-BuildLog $line
            }
        }
        
        if ($testExitCode -eq 0) {
            Write-BuildLog "All tests passed" "INFO"
            return $true
        } else {
            Write-BuildLog "Tests failed with exit code $testExitCode" "ERROR"
            return $false
        }
    }
    finally {
        Pop-Location
    }
}

# Main execution
Write-BuildLog "=== PrintMaster Build Script ===" "INFO"
Write-BuildLog "Target: $Target" "INFO"
Write-BuildLog "Project Root: $ProjectRoot" "INFO"

if ($Target -in @('agent', 'server', 'both', 'all') -or $TestOnly) {
    if ($SkipTests) {
        Write-BuildLog "Skipping shared test gate (-SkipTests)" "WARN"
    } else {
        $gateArgs = @(if ($AllTests) { '--all' } else { '--skip-playwright' })
        if ($TestArgs.Count) { $gateArgs = @($TestArgs) }
        & (Join-Path $ProjectRoot 'tests.ps1') @gateArgs
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    if ($TestOnly) { exit 0 }
}

# Check prerequisites (Go, staticcheck, git)
if (-not (Test-Prerequisites)) {
    Write-BuildLog "Prerequisites check failed - cannot continue" "ERROR"
    exit 1
}

# Determine current git branch and create a branch suffix for dev builds when not main/master
try {
    $gitBranchRaw = (git rev-parse --abbrev-ref HEAD 2>$null) -join ""
} catch {
    $gitBranchRaw = ""
}
if (-not $gitBranchRaw) { $gitBranchRaw = "unknown" }

# Sanitize branch for use in version strings: allow alphanumerics, dot, underscore and dash
$gitBranchSanitized = $gitBranchRaw -replace '[^A-Za-z0-9_.-]', '-'

# If branch is a mainline branch, don't add suffix; otherwise create a suffix like -feature-x
if ($gitBranchSanitized -in @('main','master','trunk')) {
    $script:BranchSuffix = ''
} else {
    $script:BranchSuffix = "-" + $gitBranchSanitized
}

# Expose GitBranch to ldflags as well
$script:GitBranch = $gitBranchSanitized

$success = $false

switch ($Target) {
    'clean' {
        Remove-BuildArtifacts
        $success = $true
    }
    'agent' {
        $success = Build-Agent -IsRelease:$Release -IncrementVersion:$IncrementVersion
    }
    'server' {
        $success = Build-Server -IsRelease:$Release -IncrementVersion:$IncrementVersion
    }
    'both' {
        # Build both agent and server
        $success = Build-Agent -IsRelease:$Release -IncrementVersion:$IncrementVersion
        if ($success) {
            $success = Build-Server -IsRelease:$Release -IncrementVersion:$IncrementVersion
        }
    }
    'test' {
        # Alias for test-storage
        $success = Test-Storage
    }
    'test-storage' {
        $success = Test-Storage
    }
    'test-all' {
        $success = Test-All
    }
    'all' {
        # Build agent and run tests
        $success = Build-Agent -IsRelease:$Release -IncrementVersion:$IncrementVersion
    }
}

Write-BuildLog "=== Build Complete ===" "INFO"
Write-BuildLog "Log file: $LogFile" "INFO"

if ($success) {
    $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
    Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorBlue}[INFO]${ColorReset} ${ColorGreen}SUCCESS:${ColorReset} Build script completed with exit code 0"
    Add-Content -Path $script:LogFile -Value "[$timestamp] [INFO] SUCCESS: Build script completed with exit code 0"
    exit 0
} else {
    $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
    Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorRed}[ERROR]${ColorReset} ${ColorRed}FAIL:${ColorReset} Build script failed with exit code 1"
    Add-Content -Path $script:LogFile -Value "[$timestamp] [ERROR] FAIL: Build script failed with exit code 1"
    exit 1
}
