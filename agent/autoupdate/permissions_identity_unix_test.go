//go:build !windows

package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestInstallationProtectionIdentityChild(t *testing.T) {
	role := os.Getenv("PRINTMASTER_PERMISSION_TEST_ROLE")
	if role == "" {
		return
	}
	uid, err := strconv.Atoi(os.Getenv("PRINTMASTER_PERMISSION_TEST_UID"))
	if err != nil || os.Geteuid() != uid || uid == 0 {
		t.Fatalf("fixture identity was not isolated: uid=%d expected=%d error=%v", os.Geteuid(), uid, err)
	}
	data := os.Getenv("PRINTMASTER_PERMISSION_TEST_DATA")
	installed := os.Getenv("PRINTMASTER_PERMISSION_TEST_INSTALLED")
	files := []string{
		filepath.Join(data, updateDirName, stagingDirName, "candidate"),
		filepath.Join(data, updateDirName, backupDirName, "previous"),
		filepath.Join(data, updateDirName, "installation.json"),
		filepath.Join(data, "update-trust", "metadata", "timestamp.json"),
	}
	switch role {
	case "service":
		if err := SecureUpdaterDirectories(data); err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if err := os.WriteFile(path, []byte("private evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := validateInstallPath(installed); err != nil {
			t.Fatalf("root-owned package executable should be safe to inspect: %v", err)
		}
		manager := &Manager{binaryPath: installed}
		if err := manager.applyUpdateUnix(files[0]); err == nil || !errors.Is(err, syscall.EACCES) {
			t.Fatalf("service identity replaced a root-owned package executable or hid OS denial: %v", err)
		}
		m, descriptor := preparedInstallation(t)
		if err := m.applyUpdate(context.Background(), descriptor); err != nil {
			t.Fatalf("service-owned raw installation failed: %v", err)
		}
		if body, err := os.ReadFile(m.binaryPath); err != nil || string(body) != "new binary" {
			t.Fatalf("raw atomic replacement failed: %q %v", body, err)
		}
	case "outsider":
		for _, path := range append(files, installed) {
			if file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0); err == nil {
				file.Close()
				t.Fatalf("outsider could overwrite protected path: %s", path)
			} else if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("overwrite did not prove permission denial: %s %v", path, err)
			}
			if err := os.Remove(path); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("outsider deletion was not denied: %s %v", path, err)
			}
		}
		for _, path := range files {
			if _, err := os.ReadFile(path); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("outsider could read private update state: %s %v", path, err)
			}
		}
		if err := os.WriteFile(filepath.Join(data, "injected"), []byte("substitution"), 0o600); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("outsider state injection was not denied: %v", err)
		}
	default:
		t.Fatalf("unknown fixture role: %s", role)
	}
	// Positive control: the child can write its own separate fixture directory.
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), role+"-completed"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationProtectionSeparateUnixIdentities(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("separate UID qualification requires root")
	}
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "printmaster-identity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "identity.test")
	if err := os.WriteFile(child, body, 0o755); err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(root, "root-install")
	if err := os.Mkdir(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(installDir, "printmaster-agent")
	if err := os.WriteFile(installed, []byte("unchanged package binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "service-data")
	for i, role := range []string{"service", "outsider"} {
		uid := uint32(60001 + i)
		tmp := filepath.Join(root, role+"-tmp")
		for _, path := range []string{tmp, data} {
			if path == data && role == "outsider" {
				continue
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, int(uid), int(uid)); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, child, "-test.run=^TestInstallationProtectionIdentityChild$", "-test.v")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + tmp, "TMPDIR=" + tmp,
			"PRINTMASTER_PERMISSION_TEST_ROLE=" + role, "PRINTMASTER_PERMISSION_TEST_UID=" + strconv.Itoa(int(uid)),
			"PRINTMASTER_PERMISSION_TEST_DATA=" + data, "PRINTMASTER_PERMISSION_TEST_INSTALLED=" + installed}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
		output, runErr := cmd.CombinedOutput()
		cancel()
		if runErr != nil {
			t.Fatalf("%s identity qualification failed: %v\n%s", role, runErr, output)
		}
		if _, err := os.Stat(filepath.Join(tmp, role+"-completed")); err != nil {
			t.Fatal(err)
		}
		t.Logf("UID %d (%s): protected-state and installation checks passed", uid, role)
	}
	if body, err := os.ReadFile(installed); err != nil || string(body) != "unchanged package binary" {
		t.Fatalf("protected package executable changed: %q %v", body, err)
	}
}
