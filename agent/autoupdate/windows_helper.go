package autoupdate

import (
	"fmt"
	"strings"
	"time"
)

const (
	windowsHelperParentExitTimeout = 2 * time.Minute
	windowsHelperServiceTimeout    = 2 * time.Minute
	windowsHelperMSITimeout        = 30 * time.Minute
	windowsHelperResultFile        = "helper_result.json"
)

type helperOutcome struct {
	RunID     string    `json:"run_id"`
	Target    string    `json:"target_version"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"error_code,omitempty"`
	Message   string    `json:"message,omitempty"`
	ExitCode  int       `json:"exit_code,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func windowsHelperScript(descriptor InstallationDescriptor, serviceName string, isService, useMSI bool, parentPID int, resultPath, logPath string) string {
	target := descriptor.Authorization.Target
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$RunID = %s
$TargetVersion = %s
$ServiceName = %s
$ParentPID = %d
$ServiceMode = $%t
$MSIMode = $%t
$StagingPath = %s
$BinaryPath = %s
$BackupPath = %s
$ResultPath = %s
$MSILogPath = %s
$ExpectedLength = %dL
$ExpectedSHA256 = %s
$PreviousLength = %dL
$PreviousSHA256 = %s
$ParentExitTimeoutSeconds = %d
$ServiceTimeoutSeconds = %d
$MSITimeoutSeconds = %d
$ServiceWasRunning = $false
$BinaryChanged = $false

function Write-Outcome([string]$Status, [string]$ErrorCode, [string]$Message, [int]$ExitCode) {
    $record = [ordered]@{
        run_id = $RunID
        target_version = $TargetVersion
        status = $Status
        error_code = $ErrorCode
        message = $Message
        exit_code = $ExitCode
        updated_at = [DateTime]::UtcNow.ToString('o')
    }
    $tempPath = "$ResultPath.$PID.tmp"
    try {
        $json = ConvertTo-Json -InputObject $record -Compress
        [IO.File]::WriteAllText($tempPath, $json, [Text.UTF8Encoding]::new($false))
        if ([IO.File]::Exists($ResultPath)) {
            [IO.File]::Replace($tempPath, $ResultPath, $null)
        } else {
            [IO.File]::Move($tempPath, $ResultPath)
        }
    } finally {
        if ([IO.File]::Exists($tempPath)) { [IO.File]::Delete($tempPath) }
    }
}

function Test-Artifact([string]$Path, [long]$Length, [string]$SHA256) {
    if (-not [IO.File]::Exists($Path)) { throw "Required artifact is missing: $Path" }
    $file = [IO.File]::OpenRead($Path)
    $hash = [Security.Cryptography.SHA256]::Create()
    try {
        if ($file.Length -ne $Length) { throw "Artifact length mismatch: $Path" }
        $actual = [BitConverter]::ToString($hash.ComputeHash($file)).Replace('-', '')
        if ($actual -ine $SHA256) { throw "Artifact SHA-256 mismatch: $Path" }
    } finally {
        $file.Dispose()
        $hash.Dispose()
    }
}

function Wait-Service([string]$Status) {
    $service = Get-Service -Name $ServiceName -ErrorAction Stop
    $service.WaitForStatus($Status, [TimeSpan]::FromSeconds($ServiceTimeoutSeconds))
}

function Start-PreviousService {
    if ($ServiceMode -and $ServiceWasRunning) {
        Start-Service -Name $ServiceName -ErrorAction Stop
        Wait-Service 'Running'
    }
}

function Restore-PreviousBinary {
    Test-Artifact $BackupPath $PreviousLength $PreviousSHA256
    $rollbackPath = Join-Path ([IO.Path]::GetDirectoryName($BinaryPath)) ".printmaster-rollback-$RunID.tmp"
    try {
        [IO.File]::Copy($BackupPath, $rollbackPath, $true)
        Test-Artifact $rollbackPath $PreviousLength $PreviousSHA256
        [IO.File]::Replace($rollbackPath, $BinaryPath, $null)
    } finally {
        if ([IO.File]::Exists($rollbackPath)) { [IO.File]::Delete($rollbackPath) }
    }
}

function Install-BinaryAtomically {
    Test-Artifact $StagingPath $ExpectedLength $ExpectedSHA256
    Test-Artifact $BackupPath $PreviousLength $PreviousSHA256
    $tempPath = Join-Path ([IO.Path]::GetDirectoryName($BinaryPath)) ".printmaster-update-$RunID.tmp"
    try {
        [IO.File]::Copy($StagingPath, $tempPath, $true)
        Test-Artifact $tempPath $ExpectedLength $ExpectedSHA256
        [IO.File]::Replace($tempPath, $BinaryPath, $null)
    } finally {
        if ([IO.File]::Exists($tempPath)) { [IO.File]::Delete($tempPath) }
    }
}

function Stop-PreviousService {
    if (-not $ServiceMode) { return }
    $service = Get-Service -Name $ServiceName -ErrorAction Stop
    $script:ServiceWasRunning = $service.Status -eq 'Running'
    if ($ServiceWasRunning) {
        Stop-Service -Name $ServiceName -ErrorAction Stop
        Wait-Service 'Stopped'
    }
}

try {
    Write-Outcome 'running' '' 'Helper started; waiting for the Agent to exit.' 0
    $parentDeadline = [DateTime]::UtcNow.AddSeconds($ParentExitTimeoutSeconds)
    while (Get-Process -Id $ParentPID -ErrorAction SilentlyContinue) {
        if ([DateTime]::UtcNow -ge $parentDeadline) {
            Write-Outcome 'timed_out' 'AGENT_EXIT_TIMEOUT' 'The Agent did not exit before the helper deadline; no installation was attempted.' 124
            exit 124
        }
        Start-Sleep -Milliseconds 250
    }

    if ($MSIMode) {
        Stop-PreviousService
        Test-Artifact $StagingPath $ExpectedLength $ExpectedSHA256
        Test-Artifact $BackupPath $PreviousLength $PreviousSHA256
        $arguments = '/i "' + $StagingPath + '" /qn /norestart /l*v "' + $MSILogPath + '"'
        $installer = Start-Process -FilePath 'msiexec.exe' -ArgumentList $arguments -PassThru -ErrorAction Stop
        if (-not $installer.WaitForExit($MSITimeoutSeconds * 1000)) {
            Write-Outcome 'timed_out' 'MSI_TIMEOUT' 'The MSI exceeded the bounded wait. It was not killed; inspect the Windows Installer log and service before recovery.' 124
            exit 124
        }
        $installerExit = $installer.ExitCode
        if ($installerExit -eq 0 -or $installerExit -eq 3010) {
            $message = 'MSI installation completed.'
            if ($installerExit -eq 3010) { $message = 'MSI installation completed; a reboot is required.' }
            Write-Outcome 'succeeded' '' $message $installerExit
            exit 0
        }
        $message = "MSI installation failed with exit code $installerExit."
        try { Start-PreviousService } catch { $message += " Previous service restart failed: $($_.Exception.Message)" }
        Write-Outcome 'failed' 'MSI_INSTALL_FAILED' $message $installerExit
        exit $installerExit
    }

    Stop-PreviousService
    Install-BinaryAtomically
    $script:BinaryChanged = $true
    if ($ServiceMode) {
        if ($ServiceWasRunning) {
            Start-Service -Name $ServiceName -ErrorAction Stop
            try {
                Wait-Service 'Running'
            } catch {
                Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
                Wait-Service 'Stopped'
                Restore-PreviousBinary
                $script:BinaryChanged = $false
                throw "Updated service failed to start; the previous binary was restored."
            }
        }
    } else {
        Start-Process -FilePath $BinaryPath -ErrorAction Stop | Out-Null
    }
    Write-Outcome 'succeeded' '' 'The authorized binary was atomically installed and the start request completed.' 0
    exit 0
} catch {
    $message = $_.Exception.Message
    if ($BinaryChanged) {
        try {
            if ($ServiceMode) {
                Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
                Wait-Service 'Stopped'
            }
            Restore-PreviousBinary
            $script:BinaryChanged = $false
        } catch {
            $message += " Previous binary recovery failed: $($_.Exception.Message)"
        }
    }
    try { Start-PreviousService } catch { $message += " Previous service restart failed: $($_.Exception.Message)" }
    Write-Outcome 'failed' 'HELPER_FAILED' $message 1
    exit 1
}
`, psLiteral(descriptor.RunID), psLiteral(target.Version), psLiteral(serviceName), parentPID, isService, useMSI,
		psLiteral(descriptor.StagingPath), psLiteral(descriptor.BinaryPath), psLiteral(descriptor.BackupPath),
		psLiteral(resultPath), psLiteral(logPath), target.SizeBytes, psLiteral(target.SHA256),
		descriptor.Previous.SizeBytes, psLiteral(descriptor.Previous.SHA256),
		int(windowsHelperParentExitTimeout/time.Second), int(windowsHelperServiceTimeout/time.Second),
		int(windowsHelperMSITimeout/time.Second))
	return script
}

func psLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
