package updatetrust

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedPublicationRejectsDivergentMirror(t *testing.T) {
	t.Parallel()
	repo, opts := transactionFixture(t)
	before := readPublication(t, repo.dir, "timestamp.json")
	if err := os.WriteFile(filepath.Join(opts.MirrorDirectory, "timestamp.json"), []byte("unexpected newer or unrelated publication"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishTransaction(opts); err == nil {
		t.Fatal("divergent mirror overwritten")
	}
	if !bytes.Equal(before, readPublication(t, repo.dir, "timestamp.json")) {
		t.Fatal("divergence changed workspace commit")
	}
	if string(readPublication(t, opts.MirrorDirectory, "timestamp.json")) != "unexpected newer or unrelated publication" {
		t.Fatal("divergent timestamp was changed")
	}
}

func TestManagedPublicationDoesNotAdoptUnsealedPreparation(t *testing.T) {
	t.Parallel()
	repo, opts := transactionFixture(t)
	stage := filepath.Join(repo.dir, ".pending-publication")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "timestamp.json"), []byte("partial preparation"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Resume = true
	if err := PublishTransaction(opts); err == nil {
		t.Fatal("unsealed state adopted")
	}
	if string(readPublication(t, stage, "timestamp.json")) != "partial preparation" {
		t.Fatal("partial state erased")
	}
}

func TestManagedPublicationInitializesOnlyExplicitEmptyWorkspace(t *testing.T) {
	t.Parallel()
	_, opts := transactionFixture(t)
	opts.Publish.Directory = t.TempDir()
	if err := PublishTransaction(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.MirrorDirectory, "1.root.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.MirrorDirectory, "timestamp.json")); err != nil {
		t.Fatal(err)
	}
}
