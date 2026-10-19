//go:build !windows

package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureUpdaterDirectoriesRestrictsModes(t *testing.T) {
	dataDir := t.TempDir()
	stateFile := filepath.Join(dataDir, updateDirName, stagingDirName, "candidate")
	if err := SecureUpdaterDirectories(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte("candidate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SecureUpdaterDirectories(dataDir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		dataDir,
		filepath.Join(dataDir, updateDirName),
		filepath.Join(dataDir, updateDirName, stagingDirName),
		filepath.Join(dataDir, updateDirName, backupDirName),
		filepath.Join(dataDir, updateDirName, downloadDirName),
		filepath.Join(dataDir, "update-trust"),
		filepath.Join(dataDir, "update-trust", "metadata"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", path, info.Mode().Perm())
		}
	}
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestSecureUpdaterDirectoriesRejectsSymlinkState(t *testing.T) {
	dataDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, updateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, updateDirName, stagingDirName)); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := SecureUpdaterDirectories(dataDir); err == nil {
		t.Fatal("accepted updater staging symlink")
	}
}

func TestSecureUpdaterDirectoriesRejectsSymlinkTrustMetadata(t *testing.T) {
	dataDir := t.TempDir()
	if err := SecureUpdaterDirectories(dataDir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "metadata")
	if err := os.WriteFile(target, []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dataDir, "update-trust", "metadata", "current.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := SecureUpdaterDirectories(dataDir); err == nil {
		t.Fatal("accepted a publisher trust metadata symlink")
	}
}

func TestValidateInstallPathRejectsHardLinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent")
	if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(dir, "alias")); err != nil {
		t.Skipf("hard-link creation unavailable: %v", err)
	}
	if err := validateInstallPath(path); err == nil {
		t.Fatal("accepted installation target with multiple hard links")
	}
}

func TestValidateInstallPathRejectsUntrustedOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing file ownership requires root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent")
	if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, -1); err != nil {
		t.Skipf("changing file ownership unavailable: %v", err)
	}
	if err := validateInstallPath(path); err == nil {
		t.Fatal("accepted an installation target owned by an untrusted identity")
	}
}

func TestValidateInstallPathRejectsWritableParentAndTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent")
	if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallPath(path); err == nil {
		t.Fatal("accepted a group-writable installation parent")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o772); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallPath(path); err == nil {
		t.Fatal("accepted a group-writable installation target")
	}
}
