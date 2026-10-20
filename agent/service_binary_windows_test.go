//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

func TestProtectWindowsServiceFileRejectsHardLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.exe")
	if err := os.WriteFile(path, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(t.TempDir(), "alias.exe")); err != nil {
		t.Fatal(err)
	}
	if err := protectWindowsServiceFile(path); err == nil {
		t.Fatal("accepted multiply linked service executable")
	}
}

func TestPrepareWindowsServiceExecutableRejectsHardLinkedSource(t *testing.T) {
	const child = "PRINTMASTER_TEST_LINKED_SERVICE_SOURCE"
	if fixture := os.Getenv(child); fixture != "" {
		t.Setenv("ProgramFiles", fixture)
		if err := prepareWindowsServiceExecutable(); err == nil {
			t.Fatal("accepted multiply linked source executable")
		}
		if _, err := os.Stat(windowsServiceExecutablePath()); !os.IsNotExist(err) {
			t.Fatalf("rejected source still created a service executable: %v", err)
		}
		return
	}
	fixture := t.TempDir()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "linked-source.test.exe")
	if err := os.Link(source, alias); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, alias, "-test.run=^TestPrepareWindowsServiceExecutableRejectsHardLinkedSource$")
	cmd.Env = append(os.Environ(), child+"="+fixture)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hard-linked source rejection failed: %v\n%s", err, output)
	}
}
