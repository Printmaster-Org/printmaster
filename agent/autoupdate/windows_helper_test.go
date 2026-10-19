package autoupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"printmaster/common/updatetrust"
)

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
