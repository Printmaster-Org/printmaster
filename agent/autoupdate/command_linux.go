package autoupdate

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureUpdateCommand(cmd *exec.Cmd) {
	// Signal the command group, not only sudo. Elevated descendants may
	// require privileged recovery; cancellation errors must remain visible.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
