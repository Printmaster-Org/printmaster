package autoupdate

import (
	"context"
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

func TestWindowsHelperExecutesMSITransaction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("MSI helper execution requires Windows")
	}
	host := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	for _, tc := range []struct {
		name       string
		exit       int
		status     string
		code       string
		startFails bool
		tamper     bool
		timeout    bool
	}{
		{name: "success", status: "succeeded"},
		{name: "reboot required", exit: 3010, status: "succeeded"},
		{name: "installer rejected package", exit: 1620, status: "failed", code: "MSI_INSTALL_FAILED"},
		{name: "installer busy", exit: 1618, status: "failed", code: "MSI_INSTALL_FAILED"},
		{name: "transaction failed", exit: 1603, status: "failed", code: "MSI_INSTALL_FAILED"},
		{name: "rejection and service restart failed", exit: 1620, status: "failed", code: "MSI_INSTALL_FAILED", startFails: true},
		{name: "service start failed after install", status: "failed", code: "HELPER_FAILED", startFails: true},
		{name: "stage substituted", status: "failed", code: "HELPER_FAILED", tamper: true},
		{name: "installer still active", status: "timed_out", code: "MSI_TIMEOUT", timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binary, stage, backup := filepath.Join(dir, "agent.exe"), filepath.Join(dir, "candidate.msi"), filepath.Join(dir, "backup.exe")
			for path, body := range map[string]string{binary: "previous", backup: "previous", stage: "authorized MSI fixture"} {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			previous, err := fileIdentity(backup)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := fileIdentity(stage)
			if err != nil {
				t.Fatal(err)
			}
			descriptor := InstallationDescriptor{
				RunID: "msi-fixture", BinaryPath: binary, StagingPath: stage, BackupPath: backup, Previous: previous,
				Authorization: updatetrust.Authorization{Target: updatetrust.Target{
					Version: "1.2.3", SHA256: candidate.SHA256, SizeBytes: candidate.SizeBytes,
				}},
			}
			if tc.tamper {
				if err := os.WriteFile(stage, []byte("substituted"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			events := filepath.Join(dir, "events")
			prelude := `
$script:Events = ` + psLiteral(events) + `
$script:Binary = ` + psLiteral(binary) + `
$script:StartFails = $` + strconv.FormatBool(tc.startFails) + `
$script:Service = [pscustomobject]@{ Status = 'Running' }
$script:Service | Add-Member ScriptMethod Stop {
    [IO.File]::AppendAllText($script:Events, "stop` + "`n" + `")
    $this.Status = 'Stopped'
}
$script:Service | Add-Member ScriptMethod Start {
    [IO.File]::AppendAllText($script:Events, "start` + "`n" + `")
    if ($script:StartFails) { throw 'fixture service start failed' }
    $this.Status = 'Running'
}
$script:Service | Add-Member ScriptMethod WaitForStatus {
    param($desired, $timeout)
    if ($this.Status -ne $desired) { throw "Unexpected status $($this.Status)" }
}
function Get-Service { param($Name, $ErrorAction) return $script:Service }
function Get-Process { param($Id, $ErrorAction) return $null }
function Start-Process {
    param($FilePath, $ArgumentList, [switch]$PassThru, $ErrorAction)
    [IO.File]::AppendAllText($script:Events, "install $FilePath $ArgumentList` + "`n" + `")
    $process = [pscustomobject]@{ ExitCode = ` + strconv.Itoa(tc.exit) + ` }
    if ($process.ExitCode -eq 0 -or $process.ExitCode -eq 3010) {
        [IO.File]::WriteAllText($script:Binary, 'MSI-installed candidate')
    }
    $process | Add-Member ScriptMethod WaitForExit { param($timeout) return $` + strconv.FormatBool(!tc.timeout) + ` }
    return $process
}
`
			resultPath := filepath.Join(dir, windowsHelperResultFile)
			script := filepath.Join(dir, "helper.ps1")
			if err := os.WriteFile(script, []byte(prelude+windowsHelperScript(descriptor, "FixtureAgent", true, true, 100,
				resultPath, filepath.Join(dir, "msi.log"))), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			output, runErr := exec.CommandContext(ctx, host, "-NoProfile", "-NonInteractive", "-File", script).CombinedOutput()
			data, err := os.ReadFile(resultPath)
			if err != nil {
				t.Fatalf("missing outcome: %v; execution: %v\n%s", err, runErr, output)
			}
			var outcome helperOutcome
			if err := json.Unmarshal(data, &outcome); err != nil {
				t.Fatal(err)
			}
			if outcome.RunID != descriptor.RunID || outcome.Target != "1.2.3" ||
				outcome.Status != tc.status || outcome.ErrorCode != tc.code ||
				(runErr == nil) != (tc.status == "succeeded") {
				t.Fatalf("outcome %+v; execution %v\n%s", outcome, runErr, output)
			}
			if tc.status == "succeeded" && outcome.ExitCode != tc.exit {
				t.Fatalf("installer exit code lost: %+v", outcome)
			}
			installed, err := os.ReadFile(binary)
			wantInstalled := "previous"
			if !tc.tamper && (tc.exit == 0 || tc.exit == 3010) {
				wantInstalled = "MSI-installed candidate"
			}
			if err != nil || string(installed) != wantInstalled {
				t.Fatalf("MSI helper must never perform raw-binary rollback: got %q want %q: %v", installed, wantInstalled, err)
			}
			eventData, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			eventText := string(eventData)
			if strings.Contains(eventText, "install ") == tc.tamper {
				t.Fatalf("unexpected installer invocation: %s", eventText)
			}
			if tc.timeout {
				if strings.Contains(eventText, "start\n") {
					t.Fatalf("must not restart during active transaction: %s", eventText)
				}
			} else if !strings.Contains(eventText, "start\n") {
				t.Fatalf("service restart not attempted: %s", eventText)
			}
			if tc.exit == 3010 && !strings.Contains(outcome.Message, "reboot is required") {
				t.Fatalf("reboot requirement lost: %+v", outcome)
			}
			if tc.startFails && !strings.Contains(outcome.Message, "fixture service start failed") {
				t.Fatalf("service restart failure lost: %+v", outcome)
			}
		})
	}
}
