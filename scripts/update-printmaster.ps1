<#
.SYNOPSIS
  Update PrintMaster agent and server binaries from the latest GitHub release

.DESCRIPTION
  Stops running PrintMaster services/processes, downloads the latest Windows
  release binaries for agent and/or server from the GitHub releases for
  printmaster-org/printmaster, backs up the existing installation in
  C:\ProgramData\PrintMaster, replaces the executables, then runs
  --service update for each component to finish the update.

.PARAMETER Components
  Which components to update: Agent, Server, or Both (default Both).

.PARAMETER RepoOwner
  GitHub repo owner (default: printmaster-org).

.PARAMETER RepoName
  GitHub repo name (default: printmaster).

.PARAMETER DestPath
  Installation path (default: C:\ProgramData\PrintMaster).
.EXAMPLE
  .\update-printmaster.ps1 -Components Both

  Runs update for both agent and server using public GitHub API.
#>

[CmdletBinding(SupportsShouldProcess=$true)]
param(
    [ValidateSet('Agent','Server','Both')]
    [string]$Components = 'Both',

    [string]$RepoOwner = 'printmaster-org',
    [string]$RepoName  = 'printmaster',

    [string]$DestPath = 'C:\ProgramData\PrintMaster'
    ,
    [switch]$SkipBackup = $false
)

function Test-IsAdmin {
    $current = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($current)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Write-Error "This script must be run as Administrator. Right-click and 'Run as Administrator'."
        exit 1
    }
}

function Stop-PrintMaster-Processes {
    param([string]$exePath)

    # Try stopping a service named after the exe base name
    $svcName = [IO.Path]::GetFileNameWithoutExtension($exePath)
    try {
        $svc = Get-Service -Name $svcName -ErrorAction SilentlyContinue
        if ($svc) {
            if ($svc.Status -ne 'Stopped') {
                Write-Host "Stopping service $svcName..."
                Stop-Service -Name $svcName -Force -ErrorAction Stop
                Write-Host "Service $svcName stopped."
            }
        }
    } catch {
        Write-Warning ([string]::Format('Failed to stop service {0}: {1}', $svcName, $_))
    }

    # Kill any process that is running from the same path
    try {
        $procs = Get-Process | Where-Object { 
            ($null -ne $_.Path) -and ([string]::Equals($_.Path, $exePath, [System.StringComparison]::InvariantCultureIgnoreCase))
        }
        foreach ($p in $procs) {
            Write-Host "Killing process $($p.Id) ($($p.ProcessName)) running from $exePath"
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        }
    } catch {
        Write-Warning ([string]::Format('Error while attempting to kill processes for {0}: {1}', $exePath, $_))
    }
}

function Get-Latest-Release {
    param(
        [string]$owner,
        [string]$repo
    )

    # No authentication used: this pulls the latest public release via GitHub's public API
    $headers = @{ 'User-Agent' = 'PrintMaster-Updater' }
    $uri = "https://api.github.com/repos/$owner/$repo/releases/latest"
    try {
        return Invoke-RestMethod -Uri $uri -Headers $headers -ErrorAction Stop
    } catch {
        Write-Error ([string]::Format('Failed to fetch latest release from {0}/{1}: {2}', $owner, $repo, $_))
        return $null
    }
}

# Fetch the most recent release whose tag matches the component prefix (agent-v* or server-v*)
function Get-Latest-Release-For-Component {
    param(
        [string]$owner,
        [string]$repo,
        [string]$component
    )

    $headers = @{ 'User-Agent' = 'PrintMaster-Updater' }
    $prefix = if ($component -ieq 'agent') { 'agent-v' } else { 'server-v' }

    # Try a few pages of releases (most recent first). Stop when we find a matching tag.
    $foundReleases = @()
    for ($page = 1; $page -le 10; $page++) {
        $uri = "https://api.github.com/repos/$owner/$repo/releases?page=$page&per_page=100"
        try {
            $releases = Invoke-RestMethod -Uri $uri -Headers $headers -ErrorAction Stop
        } catch {
            Write-Warning ([string]::Format('Failed to list releases page {0}: {1}', $page, $_))
            break
        }

        if (-not $releases) { break }

        foreach ($r in $releases) {
            if ($r.tag_name -and $r.tag_name.StartsWith($prefix, [System.StringComparison]::InvariantCultureIgnoreCase)) {
                $foundReleases += $r
            }
        }

        # if fewer than requested per_page, we've reached the end
        if ($releases.Count -lt 100) { break }
    }

    if ($foundReleases.Count -eq 0) {
        Write-Warning "No release found with tag prefix '$prefix' for component $component"
        return $null
    }

    # Prefer stable (non-prerelease) releases if available
    $candidates = $foundReleases | Where-Object { -not ($_.prerelease) }
    if (-not $candidates) { $candidates = $matches }

    # Parse semantic version from tag (strip prefix like 'agent-v') and compare numeric versions
    $parsed = @()
    foreach ($r in $candidates) {
        $tag = $r.tag_name
        $verStr = $tag.Substring($prefix.Length)
        if ($verStr.StartsWith('v')) { $verStr = $verStr.Substring(1) }

        # Extract leading numeric version (e.g. 0.8.13)
        $m = [regex]::Match($verStr, '^(\d+(?:\.\d+){0,3})')
        if ($m.Success) {
            try {
                $verObj = [Version]$m.Groups[1].Value
            } catch {
                $verObj = $null
            }
        } else {
            $verObj = $null
        }

        $parsed += [PSCustomObject]@{ release = $r; version = $verObj; tag = $tag }
    }

    if ($parsed | Where-Object { $null -ne $_.version }) {
        $best = ($parsed | Where-Object { $null -ne $_.version } | Sort-Object -Property { $_.version } -Descending | Select-Object -First 1).release
        Write-Host "Selected release $($best.tag_name) for component $component"
        return $best
    }

    # Fallback: pick most recent by tag_name string sort
    $best = ($parsed | Sort-Object -Property tag -Descending | Select-Object -First 1).release
    Write-Host "Selected release $($best.tag_name) for component $component (string fallback)"
    return $best
}

function Find-Asset-For-Component {
    param(
        $release,
        [string]$component # 'agent' or 'server'
    )

    if (-not $release) { return $null }

    # Match files that include the component name and end with .exe (case-insensitive).
    # Prefer assets that contain 'windows' in the name, but fallback to any .exe matching the component.
    $ci = $component.ToLower()
    $assets = $release.assets
        $asset = $assets | Where-Object { $_.name -match "(?i)\b$ci\b" -and $_.name -match '(?i)\.exe$' } | Sort-Object -Property name -Descending | Select-Object -First 1
    if (-not $asset) {
        # Try looser match: component anywhere and .exe
        $asset = $assets | Where-Object { $_.name -match "(?i)$ci" -and $_.name -match '(?i)\.exe$' } | Sort-Object -Property name -Descending | Select-Object -First 1
    }
    return $asset
}

function Save-Asset {
    param(
        $asset,
        [string]$outPath
    )
    if (-not $asset) { throw "No asset provided to Download-Asset" }
    Write-Host "Downloading $($asset.name) to $outPath"
    try {
        Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $outPath -UseBasicParsing -ErrorAction Stop
        return $true
    } catch {
        Write-Error ([string]::Format('Failed to download {0}: {1}', $asset.browser_download_url, $_))
        return $false
    }
}

function Backup-Existing {
    param([string]$dest)
    $timestamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $backup = Join-Path $dest "backup-$timestamp"
    Write-Host "Creating backup of $dest -> $backup"
    try {
        New-Item -ItemType Directory -Path $backup -Force | Out-Null
        Copy-Item -Path (Join-Path $dest '*') -Destination $backup -Recurse -Force -ErrorAction SilentlyContinue
        return $backup
    } catch {
        Write-Warning "Backup failed: $_"
        return $null
    }
}

function Install-And-Update {
    param(
        [string]$component,
        [string]$downloadedFile,
        [string]$destPath,
        [bool]$AlreadyStopped = $false
    )

    $targetExe = Join-Path $destPath "printmaster-$($component.ToLower()).exe"

    # Stop processes/services referencing target only if not already stopped via the component binary
    if (-not $AlreadyStopped) {
        Stop-PrintMaster-Processes -exePath $targetExe
    } else {
        Write-Host "Component appears to be already stopped via binary; skipping service/process stop."
    }

    # Backup unless user asked to skip
    if (-not $SkipBackup) {
        Backup-Existing -dest $destPath | Out-Null
    } else {
        Write-Host "Skipping backup as requested (SkipBackup=true)"
    }

    # Ensure destination exists
    New-Item -Path $destPath -ItemType Directory -Force | Out-Null

    Write-Host "Copying $downloadedFile -> $targetExe"
    try {
        Copy-Item -Path $downloadedFile -Destination $targetExe -Force -ErrorAction Stop
    } catch {
        Write-Error ([string]::Format('Failed to copy {0} to {1}: {2}', $downloadedFile, $targetExe, $_))
        return $false
    }

    # Run --service update
    Write-Host "Running $targetExe --service update --quiet"
    try {
        $proc = Start-Process -FilePath $targetExe -ArgumentList '--service','update','--quiet' -Wait -PassThru -NoNewWindow -ErrorAction Stop
        Write-Host "$component update exit code: $($proc.ExitCode)"
        return $true
    } catch {
        Write-Warning ([string]::Format('Failed to run update for {0}: {1}', $component, $_))
        return $false
    }
}

### Main
Test-IsAdmin

$componentsToRun = @()
switch ($Components) {
    'Both'  { $componentsToRun = @('agent','server') }
    'Agent' { $componentsToRun = @('agent') }
    'Server'{ $componentsToRun = @('server') }
}

Write-Host "Updating components: $($componentsToRun -join ', ')"

$tmp = New-Item -ItemType Directory -Path ([IO.Path]::Combine($env:TEMP, "printmaster-update-$(Get-Random)")) -Force

$allSuccess = $true
foreach ($comp in $componentsToRun) {
    Write-Host "Processing component: $comp"
    # Attempt to stop the running component via its binary first:
    $existingExe = Join-Path $DestPath "printmaster-$comp.exe"
    $stoppedViaBinary = $false
    if (Test-Path $existingExe) {
        try {
            Write-Host "Attempting to stop $comp via existing binary: $existingExe --service stop --quiet"
            $stopProc = Start-Process -FilePath $existingExe -ArgumentList '--service','stop','--quiet' -Wait -PassThru -NoNewWindow -ErrorAction Stop
            Write-Host "$comp stop exit code: $($stopProc.ExitCode)"
            $stoppedViaBinary = $true
        } catch {
            Write-Warning ([string]::Format('Stopping via binary failed for {0}: {1}', $comp, $_))
            $stoppedViaBinary = $false
        }
    } else {
        Write-Host "No existing binary found at $existingExe; will fall back to service/process stop if needed."
    }

    # Find the most recent release specifically for this component (agent-v* or server-v*)
    $releaseForComp = Get-Latest-Release-For-Component -owner $RepoOwner -repo $RepoName -component $comp
    if (-not $releaseForComp) {
        Write-Warning "No release found for component $comp. Skipping."
        $allSuccess = $false
        continue
    }

    $asset = Find-Asset-For-Component -release $releaseForComp -component $comp
    if (-not $asset) {
        Write-Warning "No release asset found for $comp in release $($releaseForComp.tag_name). Skipping."
        $allSuccess = $false
        continue
    }

    $downloadPath = Join-Path $tmp.FullName $asset.name
    $ok = Save-Asset -asset $asset -outPath $downloadPath
    if (-not $ok) { $allSuccess = $false; continue }

    $installed = Install-And-Update -component $comp -downloadedFile $downloadPath -destPath $DestPath -AlreadyStopped:$stoppedViaBinary
    if (-not $installed) { $allSuccess = $false }
}

if ($allSuccess) {
    Write-Host "Update completed successfully for all requested components."
    exit 0
} else {
    Write-Warning "One or more components failed to update. Check the output above for details."
    exit 3
}
