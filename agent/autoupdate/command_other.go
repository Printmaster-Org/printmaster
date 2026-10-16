//go:build !linux

package autoupdate

import "os/exec"

func configureUpdateCommand(cmd *exec.Cmd) {}
