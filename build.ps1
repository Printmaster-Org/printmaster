# PrintMaster Build Script
# Usage: .\build.ps1 [target] [options]
# Targets: agent, server, both, all, clean, test, test-storage, test-all
# Options: -Release, -VerboseBuild, -IncrementVersion

param(
    [Parameter(Position=0)]
    [ValidateSet('agent', 'server', 'both', 'all', 'clean', 'test', 'test-storage', 'test-all')]
    [string]$Target = 'agent',
    
    [Parameter()]
    [switch]$Release,
    
    [Parameter()]
    [switch]$VerboseBuild,
    
    [Parameter()]
    [switch]$IncrementVersion
)

$ErrorActionPreference = 'Continue'
$ProjectRoot = $PSScriptRoot
$LogDir = Join-Path $ProjectRoot "logs"
$LogFile = $null  # Will be set dynamically with version info
$MaxLogFiles = 10

# Track which tests have already passed this session (avoid redundant runs)
# Use environment variables to persist across sub-process invocations (e.g., release.ps1 calling build.ps1 twice)
$script:JSUnitTestsPassed = ($env:PRINTMASTER_JS_TESTS_PASSED -eq '1')
$script:PlaywrightTestsPassed = ($env:PRINTMASTER_PLAYWRIGHT_PASSED -eq '1')
$script:JSSyntaxCheckPassed = ($env:PRINTMASTER_JS_SYNTAX_PASSED -eq '1')

# Ensure logs directory exists
if (-not (Test-Path $LogDir)) {
    New-Item -ItemType Directory -Path $LogDir -Force | Out-Null
}

function Invoke-JSUnitTests {
    # Skip if already passed this session
    if ($script:JSUnitTestsPassed) {
        Write-BuildLog "JS unit tests already passed this session, skipping" "INFO"
        return $true
    }

    Write-BuildLog "Running JavaScript unit tests (jest)..." "INFO"

    if ($env:PRINTMASTER_SKIP_JS_TESTS -eq '1') {
        Write-BuildLog "Skipping JS unit tests because PRINTMASTER_SKIP_JS_TESTS=1" "WARN"
        return $true
    }

    Push-Location $ProjectRoot
    try {
        $cmd = "npm run test:js"
        Write-BuildLog "Executing: $cmd" "INFO"
        $testOutput = & npm run test:js 2>&1
        $testExit = $LASTEXITCODE
        if ($testExit -ne 0) {
            $testOutput | ForEach-Object { Write-BuildLog $_ "ERROR" }
            Write-BuildLog "JS unit tests failed" "ERROR"
            return $false
        }
        $testOutput | ForEach-Object { Write-BuildLog $_ "INFO" }
        Write-BuildLog "JS unit tests passed" "INFO"
        $script:JSUnitTestsPassed = $true
        $env:PRINTMASTER_JS_TESTS_PASSED = '1'
        return $true
    }
    finally { Pop-Location }
}

function Invoke-JSSyntaxCheck {
    # Skip if already passed this session
    if ($script:JSSyntaxCheckPassed) {
        Write-BuildLog "JS syntax check already passed this session, skipping" "INFO"
        return $true
    }

    Write-BuildLog "Running JS syntax check (node --check)" "INFO"

    Push-Location $ProjectRoot
    try {
        $pathsToCheck = @(
            (Join-Path $ProjectRoot "agent\web"),
            (Join-Path $ProjectRoot "server\web"),
            (Join-Path $ProjectRoot "common\web")
        )

        $failed = $false
        foreach ($p in $pathsToCheck) {
            if (-not (Test-Path $p)) { continue }
            Get-ChildItem -Path $p -Recurse -Include *.js -File | ForEach-Object {
                $file = $_.FullName
                Write-BuildLog "Checking JS syntax: $file" "INFO"
                $out = & node --check $file 2>&1
                $exit = $LASTEXITCODE
                if ($exit -ne 0) {
                    $out | ForEach-Object { Write-BuildLog $_ "ERROR" }
                    Write-BuildLog "Syntax error in $file" "ERROR"
                    $failed = $true
                }
            }
        }

        if ($failed) {
            Write-BuildLog "JS syntax check failed" "ERROR"
            return $false
        }

        Write-BuildLog "JS syntax check passed" "INFO"
        $script:JSSyntaxCheckPassed = $true
        $env:PRINTMASTER_JS_SYNTAX_PASSED = '1'
        return $true
    }
    finally { Pop-Location }
}

function Invoke-PlaywrightTests {
    # Skip if already passed this session
    if ($script:PlaywrightTestsPassed) {
        Write-BuildLog "Playwright tests already passed this session, skipping" "INFO"
        return $true
    }

    Write-BuildLog "Running Playwright smoke tests..." "INFO"

    if ($env:PRINTMASTER_SKIP_PLAYWRIGHT -eq '1') {
        Write-BuildLog "Skipping Playwright tests because PRINTMASTER_SKIP_PLAYWRIGHT=1" "WARN"
        return $true
    }

    Push-Location $ProjectRoot
    try {
        # Ensure playwright browsers are installed when running CI locally
        Write-BuildLog "Ensuring Playwright browsers are installed (npx playwright install)" "INFO"
        & npx playwright install > $null 2>&1

        $cmd = "npm run test:playwright"
        Write-BuildLog "Executing: $cmd" "INFO"
        
        # Set UTF-8 encoding for proper Playwright Unicode output (checkmarks, arrows)
        $prevOutputEncoding = [Console]::OutputEncoding
        [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
        
        # Stream output live instead of capturing all at once
        $testExit = 0
        & npm run test:playwright 2>&1 | ForEach-Object {
            Write-BuildLog $_ "INFO"
        }
        $testExit = $LASTEXITCODE
        
        # Restore previous encoding
        [Console]::OutputEncoding = $prevOutputEncoding
        
        if ($testExit -ne 0) {
            Write-BuildLog "Playwright smoke tests failed" "ERROR"
            return $false
        }
        Write-BuildLog "Playwright smoke tests passed" "INFO"
        $script:PlaywrightTestsPassed = $true
        $env:PRINTMASTER_PLAYWRIGHT_PASSED = '1'
        return $true
    }
    finally { Pop-Location }
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
    
    # Check for staticcheck
    $staticcheckVersion = & staticcheck -version 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-BuildLog "staticcheck not found - installing..." "WARN"
        Write-BuildLog "Running: go install honnef.co/go/tools/cmd/staticcheck@latest" "INFO"
        & go install honnef.co/go/tools/cmd/staticcheck@latest
        if ($LASTEXITCODE -ne 0) {
            Write-BuildLog "Failed to install staticcheck" "ERROR"
            return $false
        }
        Write-BuildLog "staticcheck installed successfully" "INFO"
    } else {
        Write-BuildLog "Found staticcheck: $staticcheckVersion" "INFO"
    }
    
    # Check git (for version injection)
    $gitVersion = & git --version 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-BuildLog "Git not found - version info will be limited" "WARN"
    } else {
        Write-BuildLog "Found: $gitVersion" "INFO"
    }
    
    return $true
}

function Invoke-Linters {
    param(
        [Parameter(Mandatory=$true)]
        [string]$Component
    )
    
    Write-BuildLog "Running linters for $Component..." "INFO"
    
    $componentDir = Join-Path $ProjectRoot $Component
    Push-Location $componentDir
    
    try {
        # Run go vet (capture output so we can log it in full)
        Write-BuildLog "Running go vet..." "INFO"
        $vetOutput = & go vet ./... 2>&1
        $vetExit = $LASTEXITCODE
        if ($vetOutput) {
            # Write each line of vet output into the build log (INFO level)
            $vetOutput | ForEach-Object { Write-BuildLog $_ "INFO" }
        }
        if ($vetExit -ne 0) {
            $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
            Write-BuildLog "$Component failed go vet checks (see above output)" "ERROR"
            Add-Content -Path $script:LogFile -Value "[$timestamp] [ERROR] FAIL: $Component failed go vet checks"
            return $false
        }
        # Colorize PASS
        $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
        Write-BuildLog "$Component passed go vet checks" "INFO"
        Add-Content -Path $script:LogFile -Value "[$timestamp] [INFO] PASS: $Component passed go vet checks"

        # Run staticcheck (capture output so we can log it in full)
        Write-BuildLog "Running staticcheck..." "INFO"
        $scOutput = & staticcheck ./... 2>&1
        $scExit = $LASTEXITCODE
        if ($scOutput) {
            # Write each line of staticcheck output into the build log (INFO level)
            $scOutput | ForEach-Object { Write-BuildLog $_ "INFO" }
        }
        if ($scExit -ne 0) {
            $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
            Write-BuildLog "$Component failed staticcheck (see above output)" "ERROR"
            Add-Content -Path $script:LogFile -Value "[$timestamp] [ERROR] FAIL: $Component failed staticcheck"
            return $false
        }
        # Colorize PASS
        $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
        Write-BuildLog "$Component passed staticcheck" "INFO"
        Add-Content -Path $script:LogFile -Value "[$timestamp] [INFO] PASS: $Component passed staticcheck"

        # After Go linters pass, run a JS syntax check to catch broken JS bundles early
        if (-not (Invoke-JSSyntaxCheck)) {
            Write-BuildLog "Aborting: JS syntax check failed" "ERROR"
            return $false
        }

        return $true
    }
    finally {
        Pop-Location
    }
}

function Invoke-Tests {
    param(
        [Parameter(Mandatory=$true)]
        [string]$Component
    )
    
    Write-BuildLog "Running tests for $Component..." "INFO"

    # Support skipping tests via environment variable for update/CI convenience.
    if ($env:PRINTMASTER_SKIP_TESTS -eq '1') {
        Write-BuildLog "Skipping tests for $Component because PRINTMASTER_SKIP_TESTS=1" "WARN"
        Add-Content -Path $script:LogFile -Value "[$(Get-Date -Format 'yyyy-MM-ddTHH:mm:sszzz')] [WARN] Skipping tests for $Component due to PRINTMASTER_SKIP_TESTS=1"
        return $true
    }
    
    $componentDir = Join-Path $ProjectRoot $Component
    Push-Location $componentDir
    
    try {
        # Run tests with verbose output
        $testOutput = & go test ./... -v 2>&1
        $testExitCode = $LASTEXITCODE
        
        if ($testExitCode -ne 0) {
            # Show test output on failure
            Write-Host ""
            $testOutput | ForEach-Object { Write-Host $_ -ForegroundColor Red }
            Write-Host ""
            # Colorize FAIL
            $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
            Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorRed}[ERROR]${ColorReset} ${ColorRed}FAIL:${ColorReset} $Component failed tests"
            Add-Content -Path $script:LogFile -Value "[$timestamp] [ERROR] FAIL: $Component failed tests"
            return $false
        }
        
        # Colorize PASS
        $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
        Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorBlue}[INFO]${ColorReset} ${ColorGreen}PASS:${ColorReset} $Component passed tests"
        Add-Content -Path $script:LogFile -Value "[$timestamp] [INFO] PASS: $Component passed tests"
        
        return $true
    }
    finally {
        Pop-Location
    }
}

# Track E2E test state
$script:E2ETestsPassed = $false

function Invoke-E2ETests {
    # Skip if already passed this session
    if ($script:E2ETestsPassed) {
        Write-BuildLog "E2E tests already passed this session, skipping" "INFO"
        return $true
    }

    Write-BuildLog "Running E2E tests..." "INFO"

    # Support skipping tests via environment variable
    if ($env:PRINTMASTER_SKIP_E2E_TESTS -eq '1') {
        Write-BuildLog "Skipping E2E tests because PRINTMASTER_SKIP_E2E_TESTS=1" "WARN"
        return $true
    }

    $testsDir = Join-Path $ProjectRoot "tests"
    Push-Location $testsDir

    try {
        # Run E2E tests (mock-based, no Docker required)
        $testOutput = & go test -v -count=1 ./... 2>&1
        $testExitCode = $LASTEXITCODE

        if ($testExitCode -ne 0) {
            Write-Host ""
            $testOutput | ForEach-Object { Write-Host $_ -ForegroundColor Red }
            Write-Host ""
            $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
            Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorRed}[ERROR]${ColorReset} ${ColorRed}FAIL:${ColorReset} E2E tests failed"
            Add-Content -Path $script:LogFile -Value "[$timestamp] [ERROR] FAIL: E2E tests failed"
            return $false
        }

        $timestamp = Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"
        Write-Host "${ColorDim}${timestamp}${ColorReset} ${ColorBlue}[INFO]${ColorReset} ${ColorGreen}PASS:${ColorReset} E2E tests passed"
        Add-Content -Path $script:LogFile -Value "[$timestamp] [INFO] PASS: E2E tests passed"
        $script:E2ETestsPassed = $true
        return $true
    }
    finally {
        Pop-Location
    }
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

        # Run JavaScript tests (unit + playwright smoke) before compiling Go binary
        if (-not (Invoke-JSUnitTests)) {
            Write-BuildLog "Aborting $Component build due to JS unit test failures" "ERROR"
            return $false
        }
        if (-not (Invoke-PlaywrightTests)) {
            Write-BuildLog "Aborting $Component build due to Playwright smoke test failures" "ERROR"
            return $false
        }
        
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
        $testArgs = @("test", "./storage", "-v")
        if ($VerbosePreference -eq 'Continue') {
            $testArgs += "-count=1"  # Disable cache for verbose
        }
        
        Write-BuildLog "Command: go $($testArgs -join ' ')"
        
        $testOutput = & go @testArgs 2>&1
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
    Write-BuildLog "Running all agent tests..."
    Write-BuildLog "Working directory: $(Get-Location)"
    
    Push-Location (Join-Path $ProjectRoot "agent")
    
    try {
        $testArgs = @("test", "./...", "-v")
        if ($VerbosePreference -eq 'Continue') {
            $testArgs += "-count=1"
        }
        
        Write-BuildLog "Command: go $($testArgs -join ' ')"
        
        $testOutput = & go @testArgs 2>&1
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
        # Run linters for common (shared dependency) first
        if (-not (Invoke-Linters -Component 'common')) {
            Write-BuildLog "Linter checks failed for common" "ERROR"
            exit 1
        }
        # Run tests for common
        if (-not (Invoke-Tests -Component 'common')) {
            Write-BuildLog "Tests failed for common" "ERROR"
            exit 1
        }
        # Run linters before build
        if (-not (Invoke-Linters -Component 'agent')) {
            Write-BuildLog "Linter checks failed for agent" "ERROR"
            exit 1
        }
        # Run tests before build
        if (-not (Invoke-Tests -Component 'agent')) {
            Write-BuildLog "Tests failed for agent" "ERROR"
            exit 1
        }
        $success = Build-Agent -IsRelease:$Release -IncrementVersion:$IncrementVersion
    }
    'server' {
        # Run linters for common (shared dependency) first
        if (-not (Invoke-Linters -Component 'common')) {
            Write-BuildLog "Linter checks failed for common" "ERROR"
            exit 1
        }
        # Run tests for common
        if (-not (Invoke-Tests -Component 'common')) {
            Write-BuildLog "Tests failed for common" "ERROR"
            exit 1
        }
        # Run linters before build
        if (-not (Invoke-Linters -Component 'server')) {
            Write-BuildLog "Linter checks failed for server" "ERROR"
            exit 1
        }
        # Run tests before build
        if (-not (Invoke-Tests -Component 'server')) {
            Write-BuildLog "Tests failed for server" "ERROR"
            exit 1
        }
        $success = Build-Server -IsRelease:$Release -IncrementVersion:$IncrementVersion
    }
    'both' {
        # Run linters for common (shared dependency) first
        if (-not (Invoke-Linters -Component 'common')) {
            Write-BuildLog "Linter checks failed for common" "ERROR"
            exit 1
        }
        # Run tests for common
        if (-not (Invoke-Tests -Component 'common')) {
            Write-BuildLog "Tests failed for common" "ERROR"
            exit 1
        }
        # Run linters for both components
        if (-not (Invoke-Linters -Component 'agent')) {
            Write-BuildLog "Linter checks failed for agent" "ERROR"
            exit 1
        }
        if (-not (Invoke-Linters -Component 'server')) {
            Write-BuildLog "Linter checks failed for server" "ERROR"
            exit 1
        }
        # Run tests for both components
        if (-not (Invoke-Tests -Component 'agent')) {
            Write-BuildLog "Tests failed for agent" "ERROR"
            exit 1
        }
        if (-not (Invoke-Tests -Component 'server')) {
            Write-BuildLog "Tests failed for server" "ERROR"
            exit 1
        }
        # Run E2E tests
        if (-not (Invoke-E2ETests)) {
            Write-BuildLog "E2E tests failed" "ERROR"
            exit 1
        }
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
        if ($success) {
            $success = Test-Storage
        }
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
