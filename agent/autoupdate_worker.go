package main

import (
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/autoupdate"
	"printmaster/common/logger"
	"printmaster/common/updatepolicy"
	wscommon "printmaster/common/ws"
)

// Provision public root metadata during owner setup; null deliberately blocks
// updates. Never replace this with a Server-delivered root or a disposable key.
//go:embed update_publisher_root.json
var updatePublisherRoot []byte

// autoUpdateConfigProvider wraps agentConfig to provide the interface.
type autoUpdateConfigProvider struct {
	mu  sync.RWMutex
	cfg *AgentConfig
}

func (p *autoUpdateConfigProvider) GetAutoUpdateMode() updatepolicy.AgentOverrideMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cfg == nil {
		return updatepolicy.AgentOverrideInherit
	}
	return p.cfg.AutoUpdate.GetAutoUpdateMode()
}

func (p *autoUpdateConfigProvider) GetLocalPolicy() updatepolicy.PolicySpec {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cfg == nil {
		return defaultLocalPolicySpec()
	}
	return p.cfg.AutoUpdate.GetLocalPolicy()
}

func (p *autoUpdateConfigProvider) SetConfig(cfg *AgentConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
}

// fleetPolicyProvider stores fleet policy received from server.
type fleetPolicyProvider struct {
	mu     sync.RWMutex
	policy *updatepolicy.FleetUpdatePolicy
}

func (p *fleetPolicyProvider) GetFleetPolicy() *updatepolicy.FleetUpdatePolicy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.policy
}

func (p *fleetPolicyProvider) SetFleetPolicy(policy *updatepolicy.FleetUpdatePolicy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.policy = policy
}

// sendUpdateProgress sends an update progress message via local SSE and to the server via WebSocket.
func sendUpdateProgress(status autoupdate.Status, targetVersion string, progress int, message string, err error) {
	data := map[string]interface{}{
		"status":   string(status),
		"progress": progress,
		"message":  message,
	}
	if targetVersion != "" {
		data["target_version"] = targetVersion
	}
	if err != nil {
		data["error"] = err.Error()
	}

	// Broadcast to local SSE clients (agent UI)
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{
			Type: "update_progress",
			Data: data,
		})
	}

	// Also send to server via WebSocket if connected
	uploadWorkerMu.RLock()
	worker := uploadWorker
	uploadWorkerMu.RUnlock()

	if worker == nil {
		return
	}

	wsClient := worker.WSClient()
	if wsClient == nil || !wsClient.IsConnected() {
		return
	}

	msg := wscommon.Message{
		Type:      wscommon.MessageTypeUpdateProgress,
		Data:      data,
		Timestamp: time.Now(),
	}

	if sendErr := wsClient.SendMessage(msg); sendErr != nil {
		worker.logger.Warn("Update progress delivery failed", "error", sendErr)
	}
}

func sendUpdateSnapshot(snapshot updatepolicy.ProgressSnapshot) {
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{Type: "update_progress", Data: snapshot.Data()})
	}
	uploadWorkerMu.RLock()
	worker := uploadWorker
	uploadWorkerMu.RUnlock()
	if worker == nil {
		return
	}
	wsClient := worker.WSClient()
	if wsClient != nil && wsClient.IsConnected() {
		if err := wsClient.SendMessage(wscommon.Message{
			Type: wscommon.MessageTypeUpdateProgress, Data: snapshot.Data(), Timestamp: time.Now(),
		}); err != nil {
			worker.logger.Warn("Update progress delivery failed; heartbeat will replay", "error", err)
		}
	}
}

// startAutoUpdateManager initializes and starts the auto-update background worker.
func startAutoUpdateManager(
	ctx context.Context,
	serverClient *agent.ServerClient,
	configProvider *autoUpdateConfigProvider,
	fleetProvider *fleetPolicyProvider,
	dataDir string,
	currentVersion string,
	buildType string,
	configuredChannel string,
	isService bool,
	log *logger.Logger,
) (*autoupdate.Manager, error) {
	if serverClient == nil {
		return nil, nil // No server connection, no auto-update
	}

	// Determine update channel:
	// 1. If explicitly configured, use that
	// 2. Otherwise, derive from build type.
	channel := configuredChannel
	if channel == "" {
		if buildType == "dev" {
			channel = "dev"
		} else if buildType == "beta" {
			channel = "beta"
		} else {
			channel = "stable"
		}
	}

	clientAdapter := autoupdate.NewClientAdapter(serverClient)
	telemetryAdapter := autoupdate.NewTelemetryAdapter(serverClient)
	policyAdapter := autoupdate.NewPolicyAdapter(configProvider, fleetProvider)
	verifier, trustErr := serverClient.NewPublisherVerifier(updatePublisherRoot,
		filepath.Join(dataDir, "update-trust", "metadata"))
	if trustErr != nil {
		log.Error("Publisher verifier unavailable; updates will be blocked", "error", trustErr)
	}

	opts := autoupdate.Options{
		Enabled:        true,
		CurrentVersion: currentVersion,
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        channel,
		ChannelProvider: func() string {
			if settingsManager == nil {
				return ""
			}
			cfg, managed := settingsManager.baseSettings()
			if !managed {
				return ""
			}
			for _, section := range settingsManager.ManagedSections() {
				if section == "features" {
					return cfg.Features.AgentUpdateChannel
				}
			}
			return ""
		},
		DataDir:          dataDir,
		IsService:        isService,
		ServerClient:     clientAdapter,
		PolicyProvider:   policyAdapter,
		TelemetrySink:    telemetryAdapter,
		SnapshotCallback: sendUpdateSnapshot,
		Log:              log,
		// Use defaults for intervals and thresholds
	}
	// Do not put a typed nil into the verifier interface after setup failure.
	if trustErr == nil {
		opts.PublisherVerifier = verifier
	}

	manager, err := autoupdate.NewManager(opts)
	if err != nil {
		return nil, err
	}

	// Check for post-update validation (runs if we just restarted after an update)
	wasUpdated, fromVersion, validationErr := manager.ValidatePostUpdate()
	if wasUpdated {
		if validationErr != nil {
			// Update validation failed - log error and notify
			log.Error("Post-update validation FAILED",
				"error", validationErr,
				"current_version", currentVersion,
				"from_version", fromVersion)
		} else {
			log.Info("Post-update validation PASSED",
				"new_version", currentVersion,
				"previous_version", fromVersion)
		}
	} else if validationErr != nil {
		log.Error("Could not validate pending update", "error", validationErr)
	}

	manager.Start(ctx)

	log.Info("Auto-update manager started",
		"version", currentVersion,
		"platform", runtime.GOOS,
		"arch", runtime.GOARCH,
	)

	return manager, nil
}

// initAutoUpdateWorker is called after the upload worker is ready to set up auto-updates.
// It runs in a goroutine to avoid blocking startup.
func initAutoUpdateWorker(
	ctx context.Context,
	agentCfg *AgentConfig,
	dataDir string,
	isService bool,
	log *logger.Logger,
) {
	// Wait a bit for upload worker to be fully ready
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	uploadWorkerMu.RLock()
	worker := uploadWorker
	uploadWorkerMu.RUnlock()

	if worker == nil {
		log.Info("Auto-update disabled: no upload worker available (agent not connected to server)")
		return
	}

	serverClient := worker.Client()
	if serverClient == nil {
		log.Info("Auto-update disabled: no server client available")
		return
	}

	configProvider := &autoUpdateConfigProvider{cfg: agentCfg}
	fleetProvider := &fleetPolicyProvider{} // Will be populated when fleet policy arrives

	manager, err := startAutoUpdateManager(
		ctx,
		serverClient,
		configProvider,
		fleetProvider,
		dataDir,
		Version,
		BuildType,
		agentCfg.AutoUpdate.Channel,
		isService,
		log,
	)
	if err != nil {
		log.Error("Failed to start auto-update manager", "error", err)
		return
	}

	if manager != nil {
		autoUpdateManagerMu.Lock()
		autoUpdateManager = manager
		autoUpdateManagerMu.Unlock()

		// Register command handler for server-triggered updates
		agent.SetCommandHandler(func(command string, data map[string]interface{}) {
			handleServerCommand(ctx, command, data, log)
		})
		log.Info("Auto-update ready: WebSocket command handler registered")
	}
}

// handleServerCommand processes commands received from the server via WebSocket
func handleServerCommand(ctx context.Context, command string, data map[string]interface{}, log *logger.Logger) {
	log.Info("Processing server command", "command", command)

	switch command {
	case "check_update":
		autoUpdateManagerMu.RLock()
		manager := autoUpdateManager
		autoUpdateManagerMu.RUnlock()

		if manager == nil {
			log.Warn("Received check_update command but auto-update manager not available")
			sendUpdateProgress(autoupdate.StatusFailed, "", -1, "Auto-update manager not available", nil)
			return
		}

		// Trigger immediate update check
		go func() {
			log.Info("Triggering immediate update check per server request")
			if err := manager.CheckNow(ctx); err != nil {
				log.Error("Update check failed", "error", err)
			} else {
				log.Info("Update check completed")
			}
		}()

	case "cancel_update":
		autoUpdateManagerMu.RLock()
		manager := autoUpdateManager
		autoUpdateManagerMu.RUnlock()

		if manager == nil {
			log.Warn("Received cancel_update command but auto-update manager not available")
			return
		}

		if manager.Cancel() {
			log.Info("Update cancellation requested")
		} else {
			log.Warn("Unable to cancel update - may be in non-cancellable phase")
		}

	case "force_update", "install_channel":
		autoUpdateManagerMu.RLock()
		manager := autoUpdateManager
		autoUpdateManagerMu.RUnlock()

		if manager == nil {
			log.Warn("Received force_update command but auto-update manager not available")
			sendUpdateProgress(autoupdate.StatusFailed, "", -1, "Auto-update manager not available", nil)
			return
		}

		reason, _ := data["reason"].(string)
		channel := ""
		if command == "install_channel" {
			channel, _ = data["channel"].(string)
			if channel != "stable" && channel != "beta" && channel != "dev" {
				sendUpdateProgress(autoupdate.StatusFailed, "", -1, "Invalid update channel", nil)
				return
			}
		}

		go func() {
			log.Info("Triggering forced reinstall per server request", "reason", reason)
			if err := manager.ForceInstallLatestFromChannel(ctx, reason, channel); err != nil {
				log.Error("Forced reinstall failed", "error", err)
			} else {
				log.Info("Forced reinstall completed")
			}
		}()

	case "restart":
		log.Info("Received restart command from server")
		go func() {
			// Brief delay to allow WebSocket response to be sent
			time.Sleep(500 * time.Millisecond)
			log.Info("Initiating agent restart per server request")
			// Signal the service to restart
			// On Windows service, this will trigger a graceful restart
			// On systemd, this will exit and systemd will restart us
			os.Exit(0)
		}()

	default:
		log.Warn("Unknown server command", "command", command)
	}
}
