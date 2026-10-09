package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A release publishes a raw binary and an installer for the same
// platform/arch. Both must be cached and signed independently.
func TestReleaseArtifactFormatsCoexist(t *testing.T) {
	t.Parallel()

	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	for _, art := range []*ReleaseArtifact{
		{Component: "agent", Version: "0.32.0", Platform: "windows", Arch: "amd64", SourceURL: "https://example.com/agent.exe", SHA256: "exe-sha"},
		{Component: "agent", Version: "0.32.0", Platform: "windows", Arch: "amd64", Format: "msi", SourceURL: "https://example.com/agent.msi", SHA256: "msi-sha"},
	} {
		if err := store.UpsertReleaseArtifact(ctx, art); err != nil {
			t.Fatalf("upsert %s: %v", art.SourceURL, err)
		}
	}

	binary, err := store.GetReleaseArtifact(ctx, "agent", "0.32.0", "windows", "amd64", "")
	if err != nil || binary.Format != "binary" || binary.SHA256 != "exe-sha" {
		t.Fatalf("binary artifact = %+v, err=%v", binary, err)
	}
	msi, err := store.GetReleaseArtifact(ctx, "agent", "0.32.0", "windows", "amd64", "msi")
	if err != nil || msi.Format != "msi" || msi.SHA256 != "msi-sha" {
		t.Fatalf("msi artifact = %+v, err=%v", msi, err)
	}

	for _, format := range []string{"binary", "msi"} {
		manifest := &ReleaseManifest{
			Component: "agent", Version: "0.32.0", Platform: "windows", Arch: "amd64", Format: format,
			ManifestVersion: "1.0", ManifestJSON: `{"format":"` + format + `"}`, Signature: "sig-" + format, SigningKeyID: "key",
		}
		if err := store.UpsertReleaseManifest(ctx, manifest); err != nil {
			t.Fatalf("upsert %s manifest: %v", format, err)
		}
	}
	signedMSI, err := store.GetReleaseManifest(ctx, "agent", "0.32.0", "windows", "amd64", "msi")
	if err != nil || signedMSI.Signature != "sig-msi" {
		t.Fatalf("msi manifest = %+v, err=%v", signedMSI, err)
	}
	manifests, err := store.ListReleaseManifests(ctx, "agent", 0)
	if err != nil || len(manifests) != 2 {
		t.Fatalf("expected 2 manifests, got %d (err=%v)", len(manifests), err)
	}

	bad := &ReleaseArtifact{Component: "agent", Version: "0.32.0", Platform: "linux", Arch: "amd64", Format: "tarball", SourceURL: "https://example.com/x"}
	if err := store.UpsertReleaseArtifact(ctx, bad); err == nil {
		t.Fatal("expected unsupported format to be rejected")
	}
	if _, err := store.GetReleaseArtifact(ctx, "agent", "0.32.0", "windows", "amd64", "tarball"); err == nil {
		t.Fatal("expected unsupported format lookup to be rejected")
	}
}

// Databases created before format identity hold one row per platform/arch,
// which may be an installer or OS package cached under the binary key. The
// migration must rebuild the identity and relabel those rows by file type.
func TestSQLiteLegacyReleaseTablesMigrateToFormatIdentity(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	now := time.Now().UTC()
	for _, stmt := range []string{
		`CREATE TABLE release_artifacts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			component TEXT NOT NULL, version TEXT NOT NULL, platform TEXT NOT NULL, arch TEXT NOT NULL,
			channel TEXT NOT NULL DEFAULT 'stable', source_url TEXT NOT NULL, cache_path TEXT, sha256 TEXT,
			size_bytes INTEGER NOT NULL DEFAULT 0, release_notes TEXT, published_at DATETIME, downloaded_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(component, version, platform, arch))`,
		`CREATE TABLE release_manifests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			component TEXT NOT NULL, version TEXT NOT NULL, platform TEXT NOT NULL, arch TEXT NOT NULL,
			channel TEXT NOT NULL DEFAULT 'stable', manifest_version TEXT NOT NULL, manifest_json TEXT NOT NULL,
			signature TEXT NOT NULL, signing_key_id TEXT NOT NULL, generated_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(component, version, platform, arch))`,
	} {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("create legacy table: %v", err)
		}
	}
	legacyRows := []struct{ platform, arch, file string }{
		{"windows", "amd64", "printmaster-agent-v0.31.1-windows-amd64.msi"},
		{"linux", "amd64", "printmaster-agent_0.31.1_amd64.deb"},
		{"linux", "arm64", "printmaster-agent-v0.31.1-linux-arm64"},
	}
	for _, row := range legacyRows {
		if _, err := legacy.Exec(`INSERT INTO release_artifacts (component, version, platform, arch, source_url, cache_path, sha256)
			VALUES ('agent', '0.31.1', ?, ?, ?, ?, 'sha')`, row.platform, row.arch, "https://example.com/"+row.file, "/cache/"+row.file); err != nil {
			t.Fatalf("seed legacy artifact: %v", err)
		}
		if _, err := legacy.Exec(`INSERT INTO release_manifests (component, version, platform, arch, manifest_version, manifest_json, signature, signing_key_id, generated_at)
			VALUES ('agent', '0.31.1', ?, ?, '1.0', '{}', 'sig', 'key', ?)`, row.platform, row.arch, now); err != nil {
			t.Fatalf("seed legacy manifest: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	want := map[string]string{"windows/amd64": "msi", "linux/amd64": "deb", "linux/arm64": "binary"}
	artifacts, err := store.ListReleaseArtifacts(ctx, "agent", 0)
	if err != nil || len(artifacts) != 3 {
		t.Fatalf("expected 3 migrated artifacts, got %d (err=%v)", len(artifacts), err)
	}
	for _, art := range artifacts {
		if got := art.Format; got != want[art.Platform+"/"+art.Arch] {
			t.Errorf("%s/%s format = %q, want %q", art.Platform, art.Arch, got, want[art.Platform+"/"+art.Arch])
		}
	}

	// Manifests signed over a misclassified payload are dropped for re-signing;
	// the genuine binary manifest survives.
	manifests, err := store.ListReleaseManifests(ctx, "agent", 0)
	if err != nil || len(manifests) != 1 || manifests[0].Platform != "linux" || manifests[0].Arch != "arm64" || manifests[0].Format != "binary" {
		t.Fatalf("unexpected manifests after migration: %+v (err=%v)", manifests, err)
	}

	// The rebuilt identity accepts a binary next to the reclassified MSI.
	exe := &ReleaseArtifact{Component: "agent", Version: "0.31.1", Platform: "windows", Arch: "amd64", SourceURL: "https://example.com/agent.exe", SHA256: "exe"}
	if err := store.UpsertReleaseArtifact(ctx, exe); err != nil {
		t.Fatalf("binary upsert after migration: %v", err)
	}
	if msi, err := store.GetReleaseArtifact(ctx, "agent", "0.31.1", "windows", "amd64", "msi"); err != nil || msi.SHA256 != "sha" {
		t.Fatalf("msi row was overwritten: %+v (err=%v)", msi, err)
	}

	// Reopening is a no-op: the migration is idempotent.
	if err := store.Close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}
	reopened, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	again, err := reopened.ListReleaseArtifacts(ctx, "agent", 0)
	if err != nil || len(again) != 4 {
		t.Fatalf("expected 4 artifacts after reopen, got %d (err=%v)", len(again), err)
	}
}
