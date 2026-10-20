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

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

const (
	liveWindowsConsent = "PRINTMASTER_LIVE_WINDOWS_UPDATE"
	liveWindowsChild   = "PRINTMASTER_LIVE_WINDOWS_CHILD"
	liveWindowsFrom    = "0.32.0.2-dev"
	liveWindowsTo      = "0.32.0.3-dev"
	liveWindowsSHA256  = "e02954cb2508b541772d1bc6b8c06551c9835230b194c7bc8c152c5bcb9c49f4"
)

// This test intentionally replaces the installed test Agent. It is never part
// of unattended CI: an owner must stop the service and explicitly opt in.
func TestLiveWindowsBinaryUpdate(t *testing.T) {
	if os.Getenv(liveWindowsConsent) != "replace-test-agent-with-backup" {
		t.Skip("requires an owner-approved stopped test service, backup and elevated shell")
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
	binary := filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent", "printmaster-agent.exe")
	if !strings.EqualFold(config.BinaryPathName, `"`+binary+`" --service run`) ||
		!strings.EqualFold(config.ServiceStartName, "LocalSystem") {
		t.Fatalf("unexpected service registration: path=%q account=%q", config.BinaryPathName, config.ServiceStartName)
	}
	status, err := service.Query()
	if err != nil || status.State != svc.Stopped {
		t.Fatalf("owner must stop the test service before rehearsal: %+v %v", status, err)
	}
	dataDir := filepath.Join(os.Getenv("ProgramData"), "PrintMaster", "agent")
	stateDir := filepath.Join(dataDir, updateDirName)
	pendingPath := filepath.Join(stateDir, pendingUpdateFile)
	if _, err := os.Stat(pendingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an existing/unreadable pending update blocks rehearsal; preserve it: %v", err)
	}
	baseline, err := fileIdentity(binary)
	if err != nil || baseline.SHA256 != liveWindowsSHA256 {
		t.Fatalf("installed baseline must be the exact approved %s artifact: %+v %v", liveWindowsFrom, baseline, err)
	}
	candidatePath := os.Getenv("PRINTMASTER_LIVE_WINDOWS_CANDIDATE")
	candidate, err := fileIdentity(candidatePath)
	if err != nil || !strings.EqualFold(candidate.SHA256, os.Getenv("PRINTMASTER_LIVE_WINDOWS_CANDIDATE_SHA256")) {
		t.Fatalf("candidate must match the independently checked rehearsal SHA-256: %+v %v", candidate, err)
	}
	versionCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(versionCtx, candidatePath, "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(version), "PrintMaster Agent "+liveWindowsTo+"\r\n") &&
		!strings.HasPrefix(string(version), "PrintMaster Agent "+liveWindowsTo+"\n") {
		t.Fatalf("unexpected candidate version: %s %v", version, err)
	}

	if os.Getenv(liveWindowsChild) == "1" {
		manifest := manifestForFile(t, liveWindowsTo, "dev", candidatePath)
		previous := manifestForFile(t, liveWindowsFrom, "dev", binary)
		previousTarget := updatetrust.Target{
			Component: "agent", Channel: "dev", Version: liveWindowsFrom,
			Platform: "windows", Arch: "amd64", Format: "binary",
			SHA256: previous.SHA256, SizeBytes: previous.SizeBytes,
		}
		body, err := os.ReadFile(candidatePath)
		if err != nil {
			t.Fatal(err)
		}
		verifier, _ := runtimeTrustRepository(t, manifest, body, time.Now(),
			updatetrust.PublishTarget{Target: previousTarget, File: binary})
		manager, err := NewManager(Options{
			DataDir: dataDir, Enabled: true, CurrentVersion: liveWindowsFrom,
			Platform: "windows", Arch: "amd64", Channel: "dev",
			BinaryPath: binary, IsService: true, ServiceName: "PrintMasterAgent",
			PublisherVerifier: verifier,
			ServerClient:      &forceInstallClient{manifest: manifest, payload: body},
		})
		if err != nil {
			t.Fatal(err)
		}
		// The helper waits for this child test process, not the supervisor.
		// Normal child exit releases it only after all authorization has finished.
		manager.restartFn = func() error { return nil }
		if err := manager.ForceInstallLatest(context.Background(), "owner-approved disposable-trust rehearsal"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(pendingPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("PRINTMASTER_LIVE_WINDOWS_RUN_RECORD"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runRecord := filepath.Join(t.TempDir(), "pending-run.json")
	child := exec.Command(executable, "-test.run=^TestLiveWindowsBinaryUpdate$", "-test.v")
	child.Env = append(os.Environ(), liveWindowsChild+"=1", "PRINTMASTER_LIVE_WINDOWS_RUN_RECORD="+runRecord)
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("rehearsal child failed; inspect durable outcomes before restarting manually: %v\n%s", err, output)
	}
	data, err := os.ReadFile(runRecord)
	if err != nil {
		t.Fatal(err)
	}
	var pending PendingUpdateState
	if err := json.Unmarshal(data, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.RunID == "" || pending.TargetVersion != liveWindowsTo {
		t.Fatalf("invalid rehearsal run record: %+v", pending)
	}
	t.Logf("Supervising real helper run %s; disposable keys were not persisted", pending.RunID)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(stateDir, windowsHelperResultFile))
		if errors.Is(err, os.ErrNotExist) {
			time.Sleep(time.Second)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var outcome helperOutcome
		if err := json.Unmarshal(data, &outcome); err != nil {
			t.Fatal(err)
		}
		if outcome.RunID != pending.RunID || outcome.Target != liveWindowsTo {
			time.Sleep(time.Second)
			continue
		}
		if outcome.Status == "failed" || outcome.Status == "timed_out" {
			t.Fatalf("helper failed; preserve state and inspect recovery: %+v", outcome)
		}
		progressData, err := os.ReadFile(filepath.Join(stateDir, progressFile))
		if err != nil {
			t.Fatal(err)
		}
		var progress updatepolicy.ProgressSnapshot
		if err := json.Unmarshal(progressData, &progress); err != nil {
			t.Fatal(err)
		}
		if progress.RunID != pending.RunID {
			t.Fatalf("another operation replaced the rehearsal progress: %+v", progress)
		}
		if progress.Status == string(StatusFailed) {
			t.Fatalf("startup validation failed: %+v", progress)
		}
		if outcome.Status == "succeeded" && progress.Status == string(StatusSucceeded) {
			status, err := service.Query()
			if err != nil || status.State != svc.Running {
				t.Fatalf("helper succeeded but service is not running: %+v %v", status, err)
			}
			if err := candidate.verify(binary); err != nil {
				t.Fatalf("installed candidate identity mismatch: %v", err)
			}
			if _, err := os.Stat(pendingPath); errors.Is(err, os.ErrNotExist) {
				t.Logf("PASS: authorized candidate installed, service running, startup validated run %s", pending.RunID)
				return
			} else if err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("rehearsal deadline expired; preserve helper/pending/progress files and inspect the service before recovery")
}
