package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

func preparedInstallation(t *testing.T) (*Manager, InstallationDescriptor) {
	t.Helper()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	if err := m.beginOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(m.stateDir, stagingDirName, "new-agent")
	backup := filepath.Join(m.stateDir, backupDirName, "previous-agent")
	if err := os.WriteFile(stage, []byte("new binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.backupBinary(backup); err != nil {
		t.Fatal(err)
	}
	manifest := manifestForFile(t, "1.1.0", "stable", stage)
	descriptor, err := m.installationDescriptor(context.Background(), manifest, stage, backup)
	if err != nil {
		t.Fatal(err)
	}
	return m, descriptor
}

func TestInstallationDescriptorRejectsSubstitutionBeforeApply(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"run", "from", "stage path", "destination", "backup path", "channel", "platform",
		"hash", "length", "sequence", "downgrade", "stage bytes", "backup bytes", "installed bytes", "revoked trust", "tightened policy"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			m, descriptor := preparedInstallation(t)
			if err := m.validateInstallation(context.Background(), descriptor); err != nil {
				t.Fatalf("valid descriptor: %v", err)
			}
			write := func(path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("substitution"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch field {
			case "run":
				descriptor.RunID = "other-run"
			case "from":
				descriptor.FromVersion = "0.9.0"
			case "stage path":
				descriptor.StagingPath = m.binaryPath
			case "destination":
				descriptor.BinaryPath = descriptor.BackupPath
			case "backup path":
				descriptor.BackupPath = descriptor.StagingPath
			case "channel":
				descriptor.Authorization.Target.Channel = "dev"
			case "platform":
				descriptor.Authorization.Target.Platform = "other"
			case "hash":
				descriptor.Authorization.Target.SHA256 = strings.Repeat("0", 64)
			case "length":
				descriptor.Authorization.Target.SizeBytes++
			case "sequence":
				descriptor.Authorization.ReleaseSequence++
			case "downgrade":
				descriptor.Authorization.Target.Version = "0.9.0"
			case "stage bytes":
				write(descriptor.StagingPath)
			case "backup bytes":
				write(descriptor.BackupPath)
			case "installed bytes":
				write(m.binaryPath)
			case "revoked trust":
				m.publisherVerifier = verifierFunc(func(context.Context, updatetrust.Target) error { return errors.New("revoked") })
			case "tightened policy":
				m.policyProvider = &mockPolicyProvider{enabled: true, spec: updatepolicy.PolicySpec{TargetVersion: "1.0.0"}}
			}
			before, err := os.ReadFile(m.binaryPath)
			if err != nil {
				t.Fatal(err)
			}
			launched := false
			m.launchHelperFn = func(string) error { launched = true; return nil }
			if err := m.applyUpdate(context.Background(), descriptor); !errors.Is(err, errInstallationNotStarted) {
				t.Fatalf("substituted descriptor reached apply: %v", err)
			}
			after, err := os.ReadFile(m.binaryPath)
			if err != nil || string(after) != string(before) || launched {
				t.Fatalf("authorization failure changed installation: %q %q %v", before, after, err)
			}
		})
	}
}

func TestFailedInstallRecoveryOnlyRestoresRecordedPreviousArtifact(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "tampered backup", "wrong run", "not failed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			m, descriptor := preparedInstallation(t)
			if err := os.WriteFile(m.binaryPath, []byte("failed new installation"), 0o600); err != nil {
				t.Fatal(err)
			}
			if mode != "not failed" {
				m.setStatusWithError(StatusFailed, ErrCodeApplyFailed, "injected apply failure")
			}
			if mode == "tampered backup" {
				if err := os.WriteFile(descriptor.BackupPath, []byte("unrelated older binary"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "wrong run" {
				descriptor.RunID = "other"
			}
			err := m.rollback(descriptor)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("recovery outcome: %v", err)
			}
			body, readErr := os.ReadFile(m.binaryPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := "failed new installation"
			if mode == "valid" {
				want = "old binary"
			}
			if string(body) != want {
				t.Fatalf("restored %q, want %q", body, want)
			}
		})
	}
}

func TestDescriptorPersistsThroughPendingAndStartupValidation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "wrong bytes", "wrong run", "receipt write failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			m, descriptor := preparedInstallation(t)
			if mode == "wrong run" {
				descriptor.RunID = "another-run"
			}
			if err := m.writePendingUpdateState("1.1.0", "binary", &descriptor); err != nil {
				t.Fatal(err)
			}
			if mode != "wrong bytes" {
				if err := copyFile(descriptor.StagingPath, m.binaryPath); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "receipt write failure" {
				if err := os.Mkdir(filepath.Join(m.stateDir, "installation.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			m.currentVersion = "1.1.0"
			updated, from, err := m.ValidatePostUpdate()
			if (err == nil) != (mode == "valid") || !updated || from != "1.0.0" {
				t.Fatalf("startup validation: %v %q %v", updated, from, err)
			}
			if mode != "valid" {
				if m.Progress().Status != string(StatusFailed) {
					t.Fatal("startup validation failure was not durable")
				}
				return
			}
			receipt, err := m.readInstallationReceipt()
			if err != nil || receipt != descriptor {
				t.Fatalf("lost authenticated installation receipt: %+v %v", receipt, err)
			}
		})
	}
}

func TestMSIReinstallRequiresRecordedExactIdentity(t *testing.T) {
	t.Parallel()
	m, descriptor := preparedInstallation(t)
	m.useMSI = true
	target := &descriptor.Authorization.Target
	target.Format, target.Version = "msi", "1.0.0"
	manifest := fixtureManifest(&UpdateManifest{Version: "1.0.0", Format: "msi", SHA256: target.SHA256, SizeBytes: target.SizeBytes})
	if _, err := m.applicationPolicy(context.Background(), manifest, true); !errors.Is(err, ErrApplicationPolicy) {
		t.Fatalf("unrecorded MSI reinstall accepted: %v", err)
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUpdateState(filepath.Join(m.stateDir, "installation.json"), data); err != nil {
		t.Fatal(err)
	}
	if allowed, err := m.applicationPolicy(context.Background(), manifest, true); err != nil || !allowed {
		t.Fatalf("exact recorded MSI reinstall rejected: %v", err)
	}
	manifest.SHA256 = strings.Repeat("0", 64)
	if _, err := m.applicationPolicy(context.Background(), manifest, true); !errors.Is(err, ErrApplicationPolicy) {
		t.Fatalf("different recorded MSI identity accepted: %v", err)
	}
}

func TestFinalStagingFailureNeverHandsOffOrRollsBack(t *testing.T) {
	t.Parallel()
	body := []byte("new binary")
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	file := filepath.Join(t.TempDir(), "official")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := manifestForFile(t, "1.1.0", "stable", file)
	m.client = &forceInstallClient{manifest: manifest, payload: body}
	m.snapshotCallback = func(snapshot updatepolicy.ProgressSnapshot) {
		if snapshot.Status == string(StatusApplying) {
			stage := filepath.Join(m.stateDir, stagingDirName,
				"agent-1.1.0-"+m.platform+"-"+m.arch+m.binaryExtension())
			if err := os.WriteFile(stage, []byte("tampered staging"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	launched := false
	m.launchHelperFn = func(string) error { launched = true; return nil }
	m.restartFn = func() error { t.Fatal("restarted after failed final verification"); return nil }
	if err := m.ForceInstallLatest(context.Background(), "test"); err == nil {
		t.Fatal("tampered staged artifact accepted")
	}
	if body, err := os.ReadFile(m.binaryPath); err != nil || string(body) != "old binary" || launched {
		t.Fatalf("final failure changed installation: %q %v", body, err)
	}
	if m.Progress().Status != string(StatusFailed) {
		t.Fatal("final verification failure was not durable")
	}
}

func TestApplyFailureCanRestorePreviousVersionWithoutDowngradeBypass(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses the Windows helper-launch failure boundary")
	}
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	body := []byte("new binary")
	file := filepath.Join(t.TempDir(), "official")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := manifestForFile(t, "1.1.0", "stable", file)
	m.client = &forceInstallClient{manifest: manifest, payload: body}
	launchErr := errors.New("injected helper-launch failure")
	m.launchHelperFn = func(string) error {
		// Model a partial failed application; recovery must use the original
		// recorded backup, not relax the candidate-selection policy.
		if err := os.WriteFile(m.binaryPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return launchErr
	}
	if err := m.ForceInstallLatest(context.Background(), "test"); !errors.Is(err, launchErr) {
		t.Fatalf("apply failure lost: %v", err)
	}
	if body, err := os.ReadFile(m.binaryPath); err != nil || string(body) != "old binary" {
		t.Fatalf("previous version was not recovered: %q %v", body, err)
	}
	m.currentVersion = "1.1.0"
	m.currentSemver, _ = parseSemverVersion(m.currentVersion)
	manifest.Version = "1.0.0"
	if _, err := m.applicationPolicy(context.Background(), manifest, true); !errors.Is(err, ErrApplicationPolicy) {
		t.Fatalf("recovery accidentally enabled normal downgrades: %v", err)
	}
}

func TestWindowsHelperEnforcesExpectedBytes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("executes the real Windows PowerShell integrity check")
	}
	for _, mode := range []string{"valid", "length", "hash", "missing"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact ' % PATH.bin")
			if err := os.WriteFile(path, []byte("good binary"), 0o600); err != nil {
				t.Fatal(err)
			}
			expected, err := fileIdentity(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "length":
				expected.SizeBytes++
			case "hash":
				expected.SHA256 = strings.Repeat("0", 64)
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			line := strings.Split(helperIntegrityCommand(path, expected), "\r\n")[0]
			args := strings.Fields(line)
			output, err := exec.Command(args[0], args[1:]...).CombinedOutput()
			if (err == nil) != (mode == "valid") {
				t.Fatalf("helper integrity result: %v %s", err, output)
			}
		})
	}
}
