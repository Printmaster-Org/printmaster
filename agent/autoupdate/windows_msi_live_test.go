//go:build windows

package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	runLiveWindowsMSI(t, "", 1620, "")
}

func TestLiveWindowsMSITransaction(t *testing.T) {
	if os.Getenv("PRINTMASTER_LIVE_MSI_TRANSACTION") != "qualify-disposable-test-agent" {
		t.Skip("requires explicit owner approval for MSI transaction qualification")
	}
	exit, err := strconv.Atoi(os.Getenv("PRINTMASTER_LIVE_MSI_EXPECT_EXIT"))
	if err != nil || (exit != 0 && exit != 124 && exit != 1603 && exit != 1618) {
		t.Fatal("expected installer exit must be explicitly 0, 124, 1603 or 1618")
	}
	path := os.Getenv("PRINTMASTER_LIVE_MSI_PACKAGE")
	identity, err := fileIdentity(path)
	if err != nil || !strings.EqualFold(identity.SHA256, os.Getenv("PRINTMASTER_LIVE_MSI_PACKAGE_SHA256")) {
		t.Fatalf("package differs from independently pinned fixture: %v", err)
	}
	installedSHA := os.Getenv("PRINTMASTER_LIVE_MSI_INSTALLED_SHA256")
	if exit == 0 && len(installedSHA) != 64 {
		t.Fatal("successful install requires independently pinned installed executable SHA-256")
	}
	runLiveWindowsMSI(t, path, exit, installedSHA)
}

func runLiveWindowsMSI(t *testing.T, packagePath string, expectedExit int, installedSHA string) {
	t.Helper()
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
	directory, err := os.MkdirTemp(os.Getenv("ProgramData"), "PrintMaster-MSI-Transaction-")
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
	backup, stage := filepath.Join(directory, "previous.exe"), filepath.Join(directory, "candidate.msi")
	if err := os.WriteFile(backup, body, 0o600); err != nil {
		t.Fatal(err)
	}
	packageBytes := []byte("PrintMaster explicitly invalid MSI transaction fixture\r\n")
	if packagePath != "" {
		packageBytes, err = os.ReadFile(packagePath)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(stage, packageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err := fileIdentity(stage)
	if err != nil {
		t.Fatal(err)
	}
	runID := "live-msi-transaction-" + time.Now().UTC().Format("20060102T150405")
	descriptor := InstallationDescriptor{
		RunID: runID, BinaryPath: binary, BackupPath: backup, StagingPath: stage, Previous: previous,
		Authorization: updatetrust.Authorization{Target: updatetrust.Target{
			Version: "fixture-msi-transaction", SHA256: candidate.SHA256, SizeBytes: candidate.SizeBytes,
		}},
	}
	result := filepath.Join(directory, windowsHelperResultFile)
	script := filepath.Join(directory, "helper.ps1")
	helper := windowsHelperScript(descriptor, "PrintMasterAgent", true, true,
		2147483647, result, filepath.Join(directory, "msi.log"))
	if expectedExit == 124 {
		helper = strings.Replace(helper, "$MSITimeoutSeconds = "+strconv.Itoa(int(windowsHelperMSITimeout/time.Second)),
			"$MSITimeoutSeconds = 5", 1)
	}
	if err := os.WriteFile(script, []byte(helper), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureWindowsTree(directory); err != nil {
		t.Fatal(err)
	}
	// No live pending/trust state is fabricated: this exercises the helper
	// transaction, independently of Manager authorization/startup reconciliation.
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
	wantStatus, wantCode := "failed", "MSI_INSTALL_FAILED"
	if expectedExit == 0 {
		wantStatus, wantCode = "succeeded", ""
	} else if expectedExit == 124 {
		wantStatus, wantCode = "timed_out", "MSI_TIMEOUT"
	}
	if (runErr == nil) != (expectedExit == 0) || outcome.RunID != runID || outcome.Status != wantStatus ||
		outcome.ErrorCode != wantCode || outcome.ExitCode != expectedExit {
		t.Fatalf("unexpected installer outcome; preserve evidence: %+v %v\n%s", outcome, runErr, output)
	}
	installed, err := fileIdentity(binary)
	if expectedExit != 0 {
		installedSHA = previous.SHA256
	}
	if err != nil || !strings.EqualFold(installed.SHA256, installedSHA) {
		t.Fatalf("installed executable differs from expected transaction result: %+v %v", installed, err)
	}
	status, err = service.Query()
	if expectedExit == 124 {
		marker := os.Getenv("PRINTMASTER_LIVE_MSI_ACTIVE_MARKER")
		if marker == "" {
			t.Fatal("active-installer fixture requires a pinned marker path")
		}
		if _, err := os.Stat(marker + "-ready"); err != nil {
			t.Fatalf("installer did not reach the bounded hold action: %v", err)
		}
		if _, err := os.Stat(marker + "-finished"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("installer already finished or marker unreadable: %v", err)
		}
		if err != nil || status.State != svc.Stopped {
			t.Fatalf("helper must leave service stopped during the active transaction: %+v %v", status, err)
		}
		if err := previous.verify(backup); err != nil {
			t.Fatal(err)
		}
		t.Logf("PASS: bounded helper timed out while actual MSI remains active; no restart; let installer finish naturally; evidence %s", directory)
		return
	}
	if err != nil || status.State != svc.Running {
		t.Fatalf("previous service was not recovered: %+v %v", status, err)
	}
	if err := previous.verify(backup); err != nil {
		t.Fatal(err)
	}
	if code, isMSI, err := checkMSIInstallation(binary); err != nil || !isMSI || code == "" {
		t.Fatalf("actual MSI component ownership not recognized after completed transaction: %q %v %v", code, isMSI, err)
	}
	t.Logf("PASS: real MSI exit %d, verified executable/backup and running service; evidence %s",
		outcome.ExitCode, directory)
}
