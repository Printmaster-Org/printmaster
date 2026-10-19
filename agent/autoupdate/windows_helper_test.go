package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestWindowsHelperExecutesBinaryHandoff(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell execution requires Windows")
	}
	host, err := exec.LookPath("pwsh.exe")
	if err != nil {
		host = "powershell.exe"
	}
	for _, mode := range []string{"already stopped", "running", "stop pending", "start failure", "stage tampered", "backup tampered"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			previous, candidate := []byte("previous binary"), []byte("authorized candidate")
			identity := func(data []byte) ArtifactIdentity {
				hash := sha256.Sum256(data)
				return ArtifactIdentity{SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(data))}
			}
			oldID, newID := identity(previous), identity(candidate)
			descriptor := InstallationDescriptor{
				RunID: "runtime-test", BinaryPath: filepath.Join(dir, "agent.exe"),
				StagingPath: filepath.Join(dir, "candidate"), BackupPath: filepath.Join(dir, "backup"),
				Previous: oldID,
				Authorization: updatetrust.Authorization{Target: updatetrust.Target{
					Version: "1.1.0", SHA256: newID.SHA256, SizeBytes: newID.SizeBytes,
				}},
			}
			for path, data := range map[string][]byte{
				descriptor.BinaryPath: previous, descriptor.StagingPath: candidate, descriptor.BackupPath: previous,
			} {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stage tampered" || mode == "backup tampered" {
				path := descriptor.StagingPath
				if mode == "backup tampered" {
					path = descriptor.BackupPath
				}
				if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			initialStatus := "Stopped"
			if mode == "running" {
				initialStatus = "Running"
			} else if mode == "stop pending" {
				initialStatus = "StopPending"
			}
			events := filepath.Join(dir, "events")
			// Simulate only service/process control; hashing, file replacement and
			// durable outcome writes execute unchanged in the generated helper.
			prelude := `
$script:Events = ` + psLiteral(events) + `
$script:FailFirstStart = $` + strconv.FormatBool(mode == "start failure") + `
$script:Starts = 0
$script:Service = [pscustomobject]@{ Status = ` + psLiteral(initialStatus) + ` }
$script:Service | Add-Member ScriptMethod Start {
    $script:Starts++
    [IO.File]::AppendAllText($script:Events, 'start' + [Environment]::NewLine)
    if ($script:FailFirstStart -and $script:Starts -eq 1) { throw 'simulated start failure' }
    $this.Status = 'Running'
}
$script:Service | Add-Member ScriptMethod Stop {
    [IO.File]::AppendAllText($script:Events, 'stop' + [Environment]::NewLine)
    $this.Status = 'Stopped'
}
$script:Service | Add-Member ScriptMethod WaitForStatus {
    param($desired, $timeout)
    if ($this.Status -eq 'StopPending' -and $desired -eq 'Stopped') { $this.Status = 'Stopped' }
    if ($this.Status -ne $desired) { throw "Unexpected service status: $($this.Status); wanted $desired" }
}
function Get-Service { param($Name, $ErrorAction) return $script:Service }
function Get-Process { param($Id, $ErrorAction) return $null }
`
			resultPath := filepath.Join(dir, windowsHelperResultFile)
			script := prelude + windowsHelperScript(descriptor, "SimulatedAgent", true, false, 100,
				resultPath, filepath.Join(dir, "unused-msi.log"))
			path := filepath.Join(dir, "helper.ps1")
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			output, runErr := exec.CommandContext(ctx, host, "-NoProfile", "-NonInteractive",
				"-ExecutionPolicy", "Bypass", "-File", path).CombinedOutput()
			data, err := os.ReadFile(resultPath)
			if err != nil {
				t.Fatalf("missing outcome: %v; execution: %v\n%s", err, runErr, output)
			}
			var result helperOutcome
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			wantStatus := "succeeded"
			wantBytes := candidate
			if mode == "start failure" || strings.HasSuffix(mode, "tampered") {
				wantStatus, wantBytes = "failed", previous
			}
			if result.Status != wantStatus || (runErr == nil) != (wantStatus == "succeeded") {
				t.Fatalf("outcome %+v, execution %v\n%s", result, runErr, output)
			}
			installed, err := os.ReadFile(descriptor.BinaryPath)
			if err != nil || string(installed) != string(wantBytes) {
				t.Fatalf("installed bytes %q, want %q: %v", installed, wantBytes, err)
			}
			eventData, err := os.ReadFile(events)
			if err != nil || !strings.Contains(string(eventData), "start\r\n") {
				t.Fatalf("helper never restarted the service: %q %v", eventData, err)
			}
		})
	}
}

func TestWindowsHelperExecutionPolicy(t *testing.T) {
	for _, policy := range []string{"Bypass", "RemoteSigned", "Unrestricted"} {
		if err := validateWindowsHelperPolicy(policy); err != nil {
			t.Errorf("allowed policy %s rejected: %v", policy, err)
		}
	}
	for _, policy := range []string{"AllSigned", "Restricted", "Undefined", ""} {
		if err := validateWindowsHelperPolicy(policy); err == nil {
			t.Errorf("unsupported policy %q accepted", policy)
		}
	}
}

func TestWindowsHelperScriptIsBoundedAndReportsDurableOutcomes(t *testing.T) {
	descriptor := InstallationDescriptor{
		RunID: "run-123",
		Authorization: updatetrust.Authorization{
			Target: updatetrust.Target{Version: "1.2.3", SHA256: strings.Repeat("a", 64), SizeBytes: 42},
		},
		StagingPath: `C:\ProgramData\PrintMaster\agent\autoupdate\staging\agent.msi`,
		BinaryPath:  `C:\Program Files\PrintMaster\Agent\printmaster-agent.exe`,
		BackupPath:  `C:\ProgramData\PrintMaster\agent\autoupdate\backups\agent.exe`,
		Previous:    ArtifactIdentity{SHA256: strings.Repeat("b", 64), SizeBytes: 38},
	}
	script := windowsHelperScript(descriptor, "PrintMasterAgent", true, true, 100, `C:\state\helper_result.json`, `C:\state\msi.log`)
	for _, required := range []string{
		"Write-Outcome 'running'",
		"Write-Outcome 'succeeded'",
		"Write-Outcome 'timed_out'",
		"MSITimeoutSeconds",
		"WaitForExit($MSITimeoutSeconds * 1000)",
		"did not exit before the helper deadline",
		"was not killed",
		"[IO.File]::Replace",
		"Test-Artifact $BackupPath",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("generated helper is missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(script), "taskkill") || strings.Contains(script, ".Kill(") {
		t.Fatal("generated helper forcibly terminates a privileged installer")
	}
	customServiceScript := windowsHelperScript(descriptor, "Custom'Agent", true, true, 100,
		`C:\state\helper_result.json`, `C:\state\msi.log`)
	if !strings.Contains(customServiceScript, "$ServiceName = 'Custom''Agent'") {
		t.Fatal("generated helper did not safely encode the configured service name")
	}
}

func TestWindowsHelperScriptParsesOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell parser test requires Windows")
	}
	descriptor := InstallationDescriptor{
		RunID: "run-123",
		Authorization: updatetrust.Authorization{
			Target: updatetrust.Target{Version: "1.2.3", SHA256: strings.Repeat("a", 64), SizeBytes: 42},
		},
		StagingPath: `C:\ProgramData\PrintMaster\agent\autoupdate\staging\agent.msi`,
		BinaryPath:  `C:\Program Files\PrintMaster\Agent\printmaster-agent.exe`,
		BackupPath:  `C:\ProgramData\PrintMaster\agent\autoupdate\backups\agent.exe`,
		Previous:    ArtifactIdentity{SHA256: strings.Repeat("b", 64), SizeBytes: 38},
	}
	scriptPath := filepath.Join(t.TempDir(), "helper.ps1")
	script := windowsHelperScript(descriptor, "PrintMasterAgent", true, true, 100,
		filepath.Join(t.TempDir(), windowsHelperResultFile), filepath.Join(t.TempDir(), "msi.log"))
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	parser := "$tokens = $null; $errors = $null; [System.Management.Automation.Language.Parser]::ParseFile(" +
		psLiteral(scriptPath) + ", [ref]$tokens, [ref]$errors) | Out-Null; " +
		"if ($errors.Count) { $errors | ForEach-Object { Write-Error $_ }; exit 1 }"
	output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", parser).CombinedOutput()
	if err != nil {
		t.Fatalf("generated helper did not parse: %v\n%s", err, output)
	}
}
