package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func restrictedUsersToken(t *testing.T) windows.Token {
	t.Helper()
	var process windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &process); err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(process,
		windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE, nil,
		windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	restriction := windows.SIDAndAttributes{Sid: users}
	var restricted windows.Token
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	ok, _, callErr := create.Call(uintptr(impersonation), 0, 0, 0, 0, 0, 1,
		uintptr(unsafe.Pointer(&restriction)), uintptr(unsafe.Pointer(&restricted)))
	runtime.KeepAlive(restriction)
	if ok == 0 {
		t.Fatalf("create restricted Users token: %v", callErr)
	}
	t.Cleanup(func() {
		if err := restricted.Close(); err != nil {
			t.Error(err)
		}
	})
	return restricted
}

func openWithRestrictedToken(token windows.Token, path string, access, creation uint32) (result error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, token); err != nil {
		return err
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			// Never return an impersonating thread to the Go scheduler.
			panic(err)
		}
	}()
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, creation, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(handle)
}

func TestInstallationProtectionRestrictedWindowsToken(t *testing.T) {
	data := t.TempDir()
	if err := SecureUpdaterDirectories(data); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(data, updateDirName, stagingDirName, "candidate"),
		filepath.Join(data, updateDirName, backupDirName, "previous"),
		filepath.Join(data, updateDirName, "installation.json"),
		filepath.Join(data, "update-trust", "metadata", "timestamp.json"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("private evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	token := restrictedUsersToken(t)
	for _, path := range paths {
		for _, access := range []uint32{windows.GENERIC_READ, windows.GENERIC_WRITE, windows.DELETE, windows.WRITE_DAC} {
			if err := openWithRestrictedToken(token, path, access, windows.OPEN_EXISTING); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("restricted token was not denied rights %#x on %s: %v", access, path, err)
			}
		}
		if err := openWithRestrictedToken(token, path+".injected", windows.GENERIC_WRITE, windows.CREATE_NEW); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("restricted token could inject updater data or failed for another reason: %v", err)
		}
	}
	assertRestrictedTokenPositiveControl(t, token)
}

func assertRestrictedTokenPositiveControl(t *testing.T, token windows.Token) {
	t.Helper()
	public := filepath.Join(t.TempDir(), "positive-control")
	if err := os.WriteFile(public, []byte("public control"), 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;BU)(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;OW)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(public, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := openWithRestrictedToken(token, public, windows.GENERIC_WRITE, windows.OPEN_EXISTING); err != nil {
		t.Fatalf("positive control failed; negative checks alone do not prove ACL enforcement: %v", err)
	}
}
