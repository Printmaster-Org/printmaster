package updatetrust

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type testRepository struct {
	dir       string
	root      *metadata.Metadata[metadata.RootType]
	targets   *metadata.Metadata[metadata.TargetsType]
	snapshot  *metadata.Metadata[metadata.SnapshotType]
	timestamp *metadata.Metadata[metadata.TimestampType]
	keys      map[string]ed25519.PrivateKey
	target    Target
}

func newTestRepository(t *testing.T) *testRepository {
	t.Helper()
	r := &testRepository{
		dir: t.TempDir(), root: metadata.Root(time.Now().Add(365 * 24 * time.Hour)),
		targets:   metadata.Targets(time.Now().Add(180 * 24 * time.Hour)),
		snapshot:  metadata.Snapshot(time.Now().Add(30 * 24 * time.Hour)),
		timestamp: metadata.Timestamp(time.Now().Add(7 * 24 * time.Hour)),
		keys:      make(map[string]ed25519.PrivateKey),
		target:    Target{Component: "agent", Channel: "stable", Version: "0.32.0", Platform: "windows", Arch: "amd64", Format: "binary"},
	}
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		pub, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		r.keys[role] = key
		tufKey, err := metadata.KeyFromPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.root.Signed.AddKey(tufKey, role); err != nil {
			t.Fatal(err)
		}
	}
	path, err := r.target.Path()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("official test artifact")
	hash := sha256.Sum256(body)
	r.target.SHA256, r.target.SizeBytes = hex.EncodeToString(hash[:]), int64(len(body))
	info, err := metadata.TargetFile().FromBytes(path, body, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	r.targets.Signed.Targets[path] = info
	r.publish(t)
	return r
}

func signTestMetadata[T metadata.Roles](t *testing.T, value *metadata.Metadata[T], key ed25519.PrivateKey) {
	t.Helper()
	signer, err := signature.LoadSigner(key, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	value.Signatures = nil
	if _, err := value.Sign(signer); err != nil {
		t.Fatal(err)
	}
}

func (r *testRepository) publish(t *testing.T) {
	t.Helper()
	signTestMetadata(t, r.root, r.keys["root"])
	signTestMetadata(t, r.targets, r.keys["targets"])
	r.snapshot.Signed.Meta["targets.json"] = metadata.MetaFile(r.targets.Signed.Version)
	signTestMetadata(t, r.snapshot, r.keys["snapshot"])
	r.timestamp.Signed.Meta["snapshot.json"] = metadata.MetaFile(r.snapshot.Signed.Version)
	signTestMetadata(t, r.timestamp, r.keys["timestamp"])
	for path, write := range map[string]func(string, bool) error{
		"1.root.json": r.root.ToFile, "1.targets.json": r.targets.ToFile,
		"1.snapshot.json": r.snapshot.ToFile, "timestamp.json": r.timestamp.ToFile,
	} {
		if err := write(filepath.Join(r.dir, path), false); err != nil {
			t.Fatal(err)
		}
	}
}

func repositoryClient(t *testing.T, repo *testRepository) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.FileServer(http.Dir(repo.dir)))
	t.Cleanup(server.Close)
	root, err := os.ReadFile(filepath.Join(repo.dir, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Options{TrustedRoot: root, MetadataURL: server.URL, CacheDir: t.TempDir(), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestPublisherAuthorization(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hash", "size", "channel", "version", "format", "platform"} {
		t.Run(field, func(t *testing.T) {
			target := repo.target
			switch field {
			case "hash":
				target.SHA256 = hex.EncodeToString(make([]byte, 32))
			case "size":
				target.SizeBytes++
			case "channel":
				target.Channel = "beta"
			case "version":
				target.Version = "0.33.0"
			case "format":
				target.Format = "msi"
			case "platform":
				target.Platform = "linux"
			}
			if err := client.Verify(context.Background(), target); err == nil {
				t.Fatal("unauthorized target accepted")
			}
		})
	}
}

func TestPublisherRejectsExpiryAndTampering(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"expired", "tampered", "cancelled", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			client, _ := repositoryClient(t, repo)
			ctx := context.Background()
			switch mode {
			case "expired":
				repo.timestamp.Signed.Expires = time.Now().Add(-time.Hour)
				repo.publish(t)
			case "tampered":
				repo.timestamp.Signed.Version++
				if err := repo.timestamp.ToFile(filepath.Join(repo.dir, "timestamp.json"), false); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "rollback":
				repo.timestamp.Signed.Version = 2
				repo.publish(t)
				if err := client.Verify(ctx, repo.target); err != nil {
					t.Fatal(err)
				}
				repo.timestamp.Signed.Version = 1
				repo.publish(t)
			}
			if err := client.Verify(ctx, repo.target); err == nil {
				t.Fatalf("%s metadata accepted", mode)
			}
		})
	}
}

func TestPublisherRootRotationPersistsAcrossRestart(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	oldKey := repo.keys["root"]
	oldID := repo.root.Signed.Roles["root"].KeyIDs[0]
	pub, newKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	key, err := metadata.KeyFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.root.Signed.RevokeKey(oldID, "root"); err != nil {
		t.Fatal(err)
	}
	if err := repo.root.Signed.AddKey(key, "root"); err != nil {
		t.Fatal(err)
	}
	repo.root.Signed.Version = 2
	signTestMetadata(t, repo.root, oldKey)
	signer, err := signature.LoadSigner(newKey, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.root.Sign(signer); err != nil {
		t.Fatal(err)
	}
	if err := repo.root.ToFile(filepath.Join(repo.dir, "2.root.json"), false); err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("dual-authorized rotation rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(repo.dir, "2.root.json")); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewClient(client.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("verified root was not retained across restart: %v", err)
	}
}

func TestPublisherRejectsRootSignedOnlyByReplacementKey(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	replacement := newTestRepository(t)
	replacement.root.Signed.Version = 2
	signTestMetadata(t, replacement.root, replacement.keys["root"])
	if err := replacement.root.ToFile(filepath.Join(repo.dir, "2.root.json"), false); err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); err == nil {
		t.Fatal("untrusted replacement root accepted")
	}
}

func TestPublisherRefreshesExpiredLocalMetadataWithoutLosingTrust(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	// An Agent returning after an extended outage may have expired local
	// freshness state. It must obtain current metadata, not bypass expiry.
	repo.timestamp.Signed.Expires = time.Now().Add(-180 * 24 * time.Hour)
	signTestMetadata(t, repo.timestamp, repo.keys["timestamp"])
	if err := repo.timestamp.ToFile(filepath.Join(client.options.CacheDir, "timestamp.json"), false); err != nil {
		t.Fatal(err)
	}
	// Model consistently persisted metadata that has aged, not accidental
	// modification of one file without its integrity checkpoint.
	if err := client.checkpointTrustState(); err != nil {
		t.Fatal(err)
	}
	repo.timestamp.Signed.Version++
	repo.timestamp.Signed.Expires = time.Now().Add(7 * 24 * time.Hour)
	repo.publish(t)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("current metadata could not repair expired local freshness state: %v", err)
	}
}
