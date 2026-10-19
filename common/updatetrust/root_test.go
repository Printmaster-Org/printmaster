package updatetrust

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestBootstrapRootReleasePolicy(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	public := make(map[string]crypto.PublicKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		public[role] = repo.keys[role].Public()
	}
	data, err := InitializeRoot(repo.keys["root"], public, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBootstrapRoot(data, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBootstrapRoot(data, time.Now().Add(2*time.Hour)); err == nil {
		t.Fatal("expired root accepted")
	}
	for _, change := range []struct {
		name string
		edit func(*metadata.Metadata[metadata.RootType])
	}{
		{"unsigned", func(r *metadata.Metadata[metadata.RootType]) { r.Signatures = nil }},
		{"zero version", func(r *metadata.Metadata[metadata.RootType]) { r.Signed.Version = 0 }},
		{"inconsistent snapshot", func(r *metadata.Metadata[metadata.RootType]) { r.Signed.ConsistentSnapshot = false }},
		{"shared role key", func(r *metadata.Metadata[metadata.RootType]) {
			r.Signed.Roles["timestamp"].KeyIDs = r.Signed.Roles["targets"].KeyIDs
		}},
		{"missing role", func(r *metadata.Metadata[metadata.RootType]) { delete(r.Signed.Roles, "root") }},
		{"threshold", func(r *metadata.Metadata[metadata.RootType]) { r.Signed.Roles["root"].Threshold = 2 }},
	} {
		t.Run(change.name, func(t *testing.T) {
			value, err := metadata.Root().FromBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			change.edit(value)
			if change.name != "unsigned" {
				value.Signatures = nil
				if err := signMetadata(value, repo.keys["root"]); err != nil {
					t.Fatal(err)
				}
			}
			modified, err := value.ToBytes(false)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateBootstrapRoot(modified, time.Now()); err == nil {
				t.Fatal("invalid release root accepted")
			}
		})
	}
	value, err := metadata.Root().FromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	value.Signed.Version = math.MaxInt64
	value.Signatures = nil
	if err := signMetadata(value, repo.keys["root"]); err != nil {
		t.Fatal(err)
	}
	overflow, err := value.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RotateRoot(overflow, repo.keys["root"], repo.keys["root"], time.Now().Add(time.Hour)); err == nil {
		t.Fatal("root version overflow accepted")
	}
}

func TestOfflineRootInitializationAndRotation(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	public := make(map[string]crypto.PublicKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		public[role] = repo.keys[role].Public()
	}
	rootBytes, err := InitializeRoot(repo.keys["root"], public, time.Now().Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, replacement, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	nextBytes, err := RotateRoot(rootBytes, repo.keys["root"], replacement, time.Now().Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	old, err := metadata.Root().FromBytes(rootBytes)
	if err != nil {
		t.Fatal(err)
	}
	next, err := metadata.Root().FromBytes(nextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if next.Signed.Version != old.Signed.Version+1 {
		t.Fatal("root rotation did not increment version")
	}
	if err := old.VerifyDelegate("root", next); err != nil {
		t.Fatalf("missing previous authority: %v", err)
	}
	if err := next.VerifyDelegate("root", next); err != nil {
		t.Fatalf("missing replacement authority: %v", err)
	}
	if _, err := RotateRoot(rootBytes, replacement, replacement, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("unrelated old root signer accepted")
	}
	public["targets"] = repo.keys["root"].Public()
	if _, err := InitializeRoot(repo.keys["root"], public, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("offline root key reuse for routine publishing accepted")
	}
}

func TestShippedRootSupersedesOlderCache(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	_, replacement, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := RotateRoot(client.options.TrustedRoot, repo.keys["root"], replacement, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// The newer binary already contains root 2. Intermediate remote roots need
	// not be downloaded again just because its local cache still contains root 1.
	opts := client.options
	opts.TrustedRoot = next
	restarted, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	cached, err := os.ReadFile(filepath.Join(opts.CacheDir, "root.json"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := metadata.Root().FromBytes(cached)
	if err != nil {
		t.Fatal(err)
	}
	if root.Signed.Version != 2 {
		t.Fatal("newer shipped root was downgraded by local cache")
	}
}
