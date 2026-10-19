//go:build windows

package autoupdate

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSecureUpdaterDirectoriesProtectsExistingState(t *testing.T) {
	dataDir := t.TempDir()
	stateDir := filepath.Join(dataDir, updateDirName)
	if err := os.MkdirAll(filepath.Join(stateDir, stagingDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDir, stagingDirName, "candidate")
	if err := os.WriteFile(stateFile, []byte("candidate"), 0o666); err != nil {
		t.Fatal(err)
	}

	if err := SecureUpdaterDirectories(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, stagingDirName, "current-user-write"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("current updater identity lost access: %v", err)
	}

	descriptor, err := windows.GetNamedSecurityInfo(
		stateDir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("updater state DACL is not protected from inherited permissions")
	}
}

func TestSecureUpdaterDirectoriesRejectsReparseState(t *testing.T) {
	dataDir := t.TempDir()
	stateDir := filepath.Join(dataDir, updateDirName)
	outside := t.TempDir()
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stateDir, stagingDirName)); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := SecureUpdaterDirectories(dataDir); err == nil {
		t.Fatal("accepted updater staging reparse point")
	}
}

func TestWindowsServiceInstallPathRejectsLegacyLocation(t *testing.T) {
	programFiles := t.TempDir()
	t.Setenv("ProgramFiles", programFiles)
	protected := filepath.Join(programFiles, "PrintMaster", "Agent", "printmaster-agent.exe")
	if err := validateServiceInstallPath(protected); err != nil {
		t.Fatalf("accepted service install path rejected: %v", err)
	}
	if err := validateServiceInstallPath(filepath.Join(t.TempDir(), "printmaster-agent.exe")); err == nil {
		t.Fatal("accepted a service executable outside the protected install directory")
	}
}

func TestValidateInstallPathRejectsWritableAncestor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(path, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;BU)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateInstallPath(path); err == nil {
		t.Fatal("accepted an installation path writable by BUILTIN\\Users")
	}
}
