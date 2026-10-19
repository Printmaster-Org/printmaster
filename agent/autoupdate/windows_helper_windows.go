//go:build windows

package autoupdate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func launchDetachedWindowsHelper(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", "Get-ExecutionPolicy").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: inspect Windows PowerShell execution policy: %w: %s",
			errInstallationNotStarted, err, strings.TrimSpace(string(output)))
	}
	if err := validateWindowsHelperPolicy(strings.TrimSpace(string(output))); err != nil {
		return fmt.Errorf("%w: %w", errInstallationNotStarted, err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release detached PowerShell process: %w", err)
	}
	return nil
}
