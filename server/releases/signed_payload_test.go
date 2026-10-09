package releases

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"printmaster/server/storage"
)

// TestLatestManifestCarriesVerifiableSignedPayload proves Agents receive the
// exact signed bytes and the public key, and that they verify.
func TestLatestManifestCarriesVerifiableSignedPayload(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "releases.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(store, nil, ManagerOptions{ManifestVersion: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	artifact := &storage.ReleaseArtifact{Component: "agent", Version: "1.2.0", Platform: "linux", Arch: "amd64", Channel: "stable",
		SHA256: strings.Repeat("cd", 32), SizeBytes: 42, SourceURL: "https://example.com/agent"}
	if err := store.UpsertReleaseArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureManifestForArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.GetLatestManifest(ctx, "agent", "linux", "amd64", "stable", "")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SigningKeyID == "" || manifest.SigningPublicKey == "" || manifest.SignedPayload == "" {
		t.Fatalf("signing material missing: %+v", manifest)
	}
	pub, _ := base64.StdEncoding.DecodeString(manifest.SigningPublicKey)
	payload, _ := base64.StdEncoding.DecodeString(manifest.SignedPayload)
	sig, _ := base64.StdEncoding.DecodeString(manifest.Signature)
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		t.Fatal("signature does not verify against signed payload")
	}
	var signed ManifestPayload
	if err := json.Unmarshal(payload, &signed); err != nil {
		t.Fatal(err)
	}
	if signed.SHA256 != manifest.SHA256 || signed.Version != manifest.Version || signed.SizeBytes != manifest.SizeBytes {
		t.Fatalf("signed payload %+v does not match served manifest %+v", signed, manifest)
	}
}
