//go:build windows

package autoupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func windowsPowerShellCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("locate Windows PowerShell: %w", err)
	}
	home := filepath.Join(systemDir, "WindowsPowerShell", "v1.0")
	cmd := exec.CommandContext(ctx, filepath.Join(home, "powershell.exe"), args...)
	// PowerShell 7 paths inherited through a native parent can break 5.1
	// module loading. The helper needs only Windows' own built-in modules.
	cmd.Env = make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, "PSModulePath") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PSModulePath="+filepath.Join(home, "Modules"))
	return cmd, nil
}

func launchDetachedWindowsHelper(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	preflight, err := windowsPowerShellCommand(ctx, "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", "Get-ExecutionPolicy")
	if err != nil {
		return fmt.Errorf("%w: %w", errInstallationNotStarted, err)
	}
	output, err := preflight.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: inspect Windows PowerShell execution policy: %w: %s",
			errInstallationNotStarted, err, strings.TrimSpace(string(output)))
	}
	if err := validateWindowsHelperPolicy(strings.TrimSpace(string(output))); err != nil {
		return fmt.Errorf("%w: %w", errInstallationNotStarted, err)
	}
	cmd, err := windowsPowerShellCommand(context.Background(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
	if err != nil {
		return fmt.Errorf("%w: %w", errInstallationNotStarted, err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		// Windows PowerShell exits before running -File with DETACHED_PROCESS.
		// CREATE_NO_WINDOW runs the independent helper without a console.
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release detached PowerShell process: %w", err)
	}
	return nil
}
