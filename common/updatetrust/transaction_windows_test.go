package updatetrust

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
