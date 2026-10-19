package updatetrust

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func transactionFixture(t *testing.T) (*testRepository, TransactionOptions) {
	t.Helper()
	repo := newTestRepository(t)
	return repo, TransactionOptions{Publish: PublishOptions{Directory: repo.dir,
		Root: readPublication(t, repo.dir, "1.root.json"), Keys: repo.keys, Now: time.Now().UTC().Truncate(time.Second)},
		BackupDirectory: t.TempDir(), RecordDirectory: t.TempDir(), MirrorDirectory: t.TempDir()}
}

func TestManagedPublicationBackupRecordAndDistribution(t *testing.T) {
	t.Parallel()
	repo, opts := transactionFixture(t)
	before := readPublication(t, repo.dir, "timestamp.json")
	if err := PublishTransaction(opts); err != nil {
		t.Fatal(err)
	}
	current := readPublication(t, repo.dir, "timestamp.json")
	if bytes.Equal(before, current) {
		t.Fatal("freshness publication did not advance")
	}
	pin := digest(current)
	recordData := readPublication(t, opts.RecordDirectory, pin+".json")
	if !bytes.Equal(recordData, readPublication(t, filepath.Join(opts.BackupDirectory, pin), "record.json")) {
		t.Fatal("independent record and backup record differ")
	}
	var record publicationRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	for name, hash := range record.Files {
		for _, directory := range []string{repo.dir, opts.MirrorDirectory, filepath.Join(opts.BackupDirectory, pin)} {
			if digest(readPublication(t, directory, name)) != hash {
				t.Fatalf("unverified output %s", name)
			}
		}
	}
	server := httptest.NewServer(http.FileServer(http.Dir(opts.MirrorDirectory)))
	defer server.Close()
	client, err := NewClient(Options{TrustedRoot: opts.Publish.Root, MetadataURL: server.URL, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.dir, ".pending-publication")); !os.IsNotExist(err) {
		t.Fatal("successful transaction left pending state")
	}
	if err := PublishTransaction(opts); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(opts.BackupDirectory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("freshness run did not preserve both backups: %v", err)
	}
}

func TestManagedPublicationResumeAfterDistributionFailure(t *testing.T) {
	t.Parallel()
	repo, opts := transactionFixture(t)
	// A bad immutable destination stops delivery before its timestamp changes.
	if err := os.WriteFile(filepath.Join(opts.MirrorDirectory, "2.targets.json"), []byte("conflict"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishTransaction(opts); err == nil {
		t.Fatal("immutable conflict ignored")
	}
	if _, err := os.Stat(filepath.Join(opts.MirrorDirectory, "timestamp.json")); !os.IsNotExist(err) {
		t.Fatal("timestamp exposed before all immutable dependencies")
	}
	committed := readPublication(t, repo.dir, "timestamp.json")
	pin := digest(committed)
	if _, err := os.Stat(filepath.Join(opts.RecordDirectory, pin+".json")); err != nil {
		t.Fatal("missing pre-distribution record", err)
	}
	if err := PublishTransaction(opts); err == nil {
		t.Fatal("pending transaction silently replaced")
	}
	if err := os.Remove(filepath.Join(opts.MirrorDirectory, "2.targets.json")); err != nil {
		t.Fatal(err)
	}
	opts.Resume = true
	opts.Publish.Keys = nil // Resuming sealed bytes never needs signing secrets.
	if err := PublishTransaction(opts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, readPublication(t, opts.MirrorDirectory, "timestamp.json")) {
		t.Fatal("resume signed another publication")
	}
}

func TestManagedPublicationFailuresBeforeVisibleCommit(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"backup", "record", "tampered stage", "expired stage"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			repo, opts := transactionFixture(t)
			before := readPublication(t, repo.dir, "timestamp.json")
			// Build the same deterministic signed output to obstruct its backup
			// or independent record path without permissions-dependent tests.
			prepared := t.TempDir()
			if err := os.CopyFS(prepared, os.DirFS(repo.dir)); err != nil {
				t.Fatal(err)
			}
			publish := opts.Publish
			publish.Directory = prepared
			if err := Publish(publish); err != nil {
				t.Fatal(err)
			}
			pin := digest(readPublication(t, prepared, "timestamp.json"))
			if err := os.WriteFile(filepath.Join(opts.BackupDirectory, pin), []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := PublishTransaction(opts); err == nil {
				t.Fatal("backup/record failure ignored")
			}
			if !bytes.Equal(before, readPublication(t, repo.dir, "timestamp.json")) {
				t.Fatal("workspace committed before backup and record")
			}
			if _, err := os.Stat(filepath.Join(opts.MirrorDirectory, "timestamp.json")); !os.IsNotExist(err) {
				t.Fatal("failed backup distributed")
			}
			if failure == "tampered stage" {
				if err := os.WriteFile(filepath.Join(repo.dir, ".pending-publication", "2.targets.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if failure == "expired stage" {
				opts.Publish.Now = opts.Publish.Now.Add(8 * 24 * time.Hour)
			} else if failure == "record" {
				if err := os.Remove(filepath.Join(opts.BackupDirectory, pin)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(opts.RecordDirectory, pin+".json"), []byte("conflict"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			opts.Resume = true
			if err := PublishTransaction(opts); err == nil {
				t.Fatal("unsafe pending transaction resumed")
			}
		})
	}
}

func TestManagedPublicationLocksAndLocations(t *testing.T) {
	t.Parallel()
	repo, opts := transactionFixture(t)
	lock := filepath.Join(opts.MirrorDirectory, ".publisher.lock")
	if err := os.WriteFile(lock, []byte("another publisher"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishTransaction(opts); err == nil {
		t.Fatal("concurrent owner ignored")
	}
	if string(readPublication(t, opts.MirrorDirectory, ".publisher.lock")) != "another publisher" {
		t.Fatal("foreign lock removed")
	}
	if _, err := os.Stat(filepath.Join(repo.dir, ".pending-publication")); !os.IsNotExist(err) {
		t.Fatal("lock failure began signing")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{repo.dir, filepath.Join(repo.dir, "nested"), ""} {
		bad := opts
		bad.MirrorDirectory = location
		if err := PublishTransaction(bad); err == nil {
			t.Fatal("unsafe output locations accepted")
		}
	}
	opts.Resume = true
	if err := PublishTransaction(opts); err == nil {
		t.Fatal("missing pending publication silently initialized")
	}
}

func TestTimestampIsLastCopiedFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	files := map[string][]byte{"1.root.json": []byte("root"), "1.targets.json": []byte("targets"),
		"1.snapshot.json": []byte("snapshot"), "timestamp.json": []byte("new timestamp")}
	if err := os.WriteFile(filepath.Join(directory, "timestamp.json"), []byte("old timestamp"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "1.targets.json"), []byte("conflict"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyPublication(directory, files, true); err == nil {
		t.Fatal("copy conflict ignored")
	}
	if string(readPublication(t, directory, "timestamp.json")) != "old timestamp" {
		t.Fatal("timestamp changed before dependencies completed")
	}
}

func TestManagedPublicationSupportsRoutineTransition(t *testing.T) {
	t.Parallel()
	repo, opts := transactionFixture(t)
	next, keys := routineReplacement(t, repo)
	opts.Publish.PreviousRoot = opts.Publish.Root
	opts.Publish.Root, opts.Publish.Keys = next, keys
	opts.Publish.ExpectedTimestampSHA256 = digest(readPublication(t, repo.dir, "timestamp.json"))
	if err := PublishTransaction(opts); err != nil {
		t.Fatal(err)
	}
	current, err := metadata.Timestamp().FromBytes(readPublication(t, opts.MirrorDirectory, "timestamp.json"))
	if err != nil || current.Signed.Version != 2 {
		t.Fatalf("bad transition commit: %v", err)
	}
}
