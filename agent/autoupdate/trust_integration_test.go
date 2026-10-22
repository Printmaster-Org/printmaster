package autoupdate

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestRuntimeTrustStateFailureAndExactRepair(t *testing.T) {
	t.Parallel()
	body := []byte("publisher authorized artifact")
	sum := sha256.Sum256(body)
	manifest := fixtureManifest(&UpdateManifest{Version: "1.0.0", SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))})
	_, repository := runtimeTrustRepository(t, manifest, body, time.Now())
	server := httptest.NewServer(http.FileServer(http.Dir(repository)))
	defer server.Close()
	manager := lifecycleManager(t, &forceInstallClient{manifest: manifest, payload: body}, nil)
	cache := filepath.Join(filepath.Dir(manager.stateDir), "update-trust", "metadata")
	root, err := os.ReadFile(filepath.Join(repository, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := updatetrust.NewClient(updatetrust.Options{
		TrustedRoot: root, CacheDir: cache, MetadataURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.publisherVerifier = verifier
	if err := manager.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	backup := t.TempDir()
	for _, name := range []string{"root.json", "timestamp.json", "snapshot.json", "targets.json"} {
		data, err := os.ReadFile(filepath.Join(cache, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backup, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cache, "timestamp.json"), []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = manager.ForceInstallLatest(context.Background(), "test")
	if !errors.Is(err, ErrPublisherTrust) || !errors.Is(err, updatetrust.ErrTrustState) ||
		!strings.Contains(err.Error(), "update-client-trust") || manager.Progress().Status != "failed" {
		t.Fatalf("damaged trust not surfaced through Manager: %+v %v", manager.Progress(), err)
	}
	if _, err := os.Stat(filepath.Join(manager.stateDir, pendingUpdateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trust failure created pending installation: %v", err)
	}
	installed, err := os.ReadFile(manager.binaryPath)
	if err != nil || string(installed) != "old binary" {
		t.Fatalf("trust failure changed running installation: %q %v", installed, err)
	}
	if _, err := updatetrust.RepairClientTrust(context.Background(), updatetrust.ClientTrustRepairOptions{
		CacheDir: cache, BackupDir: backup, ExternalSerialization: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckNow(context.Background()); err != nil {
		t.Fatalf("normal Manager check did not recover after exact repair: %v", err)
	}
	checkpoint, err := os.ReadFile(cache + ".state.json")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(checkpoint, &state); err != nil {
		t.Fatal(err)
	}
	state["verified_at"], err = json.Marshal(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache+".state.json", checkpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ForceInstallLatest(context.Background(), "test"); !errors.Is(err, updatetrust.ErrTrustClock) ||
		!strings.Contains(err.Error(), "independent time source") || manager.Progress().Status != "failed" {
		t.Fatalf("clock failure hidden by Manager: %+v %v", manager.Progress(), err)
	}
}

func runtimeTrustRepository(t *testing.T, manifest *UpdateManifest, artifact []byte, publishedAt time.Time, earlier ...updatetrust.PublishTarget) (*updatetrust.Client, string) {
	t.Helper()
	directory := t.TempDir()
	file := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(file, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	_, rootKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := make(map[string]ed25519.PrivateKey)
	public := make(map[string]crypto.PublicKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		pub, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		keys[role], public[role] = key, pub
	}
	root, err := updatetrust.InitializeRoot(rootKey, public, time.Now().Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	target := updatetrust.Target{Component: manifest.Component, Channel: manifest.Channel,
		Version: manifest.Version, Platform: manifest.Platform, Arch: manifest.Arch,
		Format: "binary", SHA256: manifest.SHA256, SizeBytes: manifest.SizeBytes}
	if manifest.Format != "" {
		target.Format = manifest.Format
	}
	if err := updatetrust.Publish(updatetrust.PublishOptions{
		Directory: directory, Root: root, Keys: keys, Now: publishedAt,
		Targets: append(earlier, updatetrust.PublishTarget{Target: target, File: file}),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(directory)))
	t.Cleanup(server.Close)
	client, err := updatetrust.NewClient(updatetrust.Options{
		TrustedRoot: root, MetadataURL: server.URL, CacheDir: filepath.Join(t.TempDir(), "metadata"),
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, directory
}

func TestRuntimeUsesTUFInsteadOfServerArtifactHash(t *testing.T) {
	t.Parallel()
	official := []byte("publisher authorized artifact")
	hash := sha256.Sum256(official)
	manifest := fixtureManifest(&UpdateManifest{
		Version: "1.0.0", SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(official)),
	})
	verifier, _ := runtimeTrustRepository(t, manifest, official, time.Now())
	client := &forceInstallClient{manifest: manifest, payload: official}
	manager := lifecycleManager(t, client, nil)
	manager.publisherVerifier = verifier
	if err := manager.CheckNow(context.Background()); err != nil {
		t.Fatalf("legitimate TUF-authorized candidate rejected: %v", err)
	}
	malicious := []byte("server substituted artifact")
	hash = sha256.Sum256(malicious)
	substituted := *manifest
	substituted.SHA256, substituted.SizeBytes = hex.EncodeToString(hash[:]), int64(len(malicious))
	client.manifest, client.payload = &substituted, malicious
	if err := manager.ForceInstallLatest(context.Background(), "test"); !errors.Is(err, ErrPublisherTrust) {
		t.Fatalf("matching malicious Server artifact/hash accepted: %v", err)
	}
	installed, err := os.ReadFile(manager.binaryPath)
	if err != nil || string(installed) != "old binary" {
		t.Fatalf("failed authorization changed installed Agent: %q %v", installed, err)
	}
	if manager.Progress().Status != "failed" {
		t.Fatal("substitution failure not reported")
	}
	// A failure is retryable without discarding trusted metadata.
	client.manifest, client.payload = manifest, official
	if err := manager.CheckNow(context.Background()); err != nil {
		t.Fatalf("rejected substitution blocked legitimate retry: %v", err)
	}
}

func TestRuntimeInvalidMetadataCannotAuthorizeInstall(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "tampered", "expired"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			body := []byte("official artifact")
			hash := sha256.Sum256(body)
			manifest := fixtureManifest(&UpdateManifest{
				Version: "1.0.0", SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(body)),
			})
			publishedAt := time.Now()
			if mode == "expired" {
				publishedAt = publishedAt.Add(-8 * 24 * time.Hour)
			}
			verifier, directory := runtimeTrustRepository(t, manifest, body, publishedAt)
			switch mode {
			case "missing":
				if err := os.Remove(filepath.Join(directory, "timestamp.json")); err != nil {
					t.Fatal(err)
				}
			case "tampered":
				if err := os.WriteFile(filepath.Join(directory, "timestamp.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manager := lifecycleManager(t, &forceInstallClient{manifest: manifest, payload: body}, nil)
			manager.publisherVerifier = verifier
			if err := manager.ForceInstallLatest(context.Background(), "test"); !errors.Is(err, ErrPublisherTrust) {
				t.Fatalf("%s metadata accepted: %v", mode, err)
			}
			installed, err := os.ReadFile(manager.binaryPath)
			if err != nil || string(installed) != "old binary" {
				t.Fatalf("failed verification changed installation: %q %v", installed, err)
			}
		})
	}
}
