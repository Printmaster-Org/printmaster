package releases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"printmaster/server/storage"
)

func cacheTestArtifact(t *testing.T, artifact *storage.ReleaseArtifact) {
	t.Helper()
	body := []byte("artifact-" + artifact.Version)
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	artifact.CachePath, artifact.SizeBytes, artifact.SHA256 = path, int64(len(body)), hex.EncodeToString(hash[:])
}

func TestManifestRejectsMissingCorruptAndInconsistentArtifacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "release.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(store, nil, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	artifact := &storage.ReleaseArtifact{Component: "agent", Version: "1.0.0", Platform: "linux", Arch: "amd64", Channel: "stable", SourceURL: "https://example.invalid/artifact"}
	cacheTestArtifact(t, artifact)
	if err := store.UpsertReleaseArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureManifestForArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(artifact.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"corrupt", "missing", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			if err := os.WriteFile(artifact.CachePath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "corrupt":
				bad := append([]byte(nil), body...)
				bad[0] ^= 1
				if err := os.WriteFile(artifact.CachePath, bad, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(artifact.CachePath); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				artifact.SourceURL = "https://example.invalid/changed"
				if err := store.UpsertReleaseArtifact(ctx, artifact); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.GetLatestManifest(ctx, "agent", "linux", "amd64", "stable", ""); err == nil {
				t.Fatal("invalid release was advertised")
			}
		})
	}
	if _, err := manager.EnsureManifestForArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetLatestManifest(ctx, "agent", "linux", "amd64", "stable", ""); err != nil {
		t.Fatalf("metadata repair failed: %v", err)
	}
}
