package releases

import (
	"context"
	"strings"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestManagerEnsureManifestCreatesKey(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr, err := NewManager(store, nil, ManagerOptions{ManifestVersion: "1.0"})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	artifact := &storage.ReleaseArtifact{
		Component:   "agent",
		Version:     "0.9.0",
		Platform:    "windows",
		Arch:        "amd64",
		Channel:     "stable",
		SHA256:      strings.Repeat("ab", 32),
		SizeBytes:   2048,
		SourceURL:   "https://example.com/agent.zip",
		PublishedAt: time.Now().UTC(),
	}
	ctx := context.Background()
	manifest, err := mgr.EnsureManifestForArtifact(ctx, artifact)
	if err != nil {
		t.Fatalf("ensure manifest failed: %v", err)
	}
	if manifest == nil || manifest.Signature == "" {
		t.Fatalf("expected manifest with signature")
	}

	key, err := store.GetActiveSigningKey(ctx)
	if err != nil {
		t.Fatalf("failed to load active key: %v", err)
	}
	if key == nil || key.PublicKey == "" {
		t.Fatalf("expected persisted signing key")
	}
}

func TestManagerRotateAndRegenerate(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr, err := NewManager(store, nil, ManagerOptions{ManifestVersion: "1.0"})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	artifact := &storage.ReleaseArtifact{
		Component: "server",
		Version:   "0.10.1",
		Platform:  "linux",
		Arch:      "amd64",
		Channel:   "stable",
		SHA256:    strings.Repeat("cd", 32),
		SizeBytes: 4096,
		SourceURL: "https://example.com/server.tar.gz",
	}
	ctx := context.Background()
	if _, err := mgr.EnsureManifestForArtifact(ctx, artifact); err != nil {
		t.Fatalf("initial manifest failed: %v", err)
	}
	before, err := store.GetReleaseManifest(ctx, "server", "0.10.1", "linux", "amd64", "")
	if err != nil {
		t.Fatalf("failed to fetch manifest: %v", err)
	}

	if _, err := mgr.RotateSigningKey(ctx, "rotation test"); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	count, err := mgr.RegenerateManifests(ctx)
	if err != nil {
		t.Fatalf("regenerate failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 manifest regenerated, got %d", count)
	}
	after, err := store.GetReleaseManifest(ctx, "server", "0.10.1", "linux", "amd64", "")
	if err != nil {
		t.Fatalf("failed to fetch updated manifest: %v", err)
	}
	if after.Signature == before.Signature {
		t.Fatalf("signature did not change after rotation")
	}

	keys, err := mgr.ListSigningKeys(ctx, 10)
	if err != nil {
		t.Fatalf("list signing keys failed: %v", err)
	}
	if len(keys) < 2 {
		t.Fatalf("expected at least 2 signing keys, got %d", len(keys))
	}
}
