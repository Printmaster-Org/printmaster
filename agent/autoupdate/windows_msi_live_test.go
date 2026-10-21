//go:build windows

package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"printmaster/common/updatetrust"
)

func TestLiveWindowsMSIRejectedPackageRecovery(t *testing.T) {
	if os.Getenv("PRINTMASTER_LIVE_MSI_FAILURE") != "reject-invalid-msi-on-test-agent" {
		t.Skip("requires explicit owner approval for the disposable MSI-installed test Agent")
	}
	binary := filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent", "printmaster-agent.exe")
	previous, err := fileIdentity(binary)
	if err != nil || !strings.EqualFold(previous.SHA256, os.Getenv("PRINTMASTER_LIVE_MSI_BASELINE_SHA256")) {
		t.Fatalf("installed baseline does not match independently recorded consent: %+v %v", previous, err)
	}
	state := filepath.Join(os.Getenv("ProgramData"), "PrintMaster", "agent", updateDirName)
	if _, err := os.Stat(filepath.Join(state, pendingUpdateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing or unreadable pending update blocks rehearsal: %v", err)
	}
	connection, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	service, err := connection.OpenService("PrintMasterAgent")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	config, err := service.Config()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(config.BinaryPathName, `"`+binary+`" --service run`) ||
		!strings.EqualFold(config.ServiceStartName, "LocalSystem") {
		t.Fatal("unexpected service registration; refusing live rehearsal")
	}
	status, err := service.Query()
	if err != nil || status.State != svc.Running {
		t.Fatalf("test Agent must initially be running: %+v %v", status, err)
	}
	directory, err := os.MkdirTemp(os.Getenv("ProgramData"), "PrintMaster-MSI-Rejection-")
	if err != nil {
		t.Fatal(err)
	}
	if err := secureWindowsTree(directory); err != nil {
		t.Fatal(err)
	}
	t.Logf("Retaining protected backup and helper/MSI evidence at %s", directory)
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	backup, stage := filepath.Join(directory, "previous.exe"), filepath.Join(directory, "invalid.msi")
	if err := os.WriteFile(backup, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("PrintMaster explicitly invalid MSI transaction fixture\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err := fileIdentity(stage)
	if err != nil {
		t.Fatal(err)
	}
	runID := "live-msi-rejection-" + time.Now().UTC().Format("20060102T150405")
	descriptor := InstallationDescriptor{
		RunID: runID, BinaryPath: binary, BackupPath: backup, StagingPath: stage, Previous: previous,
		Authorization: updatetrust.Authorization{Target: updatetrust.Target{
			Version: "fixture-invalid-msi", SHA256: candidate.SHA256, SizeBytes: candidate.SizeBytes,
		}},
	}
	result := filepath.Join(directory, windowsHelperResultFile)
	script := filepath.Join(directory, "helper.ps1")
	if err := os.WriteFile(script, []byte(windowsHelperScript(descriptor, "PrintMasterAgent", true, true,
		2147483647, result, filepath.Join(directory, "msi.log"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureWindowsTree(directory); err != nil {
		t.Fatal(err)
	}
	// Only an invalid MSI is submitted. No authorized target or live updater
	// state is fabricated; this qualifies rejection/restart, not a full upgrade.
	host := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	output, runErr := exec.CommandContext(ctx, host, "-NoProfile", "-NonInteractive", "-File", script).CombinedOutput()
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("missing helper outcome; preserve evidence and inspect service: %v %v\n%s", err, runErr, output)
	}
	var outcome helperOutcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		t.Fatal(err)
	}
	if runErr == nil || outcome.RunID != runID || outcome.Status != "failed" ||
		outcome.ErrorCode != "MSI_INSTALL_FAILED" || (outcome.ExitCode != 1619 && outcome.ExitCode != 1620) {
		t.Fatalf("unexpected actual installer rejection; preserve evidence: %+v %v\n%s", outcome, runErr, output)
	}
	if err := previous.verify(binary); err != nil {
		t.Fatalf("installed baseline changed: %v", err)
	}
	status, err = service.Query()
	if err != nil || status.State != svc.Running {
		t.Fatalf("previous service was not recovered: %+v %v", status, err)
	}
	if err := previous.verify(backup); err != nil {
		t.Fatal(err)
	}
	t.Logf("PASS: real MSI rejection %d, unchanged executable and running previous service; evidence %s",
		outcome.ExitCode, directory)
}
