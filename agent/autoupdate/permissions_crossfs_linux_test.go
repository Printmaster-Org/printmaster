package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInstallationProtectionCrossFilesystem(t *testing.T) {
	dir, err := os.MkdirTemp("/dev/shm", "printmaster-permissions-")
	if err != nil {
		t.Skipf("separate temporary filesystem unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	installed := filepath.Join(t.TempDir(), "agent")
	stage := filepath.Join(dir, "candidate")
	if err := os.WriteFile(installed, []byte("previous binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("new binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	var oldStat, newStat syscall.Stat_t
	if err := syscall.Stat(installed, &oldStat); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(stage, &newStat); err != nil {
		t.Fatal(err)
	}
	if oldStat.Dev == newStat.Dev {
		t.Skip("fixture paths are not on separate filesystems")
	}
	err = (&Manager{binaryPath: installed}).applyUpdateUnix(stage)
	if err == nil || !strings.Contains(err.Error(), "cannot cross filesystems") {
		t.Fatalf("cross-filesystem replacement was not explicitly blocked: %v", err)
	}
	for path, want := range map[string]string{installed: "previous binary", stage: "new binary"} {
		if body, err := os.ReadFile(path); err != nil || string(body) != want {
			t.Fatalf("blocked cross-filesystem handoff changed %s: %q %v", path, body, err)
		}
	}
}
