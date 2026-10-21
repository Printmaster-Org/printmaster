package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestLiveWindowsInstallerPermissions(t *testing.T) {
	if os.Getenv("PRINTMASTER_LIVE_INSTALLER_PERMISSIONS") != "qualify-test-agent-msi" {
		t.Skip("requires owner-approved MSI installation and SYSTEM test task")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil || !user.User.Sid.Equals(system) {
		t.Fatalf("live qualification must run as SYSTEM: %v", err)
	}
	connection, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	service, err := connection.OpenService("PrintMasterAgent")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	config, err := service.Config()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent", "printmaster-agent.exe")
	if !strings.EqualFold(config.BinaryPathName, `"`+binary+`" --service run`) ||
		!strings.EqualFold(config.ServiceStartName, "LocalSystem") {
		t.Fatalf("unexpected service registration: %q %q", config.BinaryPathName, config.ServiceStartName)
	}
	status, err := service.Query()
	if err != nil || status.State != svc.Running {
		t.Fatalf("MSI-installed service is not running: %+v %v", status, err)
	}
	if err := validateInstallPath(binary); err != nil {
		t.Fatalf("MSI-installed executable is not protected: %v", err)
	}
	data := filepath.Join(os.Getenv("ProgramData"), "PrintMaster", "agent")
	token := restrictedUsersToken(t)
	paths := []string{binary}
	for _, dir := range []string{
		filepath.Join(data, updateDirName, stagingDirName),
		filepath.Join(data, updateDirName, backupDirName),
		filepath.Join(data, updateDirName),
		filepath.Join(data, "update-trust", "metadata"),
	} {
		// Do not repair ACLs here: qualify the directories created by the service.
		file, err := os.CreateTemp(dir, "permission-qualification-")
		if err != nil {
			t.Fatalf("SYSTEM cannot create private evidence in %s: %v", dir, err)
		}
		path := file.Name()
		t.Cleanup(func() {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		})
		if _, err := file.WriteString("disposable permission evidence"); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := validateInstallPath(path); err != nil {
			t.Fatalf("service-created evidence has unsafe permissions: %v", err)
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		accesses := []uint32{windows.GENERIC_WRITE, windows.DELETE, windows.WRITE_DAC}
		if path != binary {
			accesses = append(accesses, windows.GENERIC_READ)
		}
		for _, access := range accesses {
			if err := openWithRestrictedToken(token, path, access, windows.OPEN_EXISTING); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("Users-restricted token not denied %#x on %s: %v", access, path, err)
			}
		}
		if err := openWithRestrictedToken(token, path+".injected", windows.GENERIC_WRITE, windows.CREATE_NEW); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("Users-restricted token not denied file injection: %s: %v", path, err)
		}
	}
	assertRestrictedTokenPositiveControl(t, token)
	t.Log("MSI canonical service path, running SYSTEM identity, protected inherited state and Users denial qualified")
}
