package main

import (
	"path/filepath"
	"testing"
)

func TestManagedCLIRejectsIncompleteAndUnsafeOptions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := []string{"-repository", filepath.Join(dir, "workspace"), "-root", filepath.Join(dir, "root.json"), "-keys", filepath.Join(dir, "keys")}
	for _, flags := range [][]string{
		{"-backup", filepath.Join(dir, "backup")},
		{"-resume"},
		{"-backup", dir, "-records", filepath.Join(dir, "records"), "-mirror", filepath.Join(dir, "mirror")},
		{"-backup", filepath.Join(dir, "backup"), "-records", filepath.Join(dir, "records"),
			"-mirror", filepath.Join(dir, "mirror"), "-resume", "-inventory", "inventory.json"},
	} {
		args := append(append([]string(nil), base...), flags...)
		if err := run(args); err == nil {
			t.Fatalf("unsafe managed flags accepted: %v", flags)
		}
	}
}
