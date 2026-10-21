package autoupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestRepositoryPackageManagerDurableHandoff(t *testing.T) {
	for _, mode := range []string{"valid", "submit failure", "awaiting only", "acquisition failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			body := bytes.Repeat([]byte("package"), 4096)
			_, target := packageRequestTestFrame(t, body)
			manifest := &UpdateManifest{Component: target.Component, Channel: target.Channel, Version: target.Version,
				Platform: target.Platform, Arch: target.Arch, Format: target.Format, SHA256: target.SHA256, SizeBytes: target.SizeBytes}
			verifier, _ := runtimeTrustRepository(t, manifest, body, time.Now())
			m := lifecycleManager(t, &mockUpdateClient{}, nil)
			m.platform, m.arch, m.packageManager, m.packageName = "linux", target.Arch, "apt", "printmaster-agent"
			m.publisherVerifier = verifier
			if err := m.beginOperation(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			var workspace string
			commands := func(_ context.Context, _ time.Duration, dir string, _ []string, tool string, _ ...string) ([]byte, error) {
				workspace = dir
				if mode == "acquisition failure" {
					return nil, errors.New("repository unavailable")
				}
				if tool == "apt-get" {
					return nil, os.WriteFile(filepath.Join(dir, "candidate.deb"), body, 0o600)
				}
				if tool != "dpkg-deb" {
					t.Fatalf("unexpected acquisition command %s", tool)
				}
				arch, err := nativePackageArch(target.Arch, target.Format)
				if err != nil {
					t.Fatal(err)
				}
				return []byte("printmaster-agent\n1.1.0\n" + arch + "\n"), nil
			}
			submitted := false
			pendingPath := filepath.Join(m.stateDir, pendingUpdateFile)
			err := m.executeRepositoryPackageWith(ctx, manifest, commands,
				func(_ context.Context, source *os.File, got updatetrust.Target) (RepositoryPackageOutcome, error) {
					submitted = true
					data, err := os.ReadFile(pendingPath)
					if err != nil {
						t.Fatalf("submission preceded durable pending state: %v", err)
					}
					var pending PendingUpdateState
					if err := json.Unmarshal(data, &pending); err != nil || pending.RepositoryTarget == nil ||
						*pending.RepositoryTarget != target || pending.RunID != m.Progress().RunID {
						t.Fatalf("pending target/run differs: %+v %v", pending, err)
					}
					actual, err := packageTargetFromFile(context.Background(), source, got)
					if err != nil || actual != target {
						t.Fatalf("handoff lost exact bytes: %+v %v", actual, err)
					}
					if mode == "submit failure" {
						return RepositoryPackageOutcome{}, errors.New("connection lost after intake")
					}
					status := "startup-confirmed"
					if mode == "awaiting only" {
						status = "awaiting-startup"
					}
					return RepositoryPackageOutcome{Schema: 1, Status: status, RunID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Target: &target}, nil
				})
			if (err == nil) != (mode == "valid") {
				t.Fatalf("handoff result mode=%s err=%v", mode, err)
			}
			if workspace != "" {
				if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("handoff workspace leaked: %v", err)
				}
			}
			_, pendingErr := os.Stat(pendingPath)
			if submitted {
				if pendingErr != nil || m.unresolvedHelperHandoff() == nil {
					t.Fatalf("submitted transaction lost pending/replay blocker: %v", pendingErr)
				}
				if m.Progress().Status == "succeeded" {
					t.Fatal("old Manager claimed success for replacement Agent")
				}
			} else if !errors.Is(pendingErr, os.ErrNotExist) {
				t.Fatalf("rejected acquisition created pending install: %v", pendingErr)
			}
		})
	}
}

func TestPrivilegedRepositoryPackageManagerReconciliation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-written native observation fixture requires root")
	}
	for _, mode := range []string{"valid", "owner-repaired", "wrong target", "wrong pid", "wrong version", "recovery-required", "writable", "symlink", "nonroot"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			journal := testPackageTransaction(t, dir)
			journal.Phase = "startup-confirmed"
			journal.Startup = &RepositoryPackageStartup{PID: os.Getpid(), InvocationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
			pending := PendingUpdateState{RunID: "fixture-run", FromVersion: "1.0.0", TargetVersion: "1.1.0",
				Method: "deb", StartedAt: journal.StartedAt.Add(-time.Second), RepositoryTarget: &journal.Target}
			m := lifecycleManager(t, &mockUpdateClient{}, nil)
			m.currentVersion = "1.1.0"
			if mode == "wrong version" {
				m.currentVersion = "1.0.0"
			}
			if mode == "wrong pid" {
				journal.Startup.PID++
			}
			if mode == "wrong target" {
				target := journal.Target
				target.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
				pending.RepositoryTarget = &target
			}
			if mode == "recovery-required" {
				journal.Phase, journal.Startup = "recovery-required", nil
			}
			if mode == "owner-repaired" {
				journal.Phase, journal.RepairedVersion = "owner-repaired", "1.0.0"
				m.currentVersion = "1.0.0"
			}
			path := filepath.Join(dir, "results", "current.json")
			if err := writeRepositoryPackageObservation(path, *journal); err != nil {
				t.Fatal(err)
			}
			if mode == "writable" {
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "nonroot" {
				if err := os.Chown(path, 1000, 1000); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				link := filepath.Join(filepath.Dir(path), "link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			if err := m.beginOperation(context.Background()); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(pending)
			if err != nil {
				t.Fatal(err)
			}
			pendingPath := filepath.Join(m.stateDir, pendingUpdateFile)
			if err := writeUpdateState(pendingPath, data); err != nil {
				t.Fatal(err)
			}
			_, _, err = m.validateRepositoryPostUpdateFrom(pending, path, os.Getpid())
			if (err == nil) != (mode == "valid" || mode == "owner-repaired") {
				t.Fatalf("unexpected reconciliation result: %s %v", mode, err)
			}
			_, pendingErr := os.Stat(pendingPath)
			if mode == "valid" || mode == "owner-repaired" {
				status := "succeeded"
				if mode == "owner-repaired" {
					status = "failed"
				}
				if m.Progress().Status != status || !errors.Is(pendingErr, os.ErrNotExist) {
					t.Fatalf("valid observation was not reconciled: %+v %v", m.Progress(), pendingErr)
				}
			} else if pendingErr != nil || m.unresolvedHelperHandoff() == nil {
				t.Fatalf("failed observation lost blocker: %v", pendingErr)
			}
		})
	}
}
