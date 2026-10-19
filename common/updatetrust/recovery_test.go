package updatetrust

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func readPublication(t *testing.T, directory, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func routineReplacement(t *testing.T, repo *testRepository) ([]byte, map[string]ed25519.PrivateKey) {
	t.Helper()
	public := make(map[string]crypto.PublicKey)
	keys := make(map[string]ed25519.PrivateKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		pub, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		public[role], keys[role] = pub, key
	}
	root, err := RotateRoutineKeys(readPublication(t, repo.dir, "1.root.json"), repo.keys["root"], public, time.Now().Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return root, keys
}

func TestRoutineRotationWithLostKeysAndRestartedClients(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	root := readPublication(t, repo.dir, "1.root.json")
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	// A client that skipped an intervening publication must also be supported.
	offlineOptions := client.options
	offlineOptions.CacheDir = filepath.Join(t.TempDir(), "offline-client")
	if err := Publish(PublishOptions{Directory: repo.dir, Root: root, Keys: repo.keys}); err != nil {
		t.Fatal(err)
	}
	before := readPublication(t, repo.dir, "timestamp.json")
	revokedTimestampKey := repo.keys["timestamp"]
	next, keys := routineReplacement(t, repo)
	base := PublishOptions{Directory: repo.dir, Root: next, Keys: keys, PreviousRoot: root,
		ExpectedTimestampSHA256: fmt.Sprintf("%x", sha256.Sum256(before))}
	// The first attempt can leave immutable files before the timestamp commit.
	// An exact matching orphan must not require resetting publisher state.
	if err := os.WriteFile(filepath.Join(repo.dir, "2.root.json"), next, 0o600); err != nil {
		t.Fatal(err)
	}
	delete(repo.keys, "targets")
	delete(repo.keys, "snapshot")
	delete(repo.keys, "timestamp")
	if err := Publish(base); err != nil {
		t.Fatalf("old routine keys must not be required: %v", err)
	}
	after := readPublication(t, repo.dir, "timestamp.json")
	oldTimestamp, err := metadata.Timestamp().FromBytes(before)
	if err != nil {
		t.Fatal(err)
	}
	newTimestamp, err := metadata.Timestamp().FromBytes(after)
	if err != nil {
		t.Fatal(err)
	}
	if newTimestamp.Signed.Version != oldTimestamp.Signed.Version+1 {
		t.Fatal("rotation reset publication versions")
	}
	for _, options := range []Options{client.options, offlineOptions} {
		restarted, err := NewClient(options)
		if err != nil {
			t.Fatal(err)
		}
		if err := restarted.Verify(context.Background(), repo.target); err != nil {
			t.Fatalf("root transition must retain target authorization: %v", err)
		}
	}
	if err := Publish(base); err == nil {
		t.Fatal("old pinned transition silently repeated after commit")
	}
	if err := Publish(PublishOptions{Directory: repo.dir, Root: next, Keys: keys}); err != nil {
		t.Fatalf("ordinary post-rotation freshness refresh failed: %v", err)
	}
	// Even metadata with higher versions and a matching target is unauthorized
	// when signed by the revoked online authority.
	revoked := metadata.Timestamp(time.Now().Add(7 * 24 * time.Hour))
	revoked.Signed.Version = newTimestamp.Signed.Version + 100
	revoked.Signed.Meta = newTimestamp.Signed.Meta
	signTestMetadata(t, revoked, revokedTimestampKey)
	original := readPublication(t, repo.dir, "1.root.json")
	trusted, err := metadata.Root().FromBytes(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.VerifyDelegate("timestamp", revoked); err != nil {
		t.Fatalf("test must use a genuinely revoked signer: %v", err)
	}
	if err := revoked.ToFile(filepath.Join(repo.dir, "timestamp.json"), false); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewClient(client.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Verify(context.Background(), repo.target); err == nil {
		t.Fatal("revoked signer accepted after root rotation and restart")
	}
}

func TestPublisherRecoveryPinsLatestCommitAndPreservesVersions(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	root := readPublication(t, repo.dir, "1.root.json")
	stale := t.TempDir()
	if err := os.CopyFS(stale, os.DirFS(repo.dir)); err != nil {
		t.Fatal(err)
	}
	if err := Publish(PublishOptions{Directory: repo.dir, Root: root, Keys: repo.keys}); err != nil {
		t.Fatal(err)
	}
	latest := readPublication(t, repo.dir, "timestamp.json")
	pin := fmt.Sprintf("%x", sha256.Sum256(latest))
	restored := t.TempDir()
	if err := os.CopyFS(restored, os.DirFS(repo.dir)); err != nil {
		t.Fatal(err)
	}
	opts := PublishOptions{Directory: restored, Root: root, Keys: repo.keys, ExpectedTimestampSHA256: pin,
		Now: time.Now().Add(200 * 24 * time.Hour)}
	if err := Publish(opts); err != nil {
		t.Fatalf("authenticated expired routine state should be recoverable: %v", err)
	}
	timestamp, err := metadata.Timestamp().FromBytes(readPublication(t, restored, "timestamp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if timestamp.Signed.Version != 3 {
		t.Fatalf("expected monotonic recovered timestamp 3, got %d", timestamp.Signed.Version)
	}
	if string(readPublication(t, restored, "1.targets.json")) != string(readPublication(t, repo.dir, "1.targets.json")) {
		t.Fatal("recovery overwrote retained history")
	}
	for _, directory := range []string{stale, t.TempDir()} {
		opts.Directory = directory
		before, _ := os.ReadFile(filepath.Join(directory, "timestamp.json"))
		if err := Publish(opts); err == nil {
			t.Fatal("missing or stale backup accepted against independently recorded latest commit")
		}
		after, _ := os.ReadFile(filepath.Join(directory, "timestamp.json"))
		if string(before) != string(after) {
			t.Fatal("failed recovery changed the commit point")
		}
	}
}

func TestPublisherTransitionFailuresDoNotCommit(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing pin", "wrong pin", "invalid pin", "wrong previous authority",
		"skipped root", "old signers", "no explicit previous root", "missing root history", "corrupt dependency", "missing dependency", "conflicting root output"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			root := readPublication(t, repo.dir, "1.root.json")
			next, keys := routineReplacement(t, repo)
			before := readPublication(t, repo.dir, "timestamp.json")
			opts := PublishOptions{Directory: repo.dir, Root: next, Keys: keys, PreviousRoot: root,
				ExpectedTimestampSHA256: fmt.Sprintf("%x", sha256.Sum256(before))}
			switch name {
			case "missing pin":
				opts.ExpectedTimestampSHA256 = ""
			case "wrong pin":
				opts.ExpectedTimestampSHA256 = strings.Repeat("0", 64)
			case "invalid pin":
				opts.ExpectedTimestampSHA256 = "invalid"
			case "wrong previous authority":
				other := newTestRepository(t)
				opts.PreviousRoot = readPublication(t, other.dir, "1.root.json")
			case "skipped root":
				value, err := metadata.Root().FromBytes(next)
				if err != nil {
					t.Fatal(err)
				}

				value.Signed.Version++
				signTestMetadata(t, value, repo.keys["root"])
				opts.Root, _ = value.ToBytes(false)
			case "old signers":
				opts.Keys = repo.keys
			case "no explicit previous root":
				opts.PreviousRoot = nil
			case "missing root history":
				if err := os.Remove(filepath.Join(repo.dir, "1.root.json")); err != nil {
					t.Fatal(err)
				}
			case "corrupt dependency":
				if err := os.WriteFile(filepath.Join(repo.dir, "1.targets.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing dependency":
				if err := os.Remove(filepath.Join(repo.dir, "1.snapshot.json")); err != nil {
					t.Fatal(err)
				}
			case "conflicting root output":
				if err := os.WriteFile(filepath.Join(repo.dir, "2.root.json"), []byte("conflicting orphan root"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Publish(opts); err == nil {
				t.Fatal("unsafe transition accepted")
			}
			if string(before) != string(readPublication(t, repo.dir, "timestamp.json")) {
				t.Fatal("failed transition changed timestamp")
			}
		})
	}
}

func TestRoutineRotationRejectsReusedOrIncompleteKeys(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	root := readPublication(t, repo.dir, "1.root.json")
	for _, test := range []string{"old role key", "shared new keys", "root key", "missing role", "wrong role"} {
		t.Run(test, func(t *testing.T) {
			keys := make(map[string]crypto.PublicKey)
			for _, role := range []string{"targets", "snapshot", "timestamp"} {
				pub, _, err := ed25519.GenerateKey(nil)
				if err != nil {
					t.Fatal(err)
				}
				keys[role] = pub
			}
			switch test {
			case "old role key":
				keys["targets"] = repo.keys["targets"].Public()
			case "shared new keys":
				keys["timestamp"] = keys["targets"]
			case "root key":
				keys["targets"] = repo.keys["root"].Public()
			case "missing role":
				delete(keys, "snapshot")
			case "wrong role":
				keys["unknown"] = keys["snapshot"]
				delete(keys, "snapshot")
			}
			if _, err := RotateRoutineKeys(root, repo.keys["root"], keys, time.Now().Add(time.Hour)); err == nil {
				t.Fatal("invalid routine keys accepted")
			}
		})
	}
}

func TestPublisherRecoveryRequiresConsistentHistoryAfterRotation(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing intermediate", "wrong anchor", "wrong version", "broken authorization"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			root := readPublication(t, repo.dir, "1.root.json")
			next, keys := routineReplacement(t, repo)
			pin := fmt.Sprintf("%x", sha256.Sum256(readPublication(t, repo.dir, "timestamp.json")))
			if err := Publish(PublishOptions{Directory: repo.dir, Root: next, PreviousRoot: root, Keys: keys, ExpectedTimestampSHA256: pin}); err != nil {
				t.Fatal(err)
			}
			before := readPublication(t, repo.dir, "timestamp.json")
			opts := PublishOptions{Directory: repo.dir, Root: next, Keys: keys,
				ExpectedTimestampSHA256: fmt.Sprintf("%x", sha256.Sum256(before))}
			switch failure {
			case "missing intermediate":
				if err := os.Remove(filepath.Join(repo.dir, "1.root.json")); err != nil {
					t.Fatal(err)
				}
			case "wrong anchor":
				otherRoot, _ := routineReplacement(t, repo)
				opts.Root = otherRoot
			case "wrong version":
				if err := os.WriteFile(filepath.Join(repo.dir, "1.root.json"), next, 0o600); err != nil {
					t.Fatal(err)
				}
			case "broken authorization":
				other := newTestRepository(t)
				if err := os.WriteFile(filepath.Join(repo.dir, "1.root.json"),
					readPublication(t, other.dir, "1.root.json"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Publish(opts); err == nil {
				t.Fatal("inconsistent recovery root history accepted")
			}
			if string(before) != string(readPublication(t, repo.dir, "timestamp.json")) {
				t.Fatal("failed history check changed committed state")
			}
		})
	}
}
