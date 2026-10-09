package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"printmaster/common/logger"
	"printmaster/common/updatepolicy"
	"printmaster/server/storage"

	"github.com/Masterminds/semver"
)

const (
	defaultCheckInterval = 6 * time.Hour
	selfUpdateDirName    = "selfupdate"
	stagingDirName       = "staging"
	backupDirName        = "backups"
	applyDirName         = "apply"
	helperDirName        = "helpers"
	logDirName           = "logs"
	defaultComponent     = "server"
	defaultChannel       = "stable"
	defaultMaxArtifacts  = 12
	defaultServiceName   = "PrintMasterServer"
)

// Options configure the self-update manager lifecycle.
type Options struct {
	Store            storage.Store
	Log              *logger.Logger
	DataDir          string
	Enabled          bool
	CheckEvery       time.Duration
	Clock            func() time.Time
	Component        string
	Channel          string
	CurrentVersion   string
	Platform         string
	Arch             string
	MaxArtifacts     int
	BinaryPath       string
	DatabaseConfig   string
	ServiceName      string
	ApplyLauncher    ApplyLauncher
	RuntimeSkipCheck func() string // Returns reason to skip, or "" to proceed. Defaults to runtimeSkipReason.
}

// Manager coordinates the server self-update workflow.
type Manager struct {
	store            storage.Store
	log              *logger.Logger
	stateDir         string
	interval         time.Duration
	clock            func() time.Time
	disabledReason   string
	component        string
	channel          string
	currentVersion   string
	currentSemver    *semver.Version
	versionParseErr  error
	platform         string
	arch             string
	maxArtifacts     int
	binaryPath       string
	databaseConfig   string
	serviceName      string
	applier          ApplyLauncher
	runtimeSkipCheck func() string
}

// NewManager validates options, prepares state directories, and returns a manager instance.
func NewManager(opts Options) (*Manager, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("storage store is required")
	}
	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "printmaster-server")
	}
	interval := opts.CheckEvery
	if interval <= 0 {
		interval = defaultCheckInterval
	}
	clock := opts.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	component := strings.ToLower(strings.TrimSpace(opts.Component))
	if component == "" {
		component = defaultComponent
	}
	channel := normalizeChannel(opts.Channel)
	currentVersion := strings.TrimSpace(opts.CurrentVersion)
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = runtime.GOOS
	}
	arch := strings.TrimSpace(opts.Arch)
	if arch == "" {
		arch = runtime.GOARCH
	}
	maxArtifacts := opts.MaxArtifacts
	if maxArtifacts <= 0 {
		maxArtifacts = defaultMaxArtifacts
	}
	stateDir := filepath.Join(dataDir, selfUpdateDirName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to prepare self-update directory: %w", err)
	}
	for _, dir := range []string{stagingDirName, backupDirName, applyDirName, helperDirName, logDirName} {
		if err := os.MkdirAll(filepath.Join(stateDir, dir), 0o755); err != nil {
			return nil, fmt.Errorf("failed to prepare self-update subdirectory %s: %w", dir, err)
		}
	}
	binaryPath := strings.TrimSpace(opts.BinaryPath)
	if binaryPath == "" {
		if exe, err := os.Executable(); err == nil {
			binaryPath = exe
		}
	}
	databaseConfig := strings.TrimSpace(opts.DatabaseConfig)
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	var parsed *semver.Version
	var versionErr error
	if currentVersion != "" {
		if v, err := semver.NewVersion(currentVersion); err == nil {
			parsed = v
		} else {
			versionErr = err
		}
	}
	var applier ApplyLauncher
	if opts.ApplyLauncher != nil {
		applier = opts.ApplyLauncher
	} else {
		if binaryPath == "" {
			return nil, fmt.Errorf("binary path is required for self-update")
		}
		if databaseConfig == "" {
			return nil, fmt.Errorf("database config required for self-update apply helper")
		}
		applier = newHelperLauncher(stateDir, binaryPath, serviceName, opts.Log)
	}
	runtimeSkipCheck := opts.RuntimeSkipCheck
	if runtimeSkipCheck == nil {
		runtimeSkipCheck = runtimeSkipReason
	}
	reason := disableReason(opts.Enabled)
	return &Manager{
		store:            opts.Store,
		log:              opts.Log,
		stateDir:         stateDir,
		interval:         interval,
		clock:            clock,
		disabledReason:   reason,
		component:        component,
		channel:          channel,
		currentVersion:   currentVersion,
		currentSemver:    parsed,
		versionParseErr:  versionErr,
		platform:         strings.ToLower(platform),
		arch:             strings.ToLower(arch),
		maxArtifacts:     maxArtifacts,
		binaryPath:       binaryPath,
		databaseConfig:   databaseConfig,
		serviceName:      serviceName,
		applier:          applier,
		runtimeSkipCheck: runtimeSkipCheck,
	}, nil
}

// Status returns current self-update manager status.
type Status struct {
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	CurrentVersion string `json:"current_version"`
	Component      string `json:"component"`
	Channel        string `json:"channel"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
	CheckInterval  string `json:"check_interval"`
	IsContainer    bool   `json:"is_container"`
}

// Status returns the current manager status.
func (m *Manager) Status() Status {
	if m == nil {
		return Status{Enabled: false, DisabledReason: "manager not initialized", IsContainer: runningInsideContainer()}
	}
	return Status{
		Enabled:        m.disabledReason == "",
		DisabledReason: m.disabledReason,
		CurrentVersion: m.currentVersion,
		Component:      m.component,
		Channel:        m.channel,
		Platform:       m.platform,
		Arch:           m.arch,
		CheckInterval:  m.interval.String(),
		IsContainer:    runningInsideContainer(),
	}
}

// CheckNow triggers an immediate update check. Returns nil if check was initiated.
func (m *Manager) CheckNow(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("manager not initialized")
	}
	if m.disabledReason != "" {
		return fmt.Errorf("self-update disabled: %s", m.disabledReason)
	}
	go m.tick(ctx)
	return nil
}

// Start launches the background worker when enabled.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if m.disabledReason != "" {
		if m.log != nil {
			m.log.Info("Self-update manager disabled", "reason", m.disabledReason)
		}
		return
	}
	go m.run(ctx)
}

func (m *Manager) run(ctx context.Context) {
	if m.log != nil {
		m.log.Info("Self-update manager initialized", "state_dir", m.stateDir, "interval", m.interval.String())
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if m.log != nil {
				m.log.Info("Self-update manager stopping", "reason", ctx.Err())
			}
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	now := m.clock()
	run := &storage.SelfUpdateRun{
		Status:         storage.SelfUpdateStatusChecking,
		RequestedAt:    now,
		StartedAt:      now,
		CurrentVersion: m.currentVersion,
		Channel:        m.channel,
		Platform:       m.platform,
		Arch:           m.arch,
		Metadata: map[string]any{
			"component": m.component,
		},
	}
	if m.versionParseErr != nil {
		run.Metadata["current_version_error"] = m.versionParseErr.Error()
	}
	shouldFinalize := true
	if err := m.store.CreateSelfUpdateRun(ctx, run); err != nil {
		if m.log != nil {
			m.log.Warn("Self-update run creation failed", "error", err)
		}
		return
	}
	if reason := m.runtimeSkipCheck(); reason != "" {
		run.Status = storage.SelfUpdateStatusSkipped
		run.Metadata = mergeMetadata(run.Metadata, map[string]any{
			"reason": reason,
		})
		run.CompletedAt = m.clock()
		if err := m.store.UpdateSelfUpdateRun(ctx, run); err != nil && m.log != nil {
			m.log.Warn("Self-update run update failed", "error", err)
		}
		if m.log != nil {
			m.log.Info("Self-update skipped", "reason", reason)
		}
		return
	}
	defer func() {
		if !shouldFinalize {
			return
		}
		run.CompletedAt = m.clock()
		if err := m.store.UpdateSelfUpdateRun(ctx, run); err != nil && m.log != nil {
			m.log.Warn("Self-update run update failed", "error", err)
		}
	}()

	if m.currentSemver == nil {
		run.Status = storage.SelfUpdateStatusSkipped
		run.Metadata = mergeMetadata(run.Metadata, map[string]any{
			"reason": "unsupported-current-version",
		})
		if m.log != nil {
			m.log.Debug("Self-update skipped", "reason", "unsupported current version")
		}
		return
	}

	candidate, meta, err := m.selectCandidate(ctx, m.currentSemver)
	if err != nil {
		run.Status = storage.SelfUpdateStatusFailed
		run.ErrorCode = "candidate-selection"
		run.ErrorMessage = err.Error()
		run.Metadata = mergeMetadata(run.Metadata, map[string]any{"stage": "select"})
		if m.log != nil {
			m.log.Warn("Self-update candidate selection failed", "error", err)
		}
		return
	}
	if len(meta) > 0 {
		run.Metadata = mergeMetadata(run.Metadata, meta)
	}
	if candidate == nil {
		run.Status = storage.SelfUpdateStatusSkipped
		if m.log != nil {
			m.log.Debug("Self-update skipped", "reason", meta["reason"])
		}
		return
	}

	run.TargetVersion = candidate.Version
	run.ReleaseArtifactID = candidate.ID
	if err := m.stageCandidate(ctx, run, candidate); err != nil {
		run.Status = storage.SelfUpdateStatusFailed
		run.ErrorCode = "staging"
		run.ErrorMessage = err.Error()
		run.Metadata = mergeMetadata(run.Metadata, map[string]any{"stage": "prepare"})
		if m.log != nil {
			m.log.Warn("Self-update staging failed", "error", err)
		}
		return
	}
	run.Status = storage.SelfUpdateStatusStaging
	if m.log != nil {
		m.log.Info("Self-update candidate staged", "target_version", candidate.Version, "artifact_id", candidate.ID)
	}
	if err := m.beginApply(ctx, run); err != nil {
		run.Status = storage.SelfUpdateStatusFailed
		run.ErrorCode = "apply-launch"
		run.ErrorMessage = err.Error()
		run.Metadata = mergeMetadata(run.Metadata, map[string]any{"stage": "apply"})
		if m.log != nil {
			m.log.Warn("Self-update apply helper launch failed", "error", err)
		}
		return
	}
	shouldFinalize = false
	if m.log != nil {
		m.log.Info("Self-update apply helper launched", "run_id", run.ID, "target_version", run.TargetVersion)
	}
}

func (m *Manager) selectCandidate(ctx context.Context, current *semver.Version) (*storage.ReleaseArtifact, map[string]any, error) {
	artifacts, err := m.store.ListReleaseArtifacts(ctx, m.component, m.maxArtifacts)
	if err != nil {
		return nil, nil, err
	}
	meta := map[string]any{
		"checked": len(artifacts),
	}
	if len(artifacts) == 0 {
		meta["reason"] = "no-artifacts"
		return nil, meta, nil
	}
	var best *storage.ReleaseArtifact
	var bestVersion *semver.Version
	skipped := 0
	for _, artifact := range artifacts {
		if !m.matchesArtifact(artifact) {
			skipped++
			continue
		}
		candidateVersion, err := semver.NewVersion(strings.TrimSpace(artifact.Version))
		if err != nil {
			skipped++
			continue
		}
		if !candidateVersion.GreaterThan(current) {
			skipped++
			continue
		}
		if best == nil || candidateVersion.GreaterThan(bestVersion) {
			best = artifact
			bestVersion = candidateVersion
		}
	}
	meta["skipped"] = skipped
	if best == nil {
		meta["reason"] = "no-newer-version"
		return nil, meta, nil
	}
	meta["reason"] = "newer-version-available"
	meta["selected_version"] = best.Version
	meta["selected_artifact_id"] = best.ID
	return best, meta, nil
}

// matchesArtifact reports whether a cached release can replace this Server.
// Only raw binaries qualify: the Server swaps its own executable, so installer
// packages such as MSIs that share the platform/arch are never candidates.
func (m *Manager) matchesArtifact(artifact *storage.ReleaseArtifact) bool {
	if artifact == nil {
		return false
	}
	if format, ok := updatepolicy.ParseArtifactFormat(artifact.Format); !ok || format != updatepolicy.ArtifactFormatBinary {
		return false
	}
	if normalizeChannel(artifact.Channel) != m.channel {
		return false
	}
	if strings.ToLower(strings.TrimSpace(artifact.Platform)) != m.platform {
		return false
	}
	if strings.ToLower(strings.TrimSpace(artifact.Arch)) != m.arch {
		return false
	}
	return true
}

func normalizeChannel(value string) string {
	ch := strings.ToLower(strings.TrimSpace(value))
	if ch == "" {
		return defaultChannel
	}
	return ch
}

func mergeMetadata(dst map[string]any, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]any, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (m *Manager) stageCandidate(ctx context.Context, run *storage.SelfUpdateRun, artifact *storage.ReleaseArtifact) error {
	if artifact == nil {
		return fmt.Errorf("artifact is nil")
	}
	if strings.TrimSpace(artifact.CachePath) == "" {
		return fmt.Errorf("artifact cache path missing")
	}
	manifest, err := m.store.GetReleaseManifest(ctx, artifact.Component, artifact.Version, artifact.Platform, artifact.Arch, artifact.Format)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	if manifest == nil {
		return fmt.Errorf("manifest not found")
	}
	stageDir := filepath.Join(m.stateDir, stagingDirName, fmt.Sprintf("run-%d", run.ID))
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	stagePath := filepath.Join(stageDir, filepath.Base(artifact.CachePath))
	if err := copyFile(artifact.CachePath, stagePath); err != nil {
		return fmt.Errorf("copy artifact: %w", err)
	}
	if err := verifySHA(stagePath, artifact.SHA256); err != nil {
		return err
	}
	backupPath, err := m.createBackup(run)
	if err != nil {
		return err
	}
	run.Metadata = mergeMetadata(run.Metadata, map[string]any{
		"stage_path":       stagePath,
		"backup_path":      backupPath,
		"manifest_id":      manifest.ID,
		"manifest_version": manifest.ManifestVersion,
		"manifest_channel": manifest.Channel,
	})
	return nil
}

func (m *Manager) beginApply(ctx context.Context, run *storage.SelfUpdateRun) error {
	if m.applier == nil {
		return fmt.Errorf("apply launcher not configured")
	}
	stagePath, _ := run.Metadata["stage_path"].(string)
	backupPath, _ := run.Metadata["backup_path"].(string)
	if strings.TrimSpace(stagePath) == "" {
		return fmt.Errorf("stage path missing from metadata")
	}
	if strings.TrimSpace(backupPath) == "" {
		return fmt.Errorf("backup path missing from metadata")
	}
	inst := &ApplyInstruction{
		RunID:          run.ID,
		StagePath:      stagePath,
		BackupPath:     backupPath,
		BinaryPath:     m.binaryPath,
		TargetVersion:  run.TargetVersion,
		CurrentVersion: run.CurrentVersion,
		ServiceName:    m.serviceName,
		Platform:       m.platform,
		Arch:           m.arch,
		Channel:        run.Channel,
		Component:      m.component,
		DatabaseConfig: m.databaseConfig,
		StateDir:       m.stateDir,
		CreatedAt:      m.clock(),
	}
	meta, err := m.applier.Launch(run, inst)
	if err != nil {
		return err
	}
	if len(meta) > 0 {
		run.Metadata = mergeMetadata(run.Metadata, meta)
	}
	run.Status = storage.SelfUpdateStatusApplying
	run.CompletedAt = time.Time{}
	if err := m.store.UpdateSelfUpdateRun(ctx, run); err != nil {
		return err
	}
	return nil
}

func (m *Manager) createBackup(run *storage.SelfUpdateRun) (string, error) {
	path := strings.TrimSpace(m.binaryPath)
	if path == "" {
		return "", fmt.Errorf("binary path unknown")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("stat binary: %w", err)
	}
	backupDir := filepath.Join(m.stateDir, backupDirName, fmt.Sprintf("run-%d", run.ID))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	backupPath := filepath.Join(backupDir, filepath.Base(path))
	if err := copyFile(path, backupPath); err != nil {
		return "", fmt.Errorf("backup binary: %w", err)
	}
	return backupPath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func verifySHA(path, expected string) error {
	expected = strings.TrimSpace(strings.ToLower(expected))
	if expected == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := fmt.Sprintf("%x", h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("sha mismatch: expected %s got %s", expected, actual)
	}
	return nil
}
