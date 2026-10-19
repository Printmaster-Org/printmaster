package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"printmaster/common/updatepolicy"
)

func lifecycleManager(t *testing.T, client UpdateClient, callback func(updatepolicy.ProgressSnapshot)) *Manager {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(binary, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{
		Enabled: true, CurrentVersion: "1.0.0", DataDir: t.TempDir(), BinaryPath: binary,
		ServerClient: client, PublisherVerifier: fixtureVerifier(), MaxRetries: 1, SnapshotCallback: callback,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestUpdateTerminalStatesAllowRetry(t *testing.T) {
	t.Parallel()
	for _, terminal := range []Status{StatusFailed, StatusCancelled, StatusSucceeded, StatusSkipped, StatusRolledBack} {
		t.Run(string(terminal), func(t *testing.T) {
			client := &mockUpdateClient{err: errors.New("manifest unavailable")}
			m := lifecycleManager(t, client, nil)
			m.setStatus(terminal)
			if err := m.CheckNow(context.Background()); !errors.Is(err, client.err) {
				t.Fatalf("retry blocked: %v", err)
			}
			first := m.Progress()
			if first.Status != "failed" || first.Error == "" {
				t.Fatalf("failure not visible: %+v", first)
			}
			if err := m.ForceInstallLatest(context.Background(), "retry"); !errors.Is(err, client.err) {
				t.Fatalf("forced retry blocked: %v", err)
			}
			second := m.Progress()
			if second.RunID == first.RunID || !second.RunStartedAt.After(first.RunStartedAt) {
				t.Fatalf("retry reused run: %+v -> %+v", first, second)
			}
			client.err = nil
			client.manifest = &UpdateManifest{Version: "1.0.0"}
			if err := m.CheckNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := m.Progress(); got.Status != "idle" || got.Error != "" || got.TargetVersion != "1.0.0" {
				t.Fatalf("no-update result: %+v", got)
			}
		})
	}
}

func TestUpdateProgressPersistsAndOrdersCallbacks(t *testing.T) {
	t.Parallel()
	var snapshots []updatepolicy.ProgressSnapshot
	m := lifecycleManager(t, &mockUpdateClient{}, func(p updatepolicy.ProgressSnapshot) { snapshots = append(snapshots, p) })
	if err := m.beginOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(percent int) {
			defer wg.Done()
			m.setStatusWithProgress(StatusDownloading, percent, "Downloading")
		}(i)
	}
	wg.Wait()
	m.setStatusWithError(StatusFailed, ErrCodeDownloadFailed, "interrupted")
	terminal := m.Progress()
	m.setStatusWithProgress(StatusDownloading, 80, "late event")
	if got := m.Progress(); got != terminal {
		t.Fatalf("terminal state changed: %+v", got)
	}
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i].Sequence != snapshots[i-1].Sequence+1 || snapshots[i].Progress < snapshots[i-1].Progress {
			t.Fatalf("unordered callbacks: %+v -> %+v", snapshots[i-1], snapshots[i])
		}
	}
	restored := &Manager{stateDir: m.stateDir, clock: time.Now}
	if err := restored.restoreProgress(); err != nil {
		t.Fatal(err)
	}
	if restored.Progress() != terminal {
		t.Fatalf("journal did not round-trip: %+v", restored.Progress())
	}
}

func TestUpdateStartupRecoveryAndValidation(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"1.0.0", "2.0.0", ""} {
		t.Run(target, func(t *testing.T) {
			m := lifecycleManager(t, &mockUpdateClient{}, nil)
			if err := m.beginOperation(context.Background()); err != nil {
				t.Fatal(err)
			}
			run := m.Progress().RunID
			if target != "" {
				if err := m.writePendingUpdateState(target, "binary"); err != nil {
					t.Fatal(err)
				}
				m.setStatusWithProgress(StatusRestarting, 80, "Restarting")
			}
			restored := &Manager{stateDir: m.stateDir, currentVersion: "1.0.0", clock: time.Now}
			if err := restored.restoreProgress(); err != nil {
				t.Fatal(err)
			}
			if target == "" {
				if got := restored.Progress(); got.Status != "failed" || got.Error != "UPDATE_INTERRUPTED" {
					t.Fatalf("interrupted run was left busy: %+v", got)
				}
				return
			}
			wasUpdated, _, err := restored.ValidatePostUpdate()
			if !wasUpdated || (err != nil) != (target != "1.0.0") {
				t.Fatalf("validation: updated=%v err=%v", wasUpdated, err)
			}
			got := restored.Progress()
			if got.RunID != run || got.TargetVersion != target || got.Status == "restarting" {
				t.Fatalf("lost lifecycle continuity: %+v", got)
			}
			if target == "1.0.0" && (got.Status != "succeeded" || got.Progress != 100) {
				t.Fatalf("not completed: %+v", got)
			}
			if target != "1.0.0" && got.Status != "failed" {
				t.Fatalf("wrong version reported success: %+v", got)
			}
			if _, err := os.Stat(filepath.Join(m.stateDir, pendingUpdateFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending state not cleared: %v", err)
			}
		})
	}
}

func TestUpdateApplyJournalsBeforeHandoffAndRestartFailureIsRetryable(t *testing.T) {
	t.Parallel()
	payload := []byte("new binary")
	hash := sha256.Sum256(payload)
	client := &forceInstallClient{payload: payload, manifest: &UpdateManifest{
		Version: "1.1.0", SHA256: hex.EncodeToString(hash[:]), Channel: "stable", SizeBytes: int64(len(payload)),
	}}
	m := lifecycleManager(t, client, nil)
	restartErr := errors.New("restart request failed")
	m.launchHelperFn = func(string) error {
		data, err := os.ReadFile(filepath.Join(m.stateDir, pendingUpdateFile))
		if err != nil {
			t.Fatalf("helper launched before pending state: %v", err)
		}
		var state PendingUpdateState
		if err := json.Unmarshal(data, &state); err != nil || state.RunID != m.Progress().RunID {
			t.Fatalf("pending run not bound: %s %v", data, err)
		}
		return nil
	}
	m.restartFn = func() error {
		if got := m.Progress(); got.Status != "restarting" || got.Progress >= 100 {
			t.Fatalf("premature completion: %+v", got)
		}
		return restartErr
	}
	if err := m.ForceInstallLatest(context.Background(), "test"); !errors.Is(err, restartErr) {
		t.Fatalf("restart error lost: %v", err)
	}
	if got := m.Progress(); got.Status != "failed" {
		t.Fatalf("restart failure left run busy: %+v", got)
	}
	client.manifest.Version = "1.0.0"
	if err := m.CheckNow(context.Background()); err != nil {
		t.Fatalf("retry after restart failure: %v", err)
	}
}

func TestUpdateJournalFailurePreventsApplying(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	if err := os.Mkdir(filepath.Join(m.stateDir, progressFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckNow(context.Background()); err == nil {
		t.Fatal("operation started without writable journal")
	}
	if err := m.writePendingUpdateState("2.0.0", "binary"); err == nil {
		t.Fatal("apply allowed after journal failure")
	}
}

func TestUpdateCorruptJournalDoesNotDisableManager(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	if err := os.WriteFile(filepath.Join(m.stateDir, progressFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.restoreProgress(); err != nil {
		t.Fatal(err)
	}
	if got := m.Progress(); got.Status != "failed" {
		t.Fatalf("corruption not visible: %+v", got)
	}
	preserved, err := filepath.Glob(filepath.Join(m.stateDir, progressFile+".corrupt-*"))
	if err != nil || len(preserved) != 1 {
		t.Fatalf("corrupt journal not preserved: %v %v", preserved, err)
	}
}

func TestUpdateCompletionJournalFailureReturnsError(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{manifest: &UpdateManifest{Version: "1.0.0"}}, nil)
	m.snapshotCallback = func(p updatepolicy.ProgressSnapshot) {
		if p.Status == "checking" {
			if err := os.Remove(filepath.Join(m.stateDir, progressFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(m.stateDir, progressFile), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := m.CheckNow(context.Background()); err == nil {
		t.Fatal("reported success after completion journal write failed")
	}
	if got := m.Progress(); got.Status != "failed" || got.Error == "" {
		t.Fatalf("journal failure not visible: %+v", got)
	}
}

func TestUpdateCorruptPendingStateIsPreservedAndRetryable(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{manifest: &UpdateManifest{Version: "1.0.0"}}, nil)
	m.setStatus(StatusSucceeded)
	if err := os.WriteFile(filepath.Join(m.stateDir, pendingUpdateFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ValidatePostUpdate(); err == nil {
		t.Fatal("pending corruption not reported")
	}
	if got := m.Progress(); got.Status != "failed" {
		t.Fatalf("pending corruption not visible: %+v", got)
	}
	preserved, err := filepath.Glob(filepath.Join(m.stateDir, pendingUpdateFile+".corrupt-*"))
	if err != nil || len(preserved) != 1 {
		t.Fatalf("corrupt pending state not preserved: %v %v", preserved, err)
	}
	if err := m.CheckNow(context.Background()); err != nil {
		t.Fatalf("pending corruption blocked retry: %v", err)
	}
}

func TestUpdateCancellationInterruptsCheckAndAllowsRetry(t *testing.T) {
	t.Parallel()
	client := &blockedManifestClient{
		mockUpdateClient: mockUpdateClient{manifest: &UpdateManifest{Version: "1.0.0"}},
		entered:          make(chan struct{}, 1), release: make(chan struct{}),
	}
	m := lifecycleManager(t, client, nil)
	if m.Cancel() {
		t.Fatal("idle manager accepted cancellation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.CheckNow(ctx) }()
	select {
	case <-client.entered:
	case <-ctx.Done():
		t.Fatal("check did not start")
	}
	if !m.Cancel() {
		t.Fatal("check cancellation rejected")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error lost: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("cancelled check remained blocked")
	}
	cancelled := m.Progress()
	if cancelled.Status != "cancelled" {
		t.Fatalf("wrong cancellation status: %+v", cancelled)
	}
	close(client.release)
	if err := m.CheckNow(ctx); err != nil {
		t.Fatalf("cancelled check blocked retry: %v", err)
	}
	if got := m.Progress(); got.RunID == cancelled.RunID || got.Status != "idle" {
		t.Fatalf("retry did not finish independently: %+v", got)
	}
}

func TestUpdateLeftoverPendingStateStartsNewValidationRun(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	if err := m.beginOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.writePendingUpdateState("1.0.0", "binary"); err != nil {
		t.Fatal(err)
	}
	m.setStatus(StatusFailed)
	previous := m.Progress()
	if updated, _, err := m.ValidatePostUpdate(); !updated || err != nil {
		t.Fatalf("pending validation: updated=%v err=%v", updated, err)
	}
	if got := m.Progress(); got.RunID == previous.RunID || got.Status != "succeeded" {
		t.Fatalf("terminal run was not reconciled independently: %+v", got)
	}
}
