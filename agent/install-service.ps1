# Install PrintMaster Agent as Windows Service
# This script must be run as Administrator

# Check if running as Administrator
$currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
$isAdmin = $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    Write-Host "ERROR: This script must be run as Administrator" -ForegroundColor Red
    Write-Host ""
    Write-Host "Right-click PowerShell and select 'Run as Administrator', then run this script again."
    Write-Host ""
    Read-Host "Press Enter to exit"
    exit 1
}

Write-Host "PrintMaster Agent - Service Installation" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

# Get the directory where this script is located
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$agentExe = Join-Path $scriptDir "printmaster-agent.exe"

# Check if executable exists
if (-not (Test-Path $agentExe)) {
    Write-Host "ERROR: printmaster-agent.exe not found in $scriptDir" -ForegroundColor Red
    Write-Host ""
    Write-Host "Please build the agent first using one of these methods:" -ForegroundColor Yellow
    Write-Host "  Recommended: ..\build.ps1 release  (optimized production build)" -ForegroundColor Cyan
    Write-Host "  Quick:       go build -o printmaster-agent.exe ." -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Build logs will be saved to: ..\logs\build.log" -ForegroundColor Gray
    Write-Host ""
    Read-Host "Press Enter to exit"
    exit 1
}

Write-Host "Found agent executable: $agentExe" -ForegroundColor Green
Write-Host ""

# Check if service already exists
$service = Get-Service -Name "PrintMasterAgent" -ErrorAction SilentlyContinue

if ($service) {
    Write-Host "Service already installed. Current status: $($service.Status)" -ForegroundColor Yellow
    Write-Host ""
    $response = Read-Host "Do you want to reinstall? This will stop and remove the existing service. (Y/N)"
    
    if ($response -eq "Y" -or $response -eq "y") {
        Write-Host ""
        Write-Host "Stopping service..." -ForegroundColor Yellow
        & $agentExe --service stop 2>$null
        Start-Sleep -Seconds 2
        
        Write-Host "Uninstalling existing service..." -ForegroundColor Yellow
        & $agentExe --service uninstall
        
        if ($LASTEXITCODE -ne 0) {
            Write-Host "ERROR: Failed to uninstall existing service" -ForegroundColor Red
            Read-Host "Press Enter to exit"
            exit 1
        }
        
        Start-Sleep -Seconds 2
    } else {
        Write-Host "Installation cancelled." -ForegroundColor Yellow
        Read-Host "Press Enter to exit"
        exit 0
    }
}

# Install the service
Write-Host "Installing PrintMaster Agent service..." -ForegroundColor Cyan
& $agentExe --service install

if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "ERROR: Service installation failed with exit code $LASTEXITCODE" -ForegroundColor Red
    Read-Host "Press Enter to exit"
    exit 1
}

Write-Host ""
Write-Host "Service installed successfully!" -ForegroundColor Green
Write-Host ""

# Ask if user wants to start the service now
$startNow = Read-Host "Do you want to start the service now? (Y/N)"

if ($startNow -eq "Y" -or $startNow -eq "y") {
    Write-Host ""
    Write-Host "Starting PrintMaster Agent service..." -ForegroundColor Cyan
    & $agentExe --service start
    
    if ($LASTEXITCODE -eq 0) {
        Start-Sleep -Seconds 2
        
        # Verify service is running
        $service = Get-Service -Name "PrintMasterAgent" -ErrorAction SilentlyContinue
        
        if ($service -and $service.Status -eq "Running") {
            Write-Host ""
            Write-Host "SUCCESS: Service is running!" -ForegroundColor Green
            Write-Host ""
            Write-Host "Web UI should be available at:" -ForegroundColor Cyan
            Write-Host "  http://localhost:8080" -ForegroundColor White
            Write-Host "  https://localhost:8443" -ForegroundColor White
            Write-Host ""
            Write-Host "Service Details:" -ForegroundColor Cyan
            Write-Host "  Name:        $($service.Name)"
            Write-Host "  Display:     $($service.DisplayName)"
            Write-Host "  Status:      $($service.Status)"
            Write-Host "  Start Type:  $($service.StartType)"
            Write-Host ""
            Write-Host "Agent Data:     C:\ProgramData\PrintMaster\agent\" -ForegroundColor Cyan
            Write-Host "Log Directory:  C:\ProgramData\PrintMaster\agent\logs\" -ForegroundColor Cyan
            Write-Host "Service Binary: $env:ProgramFiles\PrintMaster\Agent\printmaster-agent.exe" -ForegroundColor Cyan
        } else {
            Write-Host ""
            Write-Host "WARNING: Service may not have started properly" -ForegroundColor Yellow
            Write-Host "Check status with: Get-Service PrintMasterAgent"
        }
    } else {
        Write-Host ""
        Write-Host "ERROR: Failed to start service (exit code $LASTEXITCODE)" -ForegroundColor Red
        Write-Host "You can try starting it manually with: Get-Service PrintMasterAgent | Start-Service"
    }
} else {
    Write-Host ""
    Write-Host "Service installed but not started." -ForegroundColor Yellow
    Write-Host "Start it later with: .\printmaster-agent.exe --service start"
    Write-Host "Or use PowerShell: Get-Service PrintMasterAgent | Start-Service"
}

Write-Host ""
Write-Host "Useful Commands:" -ForegroundColor Cyan
Write-Host "  Get-Service PrintMasterAgent                    # Check service status"
Write-Host "  Get-Service PrintMasterAgent | Start-Service    # Start service"
Write-Host "  Get-Service PrintMasterAgent | Stop-Service     # Stop service"
Write-Host "  Get-Service PrintMasterAgent | Restart-Service  # Restart service"
Write-Host ""

Read-Host "Press Enter to exit"
