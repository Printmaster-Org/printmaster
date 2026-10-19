//go:build !windows

package updatetrust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublisherLocationRejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := ordinaryAncestors(link); err == nil {
		t.Fatal("publisher symlink accepted")
	}
}
