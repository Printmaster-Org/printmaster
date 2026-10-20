package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// runUpdateCommand bounds unattended commands and their diagnostic output.
// Package repair may still be required after interruption; a deadline is not
// a substitute for an independent installer/recovery transaction.
func (m *Manager) runUpdateCommand(timeout time.Duration, env []string, name string, args ...string) ([]byte, error) {
	return m.runUpdateCommandIn(context.Background(), timeout, "", env, name, args...)
}

func (m *Manager) runUpdateCommandIn(parent context.Context, timeout time.Duration, dir string, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 5 * time.Second
	configureUpdateCommand(cmd)
	output := &commandOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	started := time.Now()
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errors.Join(err, fmt.Errorf("update command deadline: %w", ctx.Err()))
	}
	if err != nil {
		m.logWarn("Update command failed", "command", name, "duration", time.Since(started).Round(time.Millisecond), "timeout", timeout, "error", err)
	} else {
		m.logDebug("Update command completed", "command", name, "duration", time.Since(started).Round(time.Millisecond))
	}
	return output.bytes(), err
}

type commandOutput struct {
	mu   sync.Mutex
	data []byte
}

func (o *commandOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	const limit = 64 * 1024
	o.data = append(o.data, p...)
	if len(o.data) > limit {
		o.data = append([]byte(nil), o.data[len(o.data)-limit:]...)
	}
	return len(p), nil
}

func (o *commandOutput) bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.data...)
}
