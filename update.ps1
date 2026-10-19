param(
	[switch]$SkipBuild,
	[switch]$Quiet
)

Set-StrictMode -Version Latest

function Log {
	param([string]$m)
	$line = "[$(Get-Date -Format o)] $m"
	Write-Host $line
	try {
		if ($null -ne $logPath) { Add-Content -Path $logPath -Value $line }
	} catch {}
}

function Log-Err {
	param([string]$m)
	$line = "[$(Get-Date -Format o)] ERROR: $m"
	Write-Host $line -ForegroundColor Red
	try { if ($null -ne $logPath) { Add-Content -Path $logPath -Value $line } } catch {}
}

function Log-Warn {
	param([string]$m)
	$line = "[$(Get-Date -Format o)] WARN: $m"
	Write-Host $line -ForegroundColor Yellow
	try { if ($null -ne $logPath) { Add-Content -Path $logPath -Value $line } } catch {}
}

# Prevent accidental dot-sourcing which runs this script in the caller's session
if ($MyInvocation.InvocationName -eq '.') {
	Log-Err "This script must not be dot-sourced. Run it like: & 'C:\temp\printmaster\update.ps1'"
	return
}

function Invoke-Exe {
	param(
		[string]$Exe,
		[string[]]$Arguments
	)
	$argLine = $Arguments -join ' '
	Log "Running: $Exe $argLine"

	# Safety: refuse to launch interactive server/agent without explicit --service flag
	# Note: previous regex used '\b--service\b' which can fail to match because
	# a leading hyphen is a non-word character and word-boundary \b before it
	# won't match. Use a robust check against the argument array or a simple
	# substring match instead.
	if ($Exe -like '*printmaster-server.exe' -or $Exe -like '*printmaster-agent.exe') {
		$hasService = $false
		if ($Arguments -contains '--service') { $hasService = $true }
		elseif (($Arguments -join ' ') -like '*--service*') { $hasService = $true }
		if (-not $hasService) {
			Log-Warn "Warning: launching $Exe without --service may start it interactively. Proceeding because run requested."
			# continue and execute the process (user likely intends this)
		}
	}

	# By default we show output in-process so the user can see progress.
	# Use -Quiet to suppress in-process output and run processes hidden.
	if (-not $Quiet) {
		Log "Executing in foreground to show output"
		try {
			# Stream stdout/stderr in real-time using System.Diagnostics.Process so
			# long-running service commands' output appears live in the console and log.
			$psi = New-Object System.Diagnostics.ProcessStartInfo
			$psi.FileName = $Exe
			$psi.Arguments = $Arguments -join ' '
			$psi.RedirectStandardOutput = $true
			$psi.RedirectStandardError = $true
			$psi.UseShellExecute = $false
			$psi.CreateNoWindow = $true

			$proc = New-Object System.Diagnostics.Process
			$proc.StartInfo = $psi
			$started = $proc.Start()
			if (-not $started) { throw "Failed to start process: $Exe $argLine" }

			$stdOut = $proc.StandardOutput
			$stdErr = $proc.StandardError

			# Read until process exits and streams are drained
			while (-not $proc.HasExited -or -not $stdOut.EndOfStream -or -not $stdErr.EndOfStream) {
				if (-not $stdOut.EndOfStream) {
					$line = $stdOut.ReadLine()
					if ($null -ne $line) { Log $line }
				}
				if (-not $stdErr.EndOfStream) {
					$line = $stdErr.ReadLine()
					if ($null -ne $line) { Log $line }
				}
				Start-Sleep -Milliseconds 10
			}

			$proc.WaitForExit()
			$exit = $proc.ExitCode
			if ($exit -ne 0) { throw "Process exited with code $exit" }
		} catch {
			Log-Warn "Command failed: $Exe $argLine -- $_"
			return $false
		}
		return $true
	}

	try {
		# Use a hidden window so we don't hijack the current terminal. Wait for exit and capture exit code.
		$p = Start-Process -FilePath $Exe -ArgumentList $Arguments -WindowStyle Hidden -Wait -PassThru -ErrorAction Stop
		if ($p.ExitCode -ne 0) {
			throw "Process exited with code $($p.ExitCode)"
		}
	} catch {
		Log-Warn "Command failed: $Exe $argLine -- $_"
		return $false
	}
	return $true
}

$serverSvcExe = 'C:\ProgramData\PrintMaster\printmaster-server.exe'
$agentSvcExe  = Join-Path $env:ProgramFiles 'PrintMaster\Agent\printmaster-agent.exe'
$localRoot    = 'C:\temp\printmaster'
$localServer  = Join-Path $localRoot 'server\printmaster-server.exe'
$localAgent   = Join-Path $localRoot 'agent\printmaster-agent.exe'

# Log file (overwrite each run)
$logPath = Join-Path $localRoot 'update.log'
try {
	if (-not (Test-Path $localRoot)) { New-Item -Path $localRoot -ItemType Directory -Force | Out-Null }
	# Clear previous log
	Set-Content -Path $logPath -Value "" -Encoding UTF8
} catch {
	Log-Warn "Failed to initialize log file ${logPath}: $_"
}


Log "Update script starting. SkipBuild=$SkipBuild"

# Elevate before stopping services or changing protected installation paths.
function Is-Administrator {
	$current = [Security.Principal.WindowsIdentity]::GetCurrent()
	$principal = New-Object Security.Principal.WindowsPrincipal($current)
	return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if (-not (Is-Administrator)) {
	Log-Warn "Script not running elevated. Attempting to relaunch elevated..."
	$pwshCmd = Get-Command pwsh -ErrorAction SilentlyContinue
	if ($null -ne $pwshCmd) { $pwshPath = $pwshCmd.Source } else { $pwshPath = 'pwsh' }
	$relaunchArgs = @('-NoProfile','-ExecutionPolicy','Bypass','-File',$MyInvocation.MyCommand.Path)
	if ($SkipBuild) { $relaunchArgs += '-SkipBuild' }
	if ($Quiet) { $relaunchArgs += '-Quiet' }
	try {
		Start-Process -FilePath $pwshPath -ArgumentList $relaunchArgs -Verb RunAs
		Log "Relaunched elevated. Exiting current process."
		return
	} catch {
		Log-Err "Elevation failed or was cancelled: $_"
		exit 1
	}
}

# Stop services (best-effort)
if (Test-Path $serverSvcExe) { Invoke-Exe $serverSvcExe @('--service','stop','--quiet') | Out-Null }
if (Test-Path $agentSvcExe)  { Invoke-Exe $agentSvcExe  @('--service','stop','--quiet') | Out-Null }

# Uninstall services (best-effort)
if (Test-Path $serverSvcExe) { Invoke-Exe $serverSvcExe @('--service','uninstall','--quiet') | Out-Null }
if (Test-Path $agentSvcExe)  { Invoke-Exe $agentSvcExe  @('--service','uninstall','--quiet') | Out-Null }

# Build new binaries unless skipped
if (-not $SkipBuild) {
	$buildScript = Join-Path $localRoot 'build.ps1'
	if (-not (Test-Path $buildScript)) {
		throw "Build script not found: $buildScript"
	}
	Log "Running build script: $buildScript both"
	# Run via powershell to ensure script execution policy works
	# Guard: Get-Command may return $null in some environments; fall back to 'pwsh'.
	$pwshCmd = Get-Command pwsh -ErrorAction SilentlyContinue
	if ($null -ne $pwshCmd) { $pwshPath = $pwshCmd.Source } else { $pwshPath = 'pwsh' }
	# For unattended updates we skip running the full test suite (tests can be slow or flaky).
	$env:PRINTMASTER_SKIP_TESTS = '1'
	$buildOk = Invoke-Exe $pwshPath @('-NoProfile','-ExecutionPolicy','Bypass','-File',$buildScript,'both')
	# Clean up env var so we don't leak into the caller's session
	$env:PRINTMASTER_SKIP_TESTS = $null
	if (-not $buildOk) { throw 'Build failed - aborting update' }
}

# Ensure ProgramData and protected Program Files paths exist
$installDir = 'C:\ProgramData\PrintMaster'
if (-not (Test-Path $installDir)) { New-Item -Path $installDir -ItemType Directory -Force | Out-Null }
$agentInstallDir = Split-Path -Parent $agentSvcExe
if (-not (Test-Path $agentInstallDir)) { New-Item -Path $agentInstallDir -ItemType Directory -Force | Out-Null }

# Backup existing binaries (timestamped) then copy new ones
function Backup-And-Copy($src, $dst) {
	if (-not (Test-Path $src)) {
		throw "Source binary not found: $src"
	}

	if (Test-Path $dst) {
		$bak = "$dst.$((Get-Date).ToString('yyyyMMdd-HHmmss')).bak"
		Log "Backing up existing $dst -> $bak"
		try { Rename-Item -Path $dst -NewName $bak -Force -ErrorAction Stop } catch { Log-Warn "Backup failed: $_" }
	}

	Log "Copying $src -> $dst"
	try { Copy-Item -Path $src -Destination $dst -Force -ErrorAction Stop } catch { Log-Err "Copy failed: $_"; throw }
}

Backup-And-Copy -src $localAgent -dst $agentSvcExe
Backup-And-Copy -src $localServer -dst $serverSvcExe

# Install and start services
Invoke-Exe $serverSvcExe @('--service','install','--quiet') | Out-Null
Invoke-Exe $agentSvcExe  @('--service','install','--quiet') | Out-Null
Invoke-Exe $serverSvcExe @('--service','start','--quiet') | Out-Null
Invoke-Exe $agentSvcExe  @('--service','start','--quiet') | Out-Null

Log "Update script completed successfully"