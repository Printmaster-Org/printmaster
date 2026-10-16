package updatetrust

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestPublishAndRefreshWithoutRootPrivateKey(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	root, err := os.ReadFile(filepath.Join(repo.dir, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(file, []byte("official test artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(repo.keys, "root")
	opts := PublishOptions{Directory: repo.dir, Root: root, Keys: repo.keys,
		Targets: []PublishTarget{{Target: repo.target, File: file}}, Now: time.Now()}
	if err := Publish(opts); err != nil {
		t.Fatal(err)
	}
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("published metadata rejected: %v", err)
	}
	opts.Targets = nil
	opts.Now = opts.Now.Add(24 * time.Hour)
	if err := Publish(opts); err != nil {
		t.Fatalf("freshness-only refresh: %v", err)
	}
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("refresh lost retained target: %v", err)
	}
	// A corrupt signer workspace must fail, not reset versions and re-sign
	// possibly attacker-controlled old state.
	if err := os.WriteFile(filepath.Join(repo.dir, "timestamp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(opts); err == nil {
		t.Fatal("corrupt publisher workspace accepted")
	}
}

func TestPublishRejectsUnauthorizedSigner(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	other := newTestRepository(t)
	root, err := os.ReadFile(filepath.Join(repo.dir, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Publish(PublishOptions{Directory: repo.dir, Root: root, Keys: other.keys}); err == nil {
		t.Fatal("unrelated publisher key authorized")
	}
}

func TestPublishRetainsCommitAndSkipsInterruptedVersions(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	root, err := os.ReadFile(filepath.Join(repo.dir, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a failed upload/write that left an uncommitted versioned file.
	orphan := filepath.Join(repo.dir, "2.targets.json")
	if err := os.WriteFile(orphan, []byte("interrupted publication"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := PublishOptions{Directory: repo.dir, Root: root, Keys: repo.keys}
	if err := Publish(opts); err != nil {
		t.Fatal(err)
	}
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("committed target was lost: %v", err)
	}
	if content, err := os.ReadFile(orphan); err != nil || string(content) != "interrupted publication" {
		t.Fatal("interrupted version was overwritten")
	}
	if _, err := os.Stat(filepath.Join(repo.dir, "3.targets.json")); err != nil {
		t.Fatal("publisher reused an orphaned version")
	}
	if err := os.Remove(filepath.Join(repo.dir, "timestamp.json")); err != nil {
		t.Fatal(err)
	}
	if err := Publish(opts); err == nil {
		t.Fatal("publisher silently reset after losing its commit point")
	}
}

func TestPublishRejectsChangedReleaseIdentity(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	root, err := os.ReadFile(filepath.Join(repo.dir, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "replacement")
	body := []byte("replacement release")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := metadata.TargetFile().FromBytes("replacement", body, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	changed := repo.target
	changed.SizeBytes = info.Length
	changed.SHA256 = hex.EncodeToString(info.Hashes["sha256"])
	if err := Publish(PublishOptions{Directory: repo.dir, Root: root, Keys: repo.keys,
		Targets: []PublishTarget{{Target: changed, File: file}}}); err == nil {
		t.Fatal("existing release identity was republished with different bytes")
	}
}

func TestPublishRejectsLostDependencies(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"1.targets.json", "1.snapshot.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			root, err := os.ReadFile(filepath.Join(repo.dir, "1.root.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(repo.dir, name)); err != nil {
				t.Fatal(err)
			}
			if err := Publish(PublishOptions{Directory: repo.dir, Root: root, Keys: repo.keys}); err == nil {
				t.Fatal("missing committed dependency silently reset publisher state")
			}
		})
	}
}
