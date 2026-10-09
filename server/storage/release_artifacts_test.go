package storage

import (
	"context"
	"testing"
	"time"
)

func TestReleaseArtifactsCRUD(t *testing.T) {
	t.Parallel()

	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()
	publishedAt := time.Now().UTC().Add(-2 * time.Hour)
	downloadedAt := time.Now().UTC()

	art := &ReleaseArtifact{
		Component:    "server",
		Version:      "0.9.16",
		Platform:     "windows",
		Arch:         "amd64",
		Channel:      "stable",
		SourceURL:    "https://example.com/server.exe",
		CachePath:    "C:/temp/printmaster-server.exe",
		SHA256:       "deadbeef",
		SizeBytes:    1024,
		ReleaseNotes: "Initial server test release",
		PublishedAt:  publishedAt,
		DownloadedAt: downloadedAt,
	}

	if err := store.UpsertReleaseArtifact(ctx, art); err != nil {
		t.Fatalf("failed to upsert artifact: %v", err)
	}

	fetched, err := store.GetReleaseArtifact(ctx, "server", "0.9.16", "windows", "amd64", "")
	if err != nil {
		t.Fatalf("failed to fetch artifact: %v", err)
	}
	if fetched.CachePath != art.CachePath {
		t.Fatalf("expected cache path %s, got %s", art.CachePath, fetched.CachePath)
	}
	if fetched.ReleaseNotes != art.ReleaseNotes {
		t.Fatalf("release notes mismatch")
	}

	// Update existing artifact metadata
	art.CachePath = "C:/temp/new-location/server.exe"
	art.SHA256 = "feedcafe"
	art.SizeBytes = 2048
	if err := store.UpsertReleaseArtifact(ctx, art); err != nil {
		t.Fatalf("failed to update artifact: %v", err)
	}

	updated, err := store.GetReleaseArtifact(ctx, "server", "0.9.16", "windows", "amd64", "")
	if err != nil {
		t.Fatalf("failed to fetch updated artifact: %v", err)
	}
	if updated.CachePath != art.CachePath {
		t.Fatalf("expected updated cache path %s, got %s", art.CachePath, updated.CachePath)
	}
	if updated.SHA256 != art.SHA256 {
		t.Fatalf("expected sha %s, got %s", art.SHA256, updated.SHA256)
	}

	// Insert a second artifact to validate listing and component filtering
	artAgent := &ReleaseArtifact{
		Component: "agent",
		Version:   "0.9.12",
		Platform:  "linux",
		Arch:      "amd64",
		SourceURL: "https://example.com/agent",
		CachePath: "/tmp/agent",
		SHA256:    "abcd",
		SizeBytes: 512,
	}
	if err := store.UpsertReleaseArtifact(ctx, artAgent); err != nil {
		t.Fatalf("failed to upsert agent artifact: %v", err)
	}

	serverArtifacts, err := store.ListReleaseArtifacts(ctx, "server", 10)
	if err != nil {
		t.Fatalf("failed to list server artifacts: %v", err)
	}
	if len(serverArtifacts) != 1 {
		t.Fatalf("expected 1 server artifact, got %d", len(serverArtifacts))
	}

	allArtifacts, err := store.ListReleaseArtifacts(ctx, "", 0)
	if err != nil {
		t.Fatalf("failed to list all artifacts: %v", err)
	}
	if len(allArtifacts) != 2 {
		t.Fatalf("expected 2 artifacts total, got %d", len(allArtifacts))
	}
}

func TestReleaseArtifactDeleteAndPruning(t *testing.T) {
	t.Parallel()

	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	baseTime := time.Now().UTC()

	// Create multiple versions of artifacts to test pruning
	versions := []struct {
		version     string
		publishedAt time.Time
	}{
		{"1.0.0", baseTime.Add(-5 * 24 * time.Hour)}, // oldest
		{"1.1.0", baseTime.Add(-4 * 24 * time.Hour)},
		{"1.2.0", baseTime.Add(-3 * 24 * time.Hour)},
		{"1.3.0", baseTime.Add(-2 * 24 * time.Hour)},
		{"1.4.0", baseTime.Add(-1 * 24 * time.Hour)}, // newest
	}

	for _, v := range versions {
		art := &ReleaseArtifact{
			Component:   "agent",
			Version:     v.version,
			Platform:    "linux",
			Arch:        "amd64",
			SourceURL:   "https://example.com/agent-" + v.version,
			PublishedAt: v.publishedAt,
			SizeBytes:   1024,
		}
		if err := store.UpsertReleaseArtifact(ctx, art); err != nil {
			t.Fatalf("failed to upsert artifact %s: %v", v.version, err)
		}
	}

	// Verify all artifacts were created
	all, err := store.ListReleaseArtifacts(ctx, "agent", 0)
	if err != nil {
		t.Fatalf("failed to list artifacts: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 artifacts, got %d", len(all))
	}

	// Test pruning: keep 3 versions, should return 2 oldest for deletion
	toPrune, err := store.ListArtifactsForPruning(ctx, "agent", 3)
	if err != nil {
		t.Fatalf("ListArtifactsForPruning failed: %v", err)
	}
	if len(toPrune) != 2 {
		t.Fatalf("expected 2 artifacts to prune, got %d", len(toPrune))
	}
	// Verify oldest versions are returned
	prunedVersions := map[string]bool{}
	for _, art := range toPrune {
		prunedVersions[art.Version] = true
	}
	if !prunedVersions["1.0.0"] || !prunedVersions["1.1.0"] {
		t.Fatalf("expected versions 1.0.0 and 1.1.0 to be pruned, got: %v", prunedVersions)
	}

	// Test delete
	if err := store.DeleteReleaseArtifact(ctx, toPrune[0].ID); err != nil {
		t.Fatalf("DeleteReleaseArtifact failed: %v", err)
	}

	// Verify deletion
	remaining, err := store.ListReleaseArtifacts(ctx, "agent", 0)
	if err != nil {
		t.Fatalf("failed to list after delete: %v", err)
	}
	if len(remaining) != 4 {
		t.Fatalf("expected 4 artifacts after delete, got %d", len(remaining))
	}

	// Test pruning with retention disabled (keep 0)
	noPrune, err := store.ListArtifactsForPruning(ctx, "agent", 0)
	if err != nil {
		t.Fatalf("ListArtifactsForPruning with 0 retention failed: %v", err)
	}
	if len(noPrune) != 0 {
		t.Fatalf("expected 0 artifacts when retention is 0, got %d", len(noPrune))
	}
}

func TestSigningKeysAndManifests(t *testing.T) {
	t.Parallel()

	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	key := &SigningKey{
		ID:         "key-test",
		Algorithm:  "ed25519",
		PublicKey:  "pub",
		PrivateKey: "priv",
		Notes:      "initial",
	}
	if err := store.CreateSigningKey(ctx, key); err != nil {
		t.Fatalf("failed to create signing key: %v", err)
	}
	if err := store.SetSigningKeyActive(ctx, key.ID); err != nil {
		t.Fatalf("failed to activate key: %v", err)
	}
	active, err := store.GetActiveSigningKey(ctx)
	if err != nil {
		t.Fatalf("failed to get active key: %v", err)
	}
	if active == nil || active.ID != key.ID {
		t.Fatalf("active key mismatch")
	}
	keys, err := store.ListSigningKeys(ctx, 10)
	if err != nil {
		t.Fatalf("failed to list keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}

	manifest := &ReleaseManifest{
		Component:       "agent",
		Version:         "0.9.1",
		Platform:        "windows",
		Arch:            "amd64",
		Channel:         "stable",
		ManifestVersion: "1.0",
		ManifestJSON:    `{"version":"0.9.1"}`,
		Signature:       "sig",
		SigningKeyID:    key.ID,
		GeneratedAt:     time.Now().UTC(),
	}
	if err := store.UpsertReleaseManifest(ctx, manifest); err != nil {
		t.Fatalf("failed to upsert manifest: %v", err)
	}
	fetched, err := store.GetReleaseManifest(ctx, "agent", "0.9.1", "windows", "amd64", "")
	if err != nil {
		t.Fatalf("failed to fetch manifest: %v", err)
	}
	if fetched.Signature != manifest.Signature {
		t.Fatalf("manifest signature mismatch")
	}
	manifest.Signature = "sig-new"
	manifest.ManifestJSON = `{"version":"0.9.1","channel":"stable"}`
	if err := store.UpsertReleaseManifest(ctx, manifest); err != nil {
		t.Fatalf("failed to update manifest: %v", err)
	}
	list, err := store.ListReleaseManifests(ctx, "", 0)
	if err != nil {
		t.Fatalf("failed to list manifests: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(list))
	}
}
