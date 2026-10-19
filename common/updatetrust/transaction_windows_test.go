package updatetrust

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRestoreRejectsWindowsJunctionBeforeRemoteReads(t *testing.T) {
	t.Parallel()
	_, opts, reader := restoreFixture(t)
	junction := filepath.Join(t.TempDir(), "restore-junction")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, opts.Directory).CombinedOutput(); err != nil {
		t.Fatalf("create disposable restore junction: %v: %s", err, output)
	}
	opts.Directory = junction
	if err := RestorePublication(context.Background(), opts); err == nil || reader.reads != 0 {
		t.Fatalf("restore followed a junction: err=%v remote reads=%d", err, reader.reads)
	}
}

func TestPublisherLocationRejectsWindowsJunction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target, junction := filepath.Join(dir, "target"), filepath.Join(dir, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("create disposable junction: %v: %s", err, output)
	}
	if err := ordinaryAncestors(junction); err == nil {
		t.Fatal("publisher junction accepted")
	}
}
