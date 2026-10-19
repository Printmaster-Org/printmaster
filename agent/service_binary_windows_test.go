//go:build windows

package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrepareWindowsServiceExecutableInstallsProtectedCopy(t *testing.T) {
	t.Setenv("ProgramFiles", t.TempDir())
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}

	if err := prepareWindowsServiceExecutable(); err != nil {
		t.Fatal(err)
	}
	target := windowsServiceExecutablePath()
	targetBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(sourceBytes) != sha256.Sum256(targetBytes) {
		t.Fatal("installed service executable differs from the running executable")
	}

	descriptor, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("installed service executable has an inheritable, unprotected DACL")
	}
	if filepath.Dir(target) != filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent") {
		t.Fatalf("unexpected service executable directory: %s", filepath.Dir(target))
	}
}
