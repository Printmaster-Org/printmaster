package selfupdate

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestManagerTickSkipsWithoutArtifacts(t *testing.T) {
	store := newTestStore(t)
	dataDir := t.TempDir()
	fixed := time.Date(2025, time.November, 27, 12, 0, 0, 0, time.UTC)
	mgr, err := NewManager(Options{
		Store:            store,
		DataDir:          dataDir,
		Enabled:          true,
		CurrentVersion:   "0.9.5",
		Platform:         "windows",
		Arch:             "amd64",
		ApplyLauncher:    &stubLauncher{},
		Clock:            func() time.Time { return fixed },
		CheckEvery:       time.Minute,
		RuntimeSkipCheck: func() string { return "" }, // Bypass CI detection
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := context.Background()
	mgr.tick(ctx)

	runs, err := store.ListSelfUpdateRuns(ctx, 5)
	if err != nil {
		t.Fatalf("ListSelfUpdateRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	run := runs[0]
	if run.Status != storage.SelfUpdateStatusSkipped {
		t.Fatalf("expected skipped status, got %s", run.Status)
	}
	if got := run.Metadata["reason"]; got != "no-artifacts" {
		t.Fatalf("expected reason 'no-artifacts', got %#v", got)
	}
}

func TestManagerTickSkipsInContainer(t *testing.T) {
	store := newTestStore(t)
	dataDir := t.TempDir()
	fixed := time.Date(2025, time.November, 27, 12, 0, 0, 0, time.UTC)
	t.Setenv("CONTAINER", "docker")
	mgr, err := NewManager(Options{
		Store:          store,
		DataDir:        dataDir,
		Enabled:        true,
		CurrentVersion: "0.9.5",
		Platform:       "windows",
		Arch:           "amd64",
		BinaryPath:     createDummyBinary(t),
		ApplyLauncher:  &stubLauncher{},
		Clock:          func() time.Time { return fixed },
		CheckEvery:     time.Minute,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.tick(context.Background())
	runs, err := store.ListSelfUpdateRuns(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListSelfUpdateRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != storage.SelfUpdateStatusSkipped {
		t.Fatalf("expected skipped status, got %s", runs[0].Status)
	}
	if got := runs[0].Metadata["reason"]; got != "container environment detected" {
		t.Fatalf("expected container skip reason, got %#v", got)
	}
}

func TestManagerTickSelectsNewerCandidate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	createArtifact(t, store, ctx, "server", "0.9.6", "stable", "windows", "amd64")
	binaryPath := createDummyBinary(t)

	dataDir := t.TempDir()
	fixed := time.Date(2025, time.November, 27, 13, 0, 0, 0, time.UTC)
	launcher := &stubLauncher{meta: map[string]any{"helper_pid": 1337}}
	mgr, err := NewManager(Options{
		Store:            store,
		DataDir:          dataDir,
		Enabled:          true,
		CurrentVersion:   "0.9.5",
		Platform:         "windows",
		Arch:             "amd64",
		BinaryPath:       binaryPath,
		ApplyLauncher:    launcher,
		Clock:            func() time.Time { return fixed },
		CheckEvery:       time.Minute,
		RuntimeSkipCheck: func() string { return "" }, // Bypass CI detection
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	mgr.tick(ctx)

	runs, err := store.ListSelfUpdateRuns(ctx, 5)
	if err != nil {
		t.Fatalf("ListSelfUpdateRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	run := runs[0]
	if run.Status != storage.SelfUpdateStatusApplying {
		t.Fatalf("expected applying status, got %s", run.Status)
	}
	if run.TargetVersion != "0.9.6" {
		t.Fatalf("expected target version 0.9.6, got %s", run.TargetVersion)
	}
	if run.ReleaseArtifactID == 0 {
		t.Fatalf("expected release artifact id to be recorded")
	}
	stagePath, ok := run.Metadata["stage_path"].(string)
	if !ok || stagePath == "" {
		t.Fatalf("expected stage_path metadata, got %#v", run.Metadata["stage_path"])
	}
	if _, err := os.Stat(stagePath); err != nil {
		t.Fatalf("expected staged file to exist: %v", err)
	}
	backupPath, ok := run.Metadata["backup_path"].(string)
	if !ok || backupPath == "" {
		t.Fatalf("expected backup_path metadata, got %#v", run.Metadata["backup_path"])
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected backup file to exist: %v", err)
	}
	if launcher.inst == nil {
		t.Fatalf("expected launcher instruction to be recorded")
	}
	if launcher.inst.RunID != run.ID {
		t.Fatalf("expected instruction run id %d, got %d", run.ID, launcher.inst.RunID)
	}
}

func TestManagerApplyLauncherFailure(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	createArtifact(t, store, ctx, "server", "0.9.6", "stable", "windows", "amd64")
	binaryPath := createDummyBinary(t)
	dataDir := t.TempDir()
	launcher := &stubLauncher{err: errors.New("boom")}
	mgr, err := NewManager(Options{
		Store:            store,
		DataDir:          dataDir,
		Enabled:          true,
		CurrentVersion:   "0.9.5",
		Platform:         "windows",
		Arch:             "amd64",
		BinaryPath:       binaryPath,
		ApplyLauncher:    launcher,
		Clock:            func() time.Time { return time.Date(2025, time.November, 27, 15, 0, 0, 0, time.UTC) },
		CheckEvery:       time.Minute,
		RuntimeSkipCheck: func() string { return "" }, // Bypass CI detection
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.tick(ctx)
	runs, err := store.ListSelfUpdateRuns(ctx, 5)
	if err != nil {
		t.Fatalf("ListSelfUpdateRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != storage.SelfUpdateStatusFailed {
		t.Fatalf("expected failed status, got %s", runs[0].Status)
	}
	if runs[0].ErrorCode != "apply-launch" {
		t.Fatalf("expected apply-launch error code, got %s", runs[0].ErrorCode)
	}
}

func newTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createArtifact(t *testing.T, store storage.Store, ctx context.Context, component, version, channel, platform, arch string) {
	t.Helper()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, fmt.Sprintf("%s-%s.zip", component, version))
	content := []byte("artifact-" + version)
	if err := os.WriteFile(cachePath, content, 0o644); err != nil {
		t.Fatalf("write artifact cache: %v", err)
	}
	sum := sha256.Sum256(content)
	artifact := &storage.ReleaseArtifact{
		Component:    component,
		Version:      version,
		Platform:     platform,
		Arch:         arch,
		Channel:      channel,
		SourceURL:    "https://example.com/" + component + version,
		CachePath:    cachePath,
		SHA256:       fmt.Sprintf("%x", sum[:]),
		SizeBytes:    int64(len(content)),
		ReleaseNotes: "feature",
		PublishedAt:  time.Now().UTC(),
		DownloadedAt: time.Now().UTC(),
	}
	if err := store.UpsertReleaseArtifact(ctx, artifact); err != nil {
		t.Fatalf("UpsertReleaseArtifact: %v", err)
	}
	manifest := &storage.ReleaseManifest{
		Component:       component,
		Version:         version,
		Platform:        platform,
		Arch:            arch,
		Channel:         channel,
		ManifestVersion: "1",
		ManifestJSON:    fmt.Sprintf(`{"version":"%s"}`, version),
		Signature:       "sig",
		SigningKeyID:    "test-key",
		GeneratedAt:     time.Now().UTC(),
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.UpsertReleaseManifest(ctx, manifest); err != nil {
		t.Fatalf("UpsertReleaseManifest: %v", err)
	}
}

func createDummyBinary(t *testing.T) string {
	path := filepath.Join(t.TempDir(), "printmaster-server.exe")
	if err := os.WriteFile(path, []byte("server-binary"), 0o755); err != nil {
		t.Fatalf("write dummy binary: %v", err)
	}
	return path
}

type stubLauncher struct {
	inst *ApplyInstruction
	err  error
	meta map[string]any
}

func (s *stubLauncher) Launch(run *storage.SelfUpdateRun, inst *ApplyInstruction) (map[string]any, error) {
	s.inst = inst
	if s.err != nil {
		return nil, s.err
	}
	return s.meta, nil
}

// TestMatchesArtifactSkipsInstallerPackages ensures the Server never selects
// an MSI (which shares windows/amd64 with the .exe) as its own replacement.
func TestMatchesArtifactSkipsInstallerPackages(t *testing.T) {
	m := &Manager{channel: "stable", platform: "windows", arch: "amd64"}
	for format, want := range map[string]bool{"": true, "binary": true, "msi": false, "deb": false, "bogus": false} {
		artifact := &storage.ReleaseArtifact{Channel: "stable", Platform: "windows", Arch: "amd64", Format: format}
		if got := m.matchesArtifact(artifact); got != want {
			t.Fatalf("format %q: matches=%v want %v", format, got, want)
		}
	}
}
