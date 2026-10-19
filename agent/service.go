package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kardianos/service"
)

// program implements service.Interface
type program struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	svcLogger service.Logger
}

func (p *program) Start(s service.Service) error {
	// Get service logger
	p.svcLogger, _ = s.Logger(nil)
	if p.svcLogger != nil {
		p.svcLogger.Info("PrintMaster Agent service starting")
	}

	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.done = make(chan struct{})

	go p.run()
	return nil
}

func (p *program) run() {
	defer close(p.done)

	if p.svcLogger != nil {
		p.svcLogger.Info("PrintMaster Agent service running")
	}

	// Call runInteractive with context for graceful shutdown
	// Service mode doesn't provide a config path, pass empty string so
	// runInteractive will fall back to default config discovery.
	runInteractive(p.ctx, "")

	if p.svcLogger != nil {
		p.svcLogger.Info("PrintMaster Agent service stopping")
	}
}

func (p *program) Stop(s service.Service) error {
	// Service is stopping, cancel context and wait for shutdown
	if p.svcLogger != nil {
		p.svcLogger.Info("PrintMaster Agent service stop requested")
	}

	if p.cancel != nil {
		p.cancel()
	}

	// Wait for run() to finish with timeout
	timeout := time.After(30 * time.Second)
	select {
	case <-p.done:
		if p.svcLogger != nil {
			p.svcLogger.Info("PrintMaster Agent service stopped gracefully")
		}
	case <-timeout:
		if p.svcLogger != nil {
			p.svcLogger.Warning("PrintMaster Agent service stopped with timeout")
		}
	}

	return nil
}

// getServiceConfig returns the service configuration for the current platform
func getServiceConfig() *service.Config {
	// Determine service data directory based on platform
	var workingDir string
	switch runtime.GOOS {
	case "windows":
		// Windows: C:\ProgramData\PrintMaster
		workingDir = filepath.Join(os.Getenv("ProgramData"), "PrintMaster")
	case "darwin":
		// macOS: /Library/Application Support/PrintMaster
		workingDir = "/Library/Application Support/PrintMaster"
	default:
		// Linux: /var/lib/printmaster
		workingDir = "/var/lib/printmaster"
	}

	config := &service.Config{
		Name:             "PrintMasterAgent",
		DisplayName:      "PrintMaster Agent",
		Description:      "PrintMaster printer and copier fleet management agent. Discovers network printers, collects device metadata, and provides web-based management.",
		WorkingDirectory: workingDir,
		Arguments:        []string{"--service", "run"},
		Option: service.KeyValue{
			// Windows service options
			"StartType":              "automatic",
			"DelayedAutoStart":       true,
			"Dependencies":           "",
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   30,

			// Linux systemd options
			"Restart":           "on-failure",
			"RestartSec":        5,
			"SuccessExitStatus": "0 SIGTERM",
			"KillMode":          "mixed",
			"KillSignal":        "SIGTERM",
			"SendSIGKILL":       true,

			// macOS launchd options
			"RunAtLoad":     true,
			"KeepAlive":     true,
			"SessionCreate": false,
		},
	}
	if runtime.GOOS == "windows" {
		config.Executable = windowsServiceExecutablePath()
	}
	return config
}

// setupServiceDirectories creates necessary directories for service operation
func setupServiceDirectories() error {
	var dirs []string

	switch runtime.GOOS {
	case "windows":
		baseDir := filepath.Join(os.Getenv("ProgramData"), "PrintMaster")
		agentDir := filepath.Join(baseDir, "agent")
		dirs = []string{
			baseDir,
			agentDir,
			filepath.Join(agentDir, "logs"),
		}
	case "darwin":
		baseDir := "/Library/Application Support/PrintMaster"
		dirs = []string{
			baseDir,
			filepath.Join(baseDir, "logs"),
			"/var/log/printmaster",
		}
	default: // Linux
		dirs = []string{
			"/var/lib/printmaster",
			"/var/log/printmaster",
			"/etc/printmaster",
		}
	}

	for _, dir := range dirs {
		if runtime.GOOS == "windows" && (filepath.Clean(dir) == filepath.Clean(filepath.Join(os.Getenv("ProgramData"), "PrintMaster")) ||
			filepath.Clean(dir) == filepath.Clean(filepath.Join(os.Getenv("ProgramData"), "PrintMaster", "agent"))) {
			if err := protectWindowsServiceDirectory(dir); err != nil {
				return fmt.Errorf("failed to protect directory %s: %w", dir, err)
			}
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

// getServiceLogPath returns the log file path for service mode
func getServiceLogPath() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("ProgramData"), "PrintMaster", "agent", "logs", "agent.log")
	case "darwin":
		return "/var/log/printmaster/agent.log"
	default: // Linux
		return "/var/log/printmaster/agent.log"
	}
}
