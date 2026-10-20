package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivilegedRepositoryPackageProcessCancellation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "completed")
	path := filepath.Join(dir, "source")
	body := []byte("package")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	_, target := packageRequestTestFrame(t, body)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	outcome, err := launchRepositoryPackage(ctx, source, target, 3*time.Second, func(independent context.Context) *exec.Cmd {
		cmd := exec.CommandContext(independent, executable, "-test.run=^TestPrivilegedRepositoryPackageProcessChild$")
		cmd.Env = append(os.Environ(), "PRINTMASTER_PACKAGE_TEST_CHILD=survive cancellation", "PRINTMASTER_PACKAGE_TEST_MARKER="+marker)
		return cmd
	})
	if err == nil || outcome.Status != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled caller got actionable success: %+v %v", outcome, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "completed" {
		t.Fatalf("caller cancellation killed child or returned before reaping: %q %v", data, err)
	}
}

func TestPrivilegedRepositoryPackageProcessStartFailure(t *testing.T) {
	source, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	_, target := packageRequestTestFrame(t, []byte("package"))
	outcome, err := launchRepositoryPackage(context.Background(), source, target, time.Second, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-helper"))
	})
	if err == nil || outcome.Status != "" || !strings.Contains(err.Error(), "start native helper") {
		t.Fatalf("missing helper did not fail launch explicitly: %+v %v", outcome, err)
	}
}
