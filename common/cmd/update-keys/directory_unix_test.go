//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSymlinkOutputAncestorRejected(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	target, link := filepath.Join(parent, "target"), filepath.Join(parent, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := validateNewDirectories([]string{filepath.Join(link, "offline")}); err == nil {
		t.Fatal("key output through symlink accepted")
	}
}

func checkPrivateDirectory(t *testing.T, path string) {
	t.Helper()
	name := filepath.Join(path, "mode-probe")
	if err := writeExclusive(name, []byte("disposable mode probe")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{path: 0o700, name: 0o600} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm()&^want != 0 {
			t.Fatalf("private mode for %s: %v, %v", name, info, err)
		}
	}
}
