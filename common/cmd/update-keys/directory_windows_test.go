package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestJunctionOutputAncestorRejected(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	target, link := filepath.Join(parent, "target"), filepath.Join(parent, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create disposable junction: %v: %s", err, output)
	}
	if err := validateNewDirectories([]string{filepath.Join(link, "offline")}); err == nil {
		t.Fatal("key output through junction accepted")
	}
}

func checkPrivateDirectory(t *testing.T, path string) {
	t.Helper()
	owner, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, filepath.Join(path, "acl-probe")} {
		if name != path {
			if err := writeExclusive(name, []byte("disposable ACL probe")); err != nil {
				t.Fatal(err)
			}
		}
		sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		if name == path {
			control, _, err := sd.Control()
			if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
				t.Fatalf("directory DACL must reject inherited parent permissions: %v", err)
			}
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil || acl.AceCount != 3 {
			t.Fatalf("expected three explicit/inherited ACL grants, got %v: %v", acl, err)
		}
		allowed := map[string]bool{owner.User.Sid.String(): true, "S-1-5-18": true, "S-1-5-32-544": true}
		for i := uint32(0); i < uint32(acl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, i, &ace); err != nil {
				t.Fatal(err)
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !allowed[sid.String()] || ace.Mask != 0x1f01ff {
				t.Fatalf("unexpected ACL grant: %s", sd.String())
			}
			delete(allowed, sid.String())
		}
		if len(allowed) != 0 {
			t.Fatalf("missing ACL principals: %v", allowed)
		}
	}
}
