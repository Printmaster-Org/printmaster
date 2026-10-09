package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"printmaster/common/logger"
	"printmaster/common/updatepolicy"

	"github.com/Masterminds/semver/v3"
)

const (
	defaultCheckInterval      = 24 * time.Hour
	defaultMinDiskSpaceMB     = 200
	defaultMaxRetries         = 5
	defaultRetryBaseDelay     = 2 * time.Second
	defaultRetryMaxDelay      = 5 * time.Minute
	defaultHealthCheckDelay   = 10 * time.Second
	defaultHealthCheckTimeout = 30 * time.Second
	updateDirName             = "autoupdate"
	stagingDirName            = "staging"
	backupDirName             = "backups"
	downloadDirName           = "downloads"
	pendingUpdateFile         = "pending_update.json" // Stores expected version for post-restart validation
)

// PendingUpdateState stores information about an in-progress update for post-restart validation.
type PendingUpdateState struct {
	TargetVersion string    `json:"target_version"`
	FromVersion   string    `json:"from_version"`
	StartedAt     time.Time `json:"started_at"`
	Method        string    `json:"method"` // "binary", "msi", "dnf", "apt", "yum"
}

// ProgressCallback is invoked when update status changes, allowing real-time
// reporting to the server/UI. The callback receives the current status, target
// version, progress percentage (0-100, -1 if unknown), and optional message/error.
type ProgressCallback func(status Status, targetVersion string, progress int, message string, err error)

// DownloadProgressCallback is invoked during download to report bytes progress.
// percent is 0-100, bytesRead is total bytes downloaded so far.
type DownloadProgressCallback func(percent int, bytesRead int64)

// Options configure the auto-update manager.
type Options struct {
	Log              *logger.Logger
	DataDir          string
	Enabled          bool
	CurrentVersion   string
	Platform         string
	Arch             string
	Channel          string
	ChannelProvider  func() string // Optional managed channel, read at each operation.
	BinaryPath       string
	ServiceName      string
	IsService        bool // True if running as a Windows service (determines update restart method)
	MinDiskSpaceMB   int64
	MaxRetries       int
	Clock            func() time.Time
	ServerClient     UpdateClient
	PolicyProvider   PolicyProvider
	TelemetrySink    TelemetrySink
	ProgressCallback ProgressCallback
}

// UpdateClient abstracts the server communication needed for updates.
type UpdateClient interface {
	// GetLatestManifest fetches the signed manifest for the latest available version.
	// GetLatestManifest returns the newest manifest for the channel and
	// artifact format ("binary" or "msi") this install can apply.
	GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error)
	// DownloadArtifact downloads the artifact to the specified path, supporting range requests.
	DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error)
	// DownloadArtifactWithProgress downloads with progress reporting callback.
	DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error)
}

// PolicyProvider resolves the effective update policy.
type PolicyProvider interface {
	EffectivePolicy() (updatepolicy.PolicySpec, updatepolicy.PolicySource, bool)
}

// TelemetrySink reports update status to the server.
type TelemetrySink interface {
	ReportUpdateStatus(ctx context.Context, payload TelemetryPayload) error
}

// Manager coordinates agent auto-update operations.
type Manager struct {
	log              *logger.Logger
	stateDir         string
	enabled          bool
	disabledReason   string
	currentVersion   string
	currentSemver    *semver.Version
	platform         string
	arch             string
	channel          string
	channelProvider  func() string
	binaryPath       string
	serviceName      string
	isService        bool
	minDiskSpace     int64
	maxRetries       int
	clock            func() time.Time
	client           UpdateClient
	policyProvider   PolicyProvider
	telemetry        TelemetrySink
	restartFn        func() error
	launchHelperFn   func(helperPath string) error // For testing: allows mocking the helper launch
	progressCallback ProgressCallback

	// Package manager support (Linux dpkg/apt/dnf/rpm)
	usePackageManager bool   // True if binary is managed by a package manager
	packageName       string // Package name (e.g., "printmaster-agent")
	packageManager    string // Package manager type: "apt", "dnf", or ""

	// MSI support (Windows)
	useMSI         bool   // True if installed via MSI
	msiProductCode string // MSI Product Code GUID for upgrades

	// operationMu owns the entire check/install lifecycle, including pending
	// transitions where status alone does not indicate exclusive ownership.
	operationMu    sync.Mutex
	mu             sync.RWMutex
	status         Status
	lastCheck      time.Time
	nextCheck      time.Time
	latestVersion  string
	latestManifest *UpdateManifest
	currentRun     *UpdateRun
	cancelled      bool

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewManager creates an auto-update manager with the provided options.
func NewManager(opts Options) (*Manager, error) {
	if opts.CurrentVersion == "" {
		return nil, fmt.Errorf("current version is required")
	}

	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "printmaster-agent")
	}

	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = runtime.GOOS
	}
	arch := strings.TrimSpace(opts.Arch)
	if arch == "" {
		arch = runtime.GOARCH
	}
	channel := strings.ToLower(strings.TrimSpace(opts.Channel))
	if channel == "" {
		channel = "stable"
	}

	currentSemver, parseErr := parseSemverVersion(opts.CurrentVersion)
	if parseErr != nil {
		// Log but don't fail - we can still check for updates by string comparison
		if opts.Log != nil {
			opts.Log.Warn("Failed to parse current version as semver", "version", opts.CurrentVersion, "error", parseErr)
		}
	}

	stateDir := filepath.Join(dataDir, updateDirName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create update state directory: %w", err)
	}
	for _, sub := range []string{stagingDirName, backupDirName, downloadDirName} {
		if err := os.MkdirAll(filepath.Join(stateDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create %s directory: %w", sub, err)
		}
	}

	minDiskSpace := opts.MinDiskSpaceMB
	if minDiskSpace <= 0 {
		minDiskSpace = defaultMinDiskSpaceMB
	}
	maxRetries := opts.MaxRetries
	if maxRetries <= 0 {
		maxRetries = defaultMaxRetries
	}
	clock := opts.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}

	var disabledReason string
	if !opts.Enabled {
		disabledReason = "disabled by configuration"
	}
	if opts.ServerClient == nil {
		disabledReason = "no server client configured"
		opts.Enabled = false
	}

	binaryPath := opts.BinaryPath
	if binaryPath == "" {
		exec, err := os.Executable()
		if err == nil {
			binaryPath = exec
		}
	}

	// Check if binary is managed by package manager or if directory is writable
	var usePackageManager bool
	var packageName string
	var packageManager string
	if opts.Enabled && binaryPath != "" && runtime.GOOS != "windows" {
		binaryDir := filepath.Dir(binaryPath)
		pkgName, pkgMgr, reason := checkBinaryUpdateMethod(binaryPath, binaryDir, opts.Log)
		if pkgName != "" {
			// Binary managed by package manager
			usePackageManager = true
			packageName = pkgName
			packageManager = pkgMgr
			if opts.Log != nil {
				opts.Log.Info("Auto-update will use package manager",
					"package", pkgName, "method", pkgMgr)
			}
		} else if reason != "" {
			// Not writable and not package-managed - disable updates
			disabledReason = reason
			opts.Enabled = false
		}
	}

	// Check for MSI installation on Windows
	var useMSI bool
	var msiProductCode string
	if opts.Enabled && runtime.GOOS == "windows" {
		productCode, isMSI := checkMSIInstallation()
		if isMSI {
			useMSI = true
			msiProductCode = productCode
			if opts.Log != nil {
				opts.Log.Info("Auto-update will use MSI installer",
					"product_code", productCode)
			}
		}
	}

	mgr := &Manager{
		log:               opts.Log,
		stateDir:          stateDir,
		enabled:           opts.Enabled,
		disabledReason:    disabledReason,
		currentVersion:    opts.CurrentVersion,
		currentSemver:     currentSemver,
		platform:          platform,
		arch:              arch,
		channel:           channel,
		channelProvider:   opts.ChannelProvider,
		binaryPath:        binaryPath,
		serviceName:       opts.ServiceName,
		isService:         opts.IsService,
		minDiskSpace:      minDiskSpace,
		maxRetries:        maxRetries,
		clock:             clock,
		client:            opts.ServerClient,
		policyProvider:    opts.PolicyProvider,
		telemetry:         opts.TelemetrySink,
		progressCallback:  opts.ProgressCallback,
		usePackageManager: usePackageManager,
		packageName:       packageName,
		packageManager:    packageManager,
		useMSI:            useMSI,
		msiProductCode:    msiProductCode,
		status:            StatusIdle,
		stopCh:            make(chan struct{}),
	}

	mgr.restartFn = mgr.restartService

	return mgr, nil
}

// Start begins the background update worker.
func (m *Manager) Start(ctx context.Context) {
	m.wg.Add(1)
	go m.runLoop(ctx)
}

// Stop gracefully shuts down the update worker.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// Status returns the current manager status for UI/API consumption.
func (m *Manager) Status() ManagerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ptrIfNonZero := func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	}

	checkIntervalDays := 7
	policySource := ""
	if m.policyProvider != nil {
		spec, source, ok := m.policyProvider.EffectivePolicy()
		if ok && spec.UpdateCheckDays > 0 {
			checkIntervalDays = spec.UpdateCheckDays
		}
		policySource = string(source)
	}

	return ManagerStatus{
		Enabled:           m.enabled,
		DisabledReason:    m.disabledReason,
		CurrentVersion:    m.currentVersion,
		LatestVersion:     m.latestVersion,
		UpdateAvailable:   m.latestManifest != nil && m.latestVersion != m.currentVersion,
		Status:            m.status,
		LastCheckAt:       ptrIfNonZero(m.lastCheck),
		NextCheckAt:       ptrIfNonZero(m.nextCheck),
		PolicySource:      policySource,
		CheckIntervalDays: checkIntervalDays,
		Channel:           m.operationChannel(context.Background()),
		ChannelNote:       m.channelNote(context.Background()),
		Platform:          m.platform,
		Arch:              m.arch,
		UsePackageManager: m.usePackageManager,
		PackageName:       m.packageName,
		UseMSI:            m.useMSI,
	}
}

// CheckNow triggers an immediate update check.
func (m *Manager) CheckNow(ctx context.Context) error {
	m.mu.Lock()
	if m.status != StatusIdle && m.status != StatusPending {
		status := m.status
		m.mu.Unlock()
		return fmt.Errorf("update operation already in progress: %s", status)
	}
	m.mu.Unlock()

	return m.performCheck(ctx)
}

// ForceInstallLatest downloads and installs the latest manifest even when the
// version matches the current build or falls outside normal policy/maintenance windows.
func (m *Manager) ForceInstallLatest(ctx context.Context, reason string) error {
	return m.ForceInstallLatestFromChannel(ctx, reason, "")
}

type manualUpdateChannelKey struct{}

// ForceInstallLatestFromChannel selects a channel for this operation only.
// Scheduled policy and the configured channel are not changed by a manual install.
func (m *Manager) ForceInstallLatestFromChannel(ctx context.Context, reason, channel string) error {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "" && !isKnownChannel(channel) {
		return fmt.Errorf("invalid update channel: %s", channel)
	}
	if err := m.validateExplicitChannel(channel); err != nil {
		m.logWarn("Rejected manual update channel", "channel", channel, "error", err)
		m.reportTelemetry(ctx, StatusFailed, ErrCodeChannelUnsupported, err.Error())
		return err
	}
	if channel != "" {
		ctx = context.WithValue(ctx, manualUpdateChannelKey{}, channel)
		ctx = updatepolicy.WithExplicitChannel(ctx)
	}
	if err := m.acquireOperation(); err != nil {
		return err
	}
	defer m.operationMu.Unlock()

	if !m.enabled {
		if m.disabledReason != "" {
			return fmt.Errorf("auto-update disabled: %s", m.disabledReason)
		}
		return fmt.Errorf("auto-update disabled")
	}

	if m.client == nil {
		return fmt.Errorf("update client not configured")
	}

	m.mu.Lock()
	if m.status != StatusIdle && m.status != StatusPending {
		status := m.status
		m.mu.Unlock()
		return fmt.Errorf("update operation already in progress: %s", status)
	}
	m.status = StatusChecking
	m.mu.Unlock()

	defer func() {
		m.mu.RLock()
		currentStatus := m.status
		m.mu.RUnlock()
		if currentStatus == StatusChecking {
			m.setStatus(StatusIdle)
		}
	}()

	manifest, err := m.fetchManifest(ctx)
	if err != nil {
		m.reportTelemetry(ctx, StatusFailed, manifestErrorCode(err), err.Error())
		return fmt.Errorf("failed to fetch manifest: %w", err)
	}
	if manifest == nil {
		return fmt.Errorf("no manifest available")
	}
	if channel != "" && manifest.Channel != channel {
		return fmt.Errorf("manifest channel %q does not match requested channel %q", manifest.Channel, channel)
	}

	m.mu.Lock()
	m.lastCheck = m.clock()
	m.latestVersion = manifest.Version
	m.latestManifest = manifest
	m.mu.Unlock()

	if reason == "" {
		reason = "manual"
	}
	m.logInfo("Force update requested", "current", m.currentVersion, "target", manifest.Version, "reason", reason)

	if err := m.checkDiskSpace(manifest.SizeBytes); err != nil {
		m.reportTelemetry(ctx, StatusFailed, ErrCodeDiskSpace, err.Error())
		return err
	}

	m.setStatus(StatusPending)
	return m.executeUpdate(ctx, manifest)
}

func (m *Manager) runLoop(ctx context.Context) {
	defer m.wg.Done()

	if !m.enabled {
		m.logDebug("Auto-update worker disabled", "reason", m.disabledReason)
		return
	}

	// Calculate initial check delay with jitter
	m.scheduleNextCheck()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-time.After(m.timeUntilNextCheck()):
			if err := m.performCheck(ctx); err != nil {
				m.logWarn("Update check failed", "error", err)
			}
			m.scheduleNextCheck()
		}
	}
}

func (m *Manager) scheduleNextCheck() {
	m.mu.Lock()
	defer m.mu.Unlock()

	interval := defaultCheckInterval
	if m.policyProvider != nil {
		spec, _, ok := m.policyProvider.EffectivePolicy()
		if ok && spec.UpdateCheckDays > 0 {
			interval = time.Duration(spec.UpdateCheckDays) * 24 * time.Hour
		}
	}

	// Add jitter (up to 10% of interval)
	jitter := time.Duration(rand.Int63n(int64(interval / 10)))
	m.nextCheck = m.clock().Add(interval + jitter)
}

func (m *Manager) timeUntilNextCheck() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	until := time.Until(m.nextCheck)
	if until < 0 {
		until = time.Minute // Fallback minimum
	}
	return until
}

func (m *Manager) performCheck(ctx context.Context) error {
	if err := m.acquireOperation(); err != nil {
		return err
	}
	defer m.operationMu.Unlock()

	m.setStatus(StatusChecking)
	defer func() {
		m.mu.RLock()
		status := m.status
		m.mu.RUnlock()
		if status == StatusChecking {
			m.setStatus(StatusIdle)
		}
	}()

	// Check if we're within maintenance window
	if !m.isInMaintenanceWindow() {
		m.logDebug("Skipping update check - outside maintenance window")
		m.setStatus(StatusIdle)
		return nil
	}

	// Check effective policy
	if m.policyProvider != nil {
		_, source, enabled := m.policyProvider.EffectivePolicy()
		if !enabled {
			m.logDebug("Auto-update disabled by policy", "source", source)
			m.setStatus(StatusIdle)
			return nil
		}
	}

	// Fetch latest manifest from server
	manifest, err := m.fetchManifest(ctx)
	if err != nil {
		m.reportTelemetry(ctx, StatusFailed, manifestErrorCode(err), err.Error())
		return fmt.Errorf("failed to fetch manifest: %w", err)
	}

	m.mu.Lock()
	m.lastCheck = m.clock()
	m.latestVersion = manifest.Version
	m.latestManifest = manifest
	m.mu.Unlock()

	// Check if update is needed
	if !m.isUpdateNeeded(manifest) {
		m.logDebug("No update needed", "current", m.currentVersion, "latest", manifest.Version)
		return nil
	}

	m.logInfo("Update available", "current", m.currentVersion, "target", manifest.Version)

	// Pre-flight: disk space check
	if err := m.checkDiskSpace(manifest.SizeBytes); err != nil {
		m.reportTelemetry(ctx, StatusFailed, ErrCodeDiskSpace, err.Error())
		return err
	}

	// Proceed with update
	m.setStatus(StatusPending)
	return m.executeUpdate(ctx, manifest)
}

// acquireOperation rejects overlapping manual, scheduled, and forced work.
// Status is presentation state, not an operation lock (pending is admissible).
func (m *Manager) acquireOperation() error {
	if m.operationMu.TryLock() {
		return nil
	}
	m.mu.RLock()
	status := m.status
	m.mu.RUnlock()
	return fmt.Errorf("update operation already in progress: %s", status)
}

func (m *Manager) executeUpdate(ctx context.Context, manifest *UpdateManifest) error {
	// If using package manager, use simplified flow (no download needed)
	if m.usePackageManager && m.packageName != "" {
		return m.executeUpdateViaPackageManager(ctx, manifest)
	}

	run := &UpdateRun{
		ID:             fmt.Sprintf("run-%d", m.clock().UnixNano()),
		Status:         StatusDownloading,
		RequestedAt:    m.clock(),
		CurrentVersion: m.currentVersion,
		TargetVersion:  manifest.Version,
		Channel:        m.operationChannel(ctx),
		Platform:       m.platform,
		Arch:           m.arch,
		SizeBytes:      manifest.SizeBytes,
	}

	m.mu.Lock()
	m.currentRun = run
	m.cancelled = false // Reset cancelled flag for new run
	m.mu.Unlock()

	// Download phase
	m.setStatusWithProgress(StatusDownloading, 0, "Starting download...")
	run.StartedAt = m.clock()
	m.reportTelemetry(ctx, StatusDownloading, "", "")

	// Check for cancellation before download
	if m.IsCancelled() {
		run.Status = StatusCancelled
		run.CompletedAt = m.clock()
		m.setStatus(StatusCancelled)
		return fmt.Errorf("update cancelled by user")
	}

	downloadPath := filepath.Join(m.stateDir, downloadDirName, fmt.Sprintf("agent-%s-%s-%s%s",
		manifest.Version, m.platform, m.arch, m.binaryExtension()))

	downloadStart := m.clock()
	if err := m.downloadWithRetry(ctx, manifest, downloadPath); err != nil {
		// Check if this was a cancellation
		if m.IsCancelled() {
			run.Status = StatusCancelled
			run.CompletedAt = m.clock()
			m.setStatus(StatusCancelled)
			os.Remove(downloadPath)
			return fmt.Errorf("update cancelled by user")
		}
		run.Status = StatusFailed
		run.ErrorCode = ErrCodeDownloadFailed
		run.ErrorMessage = err.Error()
		run.CompletedAt = m.clock()
		m.setStatusWithError(StatusFailed, ErrCodeDownloadFailed, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeDownloadFailed, err.Error())
		return err
	}
	run.DownloadedAt = m.clock()
	run.DownloadTimeMs = run.DownloadedAt.Sub(downloadStart).Milliseconds()

	// Check for cancellation after download
	if m.IsCancelled() {
		run.Status = StatusCancelled
		run.CompletedAt = m.clock()
		m.setStatus(StatusCancelled)
		os.Remove(downloadPath)
		return fmt.Errorf("update cancelled by user")
	}

	// Verify hash (50-55% of overall progress)
	m.setStatusWithProgress(StatusDownloading, 52, "Verifying download...")
	if err := m.verifyHash(downloadPath, manifest.SHA256); err != nil {
		run.Status = StatusFailed
		run.ErrorCode = ErrCodeHashMismatch
		run.ErrorMessage = err.Error()
		run.CompletedAt = m.clock()
		m.setStatusWithError(StatusFailed, ErrCodeHashMismatch, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeHashMismatch, err.Error())
		os.Remove(downloadPath)
		return err
	}

	// Check for cancellation before staging
	if m.IsCancelled() {
		run.Status = StatusCancelled
		run.CompletedAt = m.clock()
		m.setStatus(StatusCancelled)
		os.Remove(downloadPath)
		return fmt.Errorf("update cancelled by user")
	}

	// Staging phase (55-65% of overall progress)
	m.setStatusWithProgress(StatusStaging, 58, "Staging update...")
	run.Status = StatusStaging
	m.reportTelemetry(ctx, StatusStaging, "", "")

	stagingPath := filepath.Join(m.stateDir, stagingDirName, filepath.Base(downloadPath))
	if err := m.stageBinary(downloadPath, stagingPath); err != nil {
		run.Status = StatusFailed
		run.ErrorCode = ErrCodeStagingFailed
		run.ErrorMessage = err.Error()
		run.CompletedAt = m.clock()
		m.setStatusWithError(StatusFailed, ErrCodeStagingFailed, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeStagingFailed, err.Error())
		return err
	}

	// Last chance to cancel before applying (point of no return after this)
	if m.IsCancelled() {
		run.Status = StatusCancelled
		run.CompletedAt = m.clock()
		m.setStatus(StatusCancelled)
		os.Remove(stagingPath)
		return fmt.Errorf("update cancelled by user")
	}

	// Backup current binary
	backupPath := filepath.Join(m.stateDir, backupDirName, fmt.Sprintf("agent-%s-%s-%s%s",
		m.currentVersion, m.platform, m.arch, m.binaryExtension()))
	if err := m.backupBinary(backupPath); err != nil {
		m.logWarn("Failed to backup current binary", "error", err)
		// Continue anyway - new install may still work
	}

	// Apply phase - NO CANCELLATION AFTER THIS POINT (65-80% of overall progress)
	m.setStatusWithProgress(StatusApplying, 68, "Applying update (cannot cancel)...")
	run.Status = StatusApplying
	m.reportTelemetry(ctx, StatusApplying, "", "")

	if err := m.applyUpdate(stagingPath); err != nil {
		run.Status = StatusFailed
		run.ErrorCode = ErrCodeApplyFailed
		run.ErrorMessage = err.Error()
		run.CompletedAt = m.clock()
		m.setStatusWithError(StatusFailed, ErrCodeApplyFailed, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeApplyFailed, err.Error())
		// Attempt rollback
		if rollbackErr := m.rollback(backupPath); rollbackErr != nil {
			m.logError("Rollback failed", "error", rollbackErr)
		}
		return err
	}

	// Success - about to restart (80% of overall progress, new agent will continue from 85%)
	run.Status = StatusSucceeded
	run.CompletedAt = m.clock()
	m.setStatusWithProgress(StatusRestarting, 80, "Restarting agent...")
	m.reportTelemetry(ctx, StatusSucceeded, "", "")

	m.logInfo("Update applied successfully", "from", m.currentVersion, "to", manifest.Version)

	// Write pending update state for post-restart validation
	updateMethod := "binary"
	if strings.HasSuffix(stagingPath, ".msi") {
		updateMethod = "msi"
	}
	if err := m.writePendingUpdateState(manifest.Version, updateMethod); err != nil {
		m.logWarn("Failed to write pending update state (update will proceed)", "error", err)
	}

	// Trigger restart (this function likely won't return outside of tests)
	if m.restartFn != nil {
		return m.restartFn()
	}
	return m.restartService()
}

// artifactFormat is the packaging this install can apply. MSI installs must
// receive the MSI so Windows Installer keeps owning the files; every other
// install swaps the raw binary. Package-managed Linux installs also ask for
// the binary manifest, which they use only for version discovery.
func (m *Manager) artifactFormat() updatepolicy.ArtifactFormat {
	if m.useMSI {
		return updatepolicy.ArtifactFormatMSI
	}
	return updatepolicy.ArtifactFormatBinary
}

// fetchManifest requests the newest manifest for this install's channel and
// artifact format, and rejects a manifest for a different format so an MSI
// install never tries to apply a raw binary (or the reverse).
func (m *Manager) fetchManifest(ctx context.Context) (*UpdateManifest, error) {
	ctx = m.manifestContext(ctx)
	format := m.artifactFormat()
	manifest, err := m.client.GetLatestManifest(ctx, "agent", m.platform, m.arch, m.operationChannel(ctx), format.String())
	if err != nil || manifest == nil {
		return manifest, err
	}
	if err := validateManifestFormat(manifest, format); err != nil {
		m.logWarn("Rejected update manifest", "requested_format", format, "manifest_format", manifest.Format, "version", manifest.Version, "error", err)
		return nil, err
	}
	return manifest, nil
}

// errManifestFormat marks a manifest whose artifact format cannot be applied
// by this install, so telemetry reports MANIFEST_ERROR instead of a Server
// outage.
var errManifestFormat = errors.New("update manifest format mismatch")

// manifestErrorCode maps a manifest fetch failure to its telemetry code.
func manifestErrorCode(err error) string {
	if errors.Is(err, errManifestFormat) {
		return ErrCodeManifestError
	}
	return ErrCodeServerError
}

// validateManifestFormat checks a Server's manifest against the requested
// format. Servers that predate formats omit the field and only ever serve
// binaries, which is acceptable for binary installs but not for MSI installs.
func validateManifestFormat(manifest *UpdateManifest, requested updatepolicy.ArtifactFormat) error {
	if strings.TrimSpace(manifest.Format) == "" {
		if requested == updatepolicy.ArtifactFormatBinary {
			return nil
		}
		return fmt.Errorf("%w: server did not return a %s artifact; upgrade the PrintMaster Server to deliver %s updates", errManifestFormat, requested, requested)
	}
	got, ok := updatepolicy.ParseArtifactFormat(manifest.Format)
	if !ok || got != requested {
		return fmt.Errorf("%w: got %q, requested %q", errManifestFormat, manifest.Format, requested)
	}
	return nil
}

// executeUpdateViaPackageManager performs an update using apt-get/dnf/yum.
// This skips download/staging phases since the package manager handles everything.
func (m *Manager) executeUpdateViaPackageManager(ctx context.Context, manifest *UpdateManifest) error {
	run := &UpdateRun{
		ID:             fmt.Sprintf("run-%d", m.clock().UnixNano()),
		Status:         StatusApplying,
		RequestedAt:    m.clock(),
		CurrentVersion: m.currentVersion,
		TargetVersion:  manifest.Version,
		Channel:        m.operationChannel(ctx),
		Platform:       m.platform,
		Arch:           m.arch,
	}

	m.mu.Lock()
	m.currentRun = run
	m.cancelled = false
	m.mu.Unlock()

	run.StartedAt = m.clock()

	// Check for cancellation before starting
	if m.IsCancelled() {
		run.Status = StatusCancelled
		run.CompletedAt = m.clock()
		m.setStatus(StatusCancelled)
		return fmt.Errorf("update cancelled by user")
	}

	// Progress callback for package manager phases
	progressFn := func(percent int, message string) {
		m.setStatusWithProgress(StatusApplying, percent, message)
	}

	// Initial status
	progressFn(0, fmt.Sprintf("Starting update to %s via %s...", manifest.Version, m.packageManager))
	run.Status = StatusApplying
	m.reportTelemetry(ctx, StatusApplying, "", "")

	if err := m.applyUpdateViaPackageManager(manifest.Version, progressFn); err != nil {
		run.Status = StatusFailed
		run.ErrorCode = ErrCodeApplyFailed
		run.ErrorMessage = err.Error()
		run.CompletedAt = m.clock()
		m.setStatusWithError(StatusFailed, ErrCodeApplyFailed, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeApplyFailed, err.Error())
		return err
	}

	// Success - about to restart (80% of overall progress, new agent will continue from 85%)
	run.Status = StatusSucceeded
	run.CompletedAt = m.clock()
	m.setStatusWithProgress(StatusRestarting, 80, "Restarting agent...")
	m.reportTelemetry(ctx, StatusSucceeded, "", "")

	m.logInfo("Update applied successfully via package manager",
		"from", m.currentVersion, "to", manifest.Version, "package", m.packageName, "manager", m.packageManager)

	// Write pending update state for post-restart validation
	if err := m.writePendingUpdateState(manifest.Version, m.packageManager); err != nil {
		m.logWarn("Failed to write pending update state (update will proceed)", "error", err)
	}

	// The package's postinst script should restart the service,
	// but we explicitly restart to ensure we're running the new version
	if m.restartFn != nil {
		return m.restartFn()
	}
	return m.restartService()
}

func (m *Manager) downloadWithRetry(ctx context.Context, manifest *UpdateManifest, destPath string) error {
	var lastErr error
	delay := defaultRetryBaseDelay

	// Create progress callback that reports download progress
	// Download is 0-50% of overall update progress (leaves room for verify/stage/apply/restart phases)
	progressCb := func(percent int, bytesRead int64) {
		overallPercent := percent * 50 / 100 // Scale 0-100% download to 0-50% overall
		m.setStatusWithProgress(StatusDownloading, overallPercent, fmt.Sprintf("Downloading... %d%%", percent))
	}

	for attempt := 1; attempt <= m.maxRetries; attempt++ {
		// Check for existing partial download
		var resumeFrom int64
		if info, err := os.Stat(destPath + ".partial"); err == nil {
			resumeFrom = info.Size()
		}

		downloaded, err := m.client.DownloadArtifactWithProgress(ctx, manifest, destPath+".partial", resumeFrom, progressCb)
		if err == nil && downloaded > 0 {
			// Rename partial to final
			if err := os.Rename(destPath+".partial", destPath); err != nil {
				return fmt.Errorf("failed to finalize download: %w", err)
			}
			return nil
		}

		lastErr = err
		m.logWarn("Download attempt failed", "attempt", attempt, "error", err)

		if attempt < m.maxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			// Exponential backoff with jitter
			delay = delay * 2
			if delay > defaultRetryMaxDelay {
				delay = defaultRetryMaxDelay
			}
			delay += time.Duration(rand.Int63n(int64(delay / 4)))
		}
	}

	return fmt.Errorf("download failed after %d attempts: %w", m.maxRetries, lastErr)
}

func (m *Manager) verifyHash(filePath, expectedHash string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file for verification: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to compute hash: %w", err)
	}

	actualHash := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

func (m *Manager) stageBinary(srcPath, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	return copyFile(srcPath, destPath)
}

func (m *Manager) backupBinary(backupPath string) error {
	if m.binaryPath == "" {
		return fmt.Errorf("binary path not set")
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return err
	}
	return copyFile(m.binaryPath, backupPath)
}

func (m *Manager) applyUpdate(stagingPath string) error {
	if m.binaryPath == "" {
		return fmt.Errorf("binary path not set")
	}

	// Package-managed installs are applied by executeUpdateViaPackageManager
	// with an exact version. Refuse to swap their binary here: the package
	// manager owns those files, and a versionless upgrade could install a
	// release other than the one the manifest selected.
	if m.usePackageManager && m.packageName != "" {
		return fmt.Errorf("package-managed install must be updated through %s, not by replacing the binary", m.packageManager)
	}

	// On Windows, we can't replace a running binary directly.
	// We need to use a helper process or schedule the replacement.
	if runtime.GOOS == "windows" {
		return m.applyUpdateWindows(stagingPath)
	}

	// On Unix, we can typically just rename
	return m.applyUpdateUnix(stagingPath)
}

func (m *Manager) applyUpdateUnix(stagingPath string) error {
	// Make the new binary executable
	if err := os.Chmod(stagingPath, 0o755); err != nil {
		return fmt.Errorf("failed to set executable permission: %w", err)
	}

	// Try to rename the staging file to replace the current binary.
	// This is the fastest approach when both paths are on the same filesystem.
	if err := os.Rename(stagingPath, m.binaryPath); err != nil {
		// Check if this is a cross-device link error (EXDEV).
		// This happens when staging dir and binary path are on different filesystems
		// (e.g., staging on /var/lib/... and binary on /usr/bin).
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
			// Fall back to copy + delete for cross-filesystem updates
			m.logInfo("Cross-filesystem update detected, using copy instead of rename",
				"staging", stagingPath, "target", m.binaryPath)

			// Copy the staged file to the target location
			if copyErr := copyFile(stagingPath, m.binaryPath); copyErr != nil {
				// Check for read-only filesystem errors and provide helpful guidance
				if strings.Contains(copyErr.Error(), "read-only file system") {
					return fmt.Errorf("failed to update binary: filesystem is read-only. "+
						"If this agent was installed via apt/deb package, update using: sudo apt-get update && sudo apt-get upgrade printmaster-agent. "+
						"If running in a container, rebuild the container image with the new version. Original error: %w", copyErr)
				}
				return fmt.Errorf("failed to copy binary across filesystems: %w", copyErr)
			}

			// Set executable permissions on the copied file
			if chmodErr := os.Chmod(m.binaryPath, 0o755); chmodErr != nil {
				return fmt.Errorf("failed to set executable permission after copy: %w", chmodErr)
			}

			// Remove the staging file
			_ = os.Remove(stagingPath) // Best effort cleanup

			return nil
		}
		// Check for read-only filesystem on the rename error too
		if strings.Contains(err.Error(), "read-only file system") {
			return fmt.Errorf("failed to update binary: filesystem is read-only. "+
				"If this agent was installed via apt/deb package, update using: sudo apt-get update && sudo apt-get upgrade printmaster-agent. "+
				"If running in a container, rebuild the container image with the new version. Original error: %w", err)
		}
		return fmt.Errorf("failed to replace binary: %w", err)
	}

	return nil
}

// ProgressFunc is a callback for reporting update progress.
type ProgressFunc func(percent int, message string)

// applyUpdateViaPackageManager uses the appropriate package manager (apt/dnf/yum)
// to update the package when the binary was installed via a package manager.
// targetVersion is the exact version to install (e.g., "0.27.4") and is
// required: installing whatever the repository considers newest could skip
// policy checks or land on a different release than the manifest selected.
// progressFn is called with phase progress (0-100) and status message.
func (m *Manager) applyUpdateViaPackageManager(targetVersion string, progressFn ProgressFunc) error {
	if strings.TrimSpace(targetVersion) == "" {
		return fmt.Errorf("package manager update requires a target version")
	}
	m.logInfo("Updating via package manager", "package", m.packageName, "manager", m.packageManager, "target_version", targetVersion)

	switch m.packageManager {
	case "apt":
		return m.applyUpdateViaApt(targetVersion, progressFn)
	case "dnf":
		return m.applyUpdateViaDnf(targetVersion, progressFn)
	case "yum":
		return m.applyUpdateViaYum(targetVersion, progressFn)
	default:
		return fmt.Errorf("unknown package manager: %s", m.packageManager)
	}
}

// applyUpdateViaApt uses apt-get to update the package when the binary was installed via dpkg.
// This is the proper way to update on Debian/Ubuntu systems with package-managed binaries.
// Note: Requires sudoers configuration installed by the .deb package in /etc/sudoers.d/printmaster-agent
func (m *Manager) applyUpdateViaApt(targetVersion string, progressFn ProgressFunc) error {
	// Phase 1: Update package lists (0-30%)
	progressFn(5, "Refreshing package metadata...")
	m.logDebug("Running sudo apt-get update")
	updateCmd := exec.Command("sudo", "apt-get", "update", "-qq")
	updateCmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if output, err := updateCmd.CombinedOutput(); err != nil {
		m.logWarn("apt-get update failed (continuing anyway)", "error", err, "output", string(output))
		// Don't fail here - the package might already be in cache
	}
	progressFn(30, "Package metadata updated")

	// Phase 2: Download and install (30-90%)
	// Format: packagename=version (e.g., printmaster-agent=0.27.4)
	packageSpec := fmt.Sprintf("%s=%s", m.packageName, targetVersion)

	progressFn(35, fmt.Sprintf("Downloading %s...", packageSpec))
	m.logInfo("Running sudo apt-get install", "package", packageSpec)
	installCmd := exec.Command("sudo", "apt-get", "install", "-y", "-qq", "--allow-downgrades", packageSpec)
	installCmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	output, err := installCmd.CombinedOutput()
	if err != nil {
		return m.wrapSudoError("apt-get install", err, output)
	}
	progressFn(90, "Package installed successfully")

	m.logInfo("Package updated successfully via apt-get", "package", packageSpec, "output", strings.TrimSpace(string(output)))

	// Phase 3: Finalize (90-100%)
	progressFn(95, "Finalizing update...")
	return nil
}

// applyUpdateViaDnf uses dnf to update the package when the binary was installed via rpm.
// This is the proper way to update on Fedora/RHEL 8+ systems with package-managed binaries.
// Note: Requires sudoers configuration installed by the .rpm package in /etc/sudoers.d/printmaster-agent
func (m *Manager) applyUpdateViaDnf(targetVersion string, progressFn ProgressFunc) error {
	// Phase 1: Prepare (0-10%)
	progressFn(5, "Preparing dnf update...")

	// Install the specific version to ensure we get exactly what we want.
	// Format: packagename-version (e.g., printmaster-agent-0.27.4)
	packageSpec := fmt.Sprintf("%s-%s", m.packageName, targetVersion)

	// Phase 2: Refresh metadata and resolve dependencies (10-30%)
	progressFn(10, "Refreshing repository metadata...")
	m.logInfo("Running sudo dnf install with refresh", "package", packageSpec)

	// Use --refresh to force metadata refresh from repos, bypassing any stale cache.
	progressFn(25, fmt.Sprintf("Resolving dependencies for %s...", packageSpec))

	// Phase 3: Download package (30-60%)
	progressFn(35, fmt.Sprintf("Downloading %s...", packageSpec))

	installCmd := exec.Command("sudo", "dnf",
		"--setopt=logdir=/tmp",
		"--refresh", // Force metadata refresh
		"install",
		"-y",
		"--allowerasing", // Allow replacing conflicting packages
		packageSpec)
	output, err := installCmd.CombinedOutput()
	if err != nil {
		// Do not fall back to a versionless upgrade: it could install a
		// different release than the manifest selected. Fail visibly instead.
		m.logWarn("dnf install of exact version failed", "package", packageSpec, "error", err, "output", string(output))
		return m.wrapSudoError("dnf install", err, output)
	}

	// Phase 4: Install/Verify (60-90%)
	progressFn(75, "Installing package...")
	progressFn(90, "Package installed successfully")

	m.logInfo("Package updated successfully via dnf", "package", packageSpec, "output", strings.TrimSpace(string(output)))

	// Phase 5: Finalize (90-100%)
	progressFn(95, "Finalizing update...")
	return nil
}

// applyUpdateViaYum uses yum to update the package when the binary was installed via rpm.
// This is for older RHEL/CentOS systems that don't have dnf.
// Note: Requires sudoers configuration installed by the .rpm package in /etc/sudoers.d/printmaster-agent
func (m *Manager) applyUpdateViaYum(targetVersion string, progressFn ProgressFunc) error {
	// Phase 1: Clean cache (0-20%)
	progressFn(5, "Cleaning yum cache...")
	m.logDebug("Running sudo yum clean metadata")
	cleanCmd := exec.Command("sudo", "yum", "clean", "metadata", "-q")
	cleanCmd.Run() // Ignore errors
	progressFn(20, "Cache cleaned")

	// Phase 2: Resolve and download (20-60%)
	packageSpec := fmt.Sprintf("%s-%s", m.packageName, targetVersion)

	progressFn(25, fmt.Sprintf("Resolving dependencies for %s...", packageSpec))
	progressFn(35, fmt.Sprintf("Downloading %s...", packageSpec))

	m.logInfo("Running sudo yum install", "package", packageSpec)
	installCmd := exec.Command("sudo", "yum", "install", "-y", "-q", packageSpec)
	output, err := installCmd.CombinedOutput()
	if err != nil {
		// No versionless fallback; see applyUpdateViaDnf.
		m.logWarn("yum install of exact version failed", "package", packageSpec, "error", err, "output", string(output))
		return m.wrapSudoError("yum install", err, output)
	}

	// Phase 3: Install complete (60-90%)
	progressFn(75, "Installing package...")
	progressFn(90, "Package installed successfully")

	m.logInfo("Package updated successfully via yum", "package", packageSpec, "output", strings.TrimSpace(string(output)))

	// Phase 4: Finalize (90-100%)
	progressFn(95, "Finalizing update...")
	return nil
}

// wrapSudoError provides a cleaner error message when sudo fails due to password prompts.
// This typically happens when the agent is not running as a service with the proper sudoers
// configuration, or when running manually without the expected user.
func (m *Manager) wrapSudoError(operation string, err error, output []byte) error {
	outStr := string(output)

	// Check for common sudo password prompt indicators
	if strings.Contains(outStr, "a password is required") ||
		strings.Contains(outStr, "password for") ||
		strings.Contains(outStr, "Password:") ||
		strings.Contains(outStr, "Sorry, try again") ||
		strings.Contains(outStr, "usual lecture from the local System Administrator") {
		return fmt.Errorf("%s requires passwordless sudo. "+
			"Ensure the agent is running as a systemd service (systemctl start printmaster-agent) "+
			"and the sudoers file exists at /etc/sudoers.d/printmaster-agent. "+
			"If running manually, you can update using: sudo %s -y %s",
			operation, m.packageManager, m.packageName)
	}

	// Generic error with full output for debugging
	return fmt.Errorf("%s failed: %w (output: %s)", operation, err, outStr)
}

func (m *Manager) applyUpdateWindows(stagingPath string) error {
	// If using MSI, delegate to MSI-specific update method
	if m.useMSI {
		return m.applyUpdateWindowsMSI(stagingPath)
	}

	// On Windows, we write a helper batch file that:
	// 1. Stops the service (if running as service) to prevent auto-restart race
	// 2. Waits for the current process to exit
	// 3. Copies the new binary over the old one
	// 4. Restarts the service (or starts the binary directly)
	//
	// The batch file is launched detached so it continues running after we exit.

	// Use the isService flag passed from main (which uses kardianos/service.Interactive())
	// This is more reliable than trying to detect service mode from within the update package
	isService := m.isService

	var helperScript string
	if isService {
		// Running as a Windows service - use SC to stop, copy, and start
		// IMPORTANT: We must stop the service first to prevent the service manager
		// from auto-restarting the old binary while we're trying to update.
		helperScript = fmt.Sprintf(`@echo off
echo PrintMaster Agent Update Helper
echo ============================================
echo Stopping service to prevent auto-restart...
sc stop PrintMasterAgent
if %%errorlevel%% neq 0 (
    echo WARNING: SC stop returned %%errorlevel%% - service may already be stopping
)

echo Waiting for service to fully stop...
:waitloop
sc query PrintMasterAgent | find "STOPPED" >nul
if %%errorlevel%% neq 0 (
    timeout /t 1 /nobreak >nul
    goto waitloop
)
echo Service stopped.

echo Copying new binary...
copy /y "%s" "%s"
if %%errorlevel%% neq 0 (
    echo ERROR: Failed to copy new binary
    echo Attempting to restart service with old binary...
    sc start PrintMasterAgent
    exit /b %%errorlevel%%
)
echo Binary copied successfully.

echo Starting service with new binary...
sc start PrintMasterAgent
if %%errorlevel%% neq 0 (
    echo WARNING: SC start returned %%errorlevel%% - service may already be starting
)

echo Update complete, cleaning up...
timeout /t 2 /nobreak >nul
del "%%~f0"
`, stagingPath, m.binaryPath)
	} else {
		// Running standalone - just start the binary
		helperScript = fmt.Sprintf(`@echo off
echo PrintMaster Agent Update Helper
echo Waiting for agent to stop...
timeout /t 3 /nobreak >nul

echo Copying new binary...
copy /y "%s" "%s"
if %%errorlevel%% neq 0 (
    echo ERROR: Failed to copy new binary
    exit /b %%errorlevel%%
)

echo Starting agent...
start "" "%s"

echo Update complete, cleaning up...
timeout /t 2 /nobreak >nul
del "%%~f0"
`, stagingPath, m.binaryPath, m.binaryPath)
	}

	helperPath := filepath.Join(m.stateDir, "update_helper.bat")

	// Ensure the state directory exists (it should, but verify)
	if err := os.MkdirAll(m.stateDir, 0o755); err != nil {
		return fmt.Errorf("failed to ensure update directory exists: %w", err)
	}

	if err := os.WriteFile(helperPath, []byte(helperScript), 0o755); err != nil {
		return fmt.Errorf("failed to write update helper: %w", err)
	}

	// Verify the file was actually written before trying to launch it
	if _, err := os.Stat(helperPath); err != nil {
		return fmt.Errorf("update helper file not found after write: %w", err)
	}

	// Launch the helper script detached - it will wait for us to exit, then do the copy
	if m.launchHelperFn != nil {
		// Use mock function (for testing)
		if err := m.launchHelperFn(helperPath); err != nil {
			return fmt.Errorf("failed to launch update helper: %w", err)
		}
	} else {
		// Production: use cmd /C start with proper quoting
		// - First quoted arg after "start" is the window title
		// - The actual command must also be quoted if it contains spaces
		cmd := exec.Command("cmd.exe", "/C", "start", "/min", "PrintMaster Update", helperPath)
		cmd.Dir = m.stateDir
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to launch update helper: %w", err)
		}
	}

	m.logInfo("Update helper launched", "helper_path", helperPath, "is_service", isService)
	return nil
}

// applyUpdateWindowsMSI applies an update using the Windows MSI installer.
// This method downloads an MSI and runs msiexec to perform an upgrade.
func (m *Manager) applyUpdateWindowsMSI(msiPath string) error {
	m.logInfo("Applying update via MSI", "msi_path", msiPath)

	// Create a helper batch script that:
	// 1. Waits for current process to exit
	// 2. Runs msiexec to install the new MSI (which handles service stop/start)
	// 3. Cleans up

	logPath := filepath.Join(m.stateDir, "msi_update.log")

	// MSI upgrade will automatically:
	// - Stop the service
	// - Replace files
	// - Start the service
	// We use /qn for silent install, /l*v for verbose logging
	helperScript := fmt.Sprintf(`@echo off
echo PrintMaster Agent MSI Update Helper
echo ============================================
echo Waiting for agent to exit...
timeout /t 3 /nobreak >nul

echo Running MSI installer...
msiexec /i "%s" /qn /norestart /l*v "%s"
set MSI_EXIT=%%errorlevel%%

if %%MSI_EXIT%% equ 0 (
    echo MSI installation completed successfully.
) else if %%MSI_EXIT%% equ 3010 (
    echo MSI installation completed - reboot required.
) else (
    echo MSI installation failed with exit code %%MSI_EXIT%%
    echo Check log file: %s
)

echo Cleaning up...
timeout /t 2 /nobreak >nul
del "%s"
del "%%~f0"
exit /b %%MSI_EXIT%%
`, msiPath, logPath, logPath, msiPath)

	helperPath := filepath.Join(m.stateDir, "msi_update_helper.bat")

	// Ensure the state directory exists
	if err := os.MkdirAll(m.stateDir, 0o755); err != nil {
		return fmt.Errorf("failed to ensure update directory exists: %w", err)
	}

	if err := os.WriteFile(helperPath, []byte(helperScript), 0o755); err != nil {
		return fmt.Errorf("failed to write MSI update helper: %w", err)
	}

	// Verify the file was written
	if _, err := os.Stat(helperPath); err != nil {
		return fmt.Errorf("MSI update helper file not found after write: %w", err)
	}

	// Launch the helper script detached
	if m.launchHelperFn != nil {
		// Use mock function (for testing)
		if err := m.launchHelperFn(helperPath); err != nil {
			return fmt.Errorf("failed to launch MSI update helper: %w", err)
		}
	} else {
		// Production: use cmd /C start with proper quoting
		cmd := exec.Command("cmd.exe", "/C", "start", "/min", "PrintMaster MSI Update", helperPath)
		cmd.Dir = m.stateDir
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to launch MSI update helper: %w", err)
		}
	}

	m.logInfo("MSI update helper launched", "helper_path", helperPath, "msi_path", msiPath)
	return nil
}

func (m *Manager) rollback(backupPath string) error {
	if backupPath == "" || m.binaryPath == "" {
		return fmt.Errorf("rollback not possible: missing paths")
	}

	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("backup file not found: %s", backupPath)
	}

	return copyFile(backupPath, m.binaryPath)
}

// writePendingUpdateState saves the expected target version before restart.
// This allows the new version to validate the update was successful on startup.
func (m *Manager) writePendingUpdateState(targetVersion, method string) error {
	state := PendingUpdateState{
		TargetVersion: targetVersion,
		FromVersion:   m.currentVersion,
		StartedAt:     m.clock(),
		Method:        method,
	}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal pending update state: %w", err)
	}

	pendingPath := filepath.Join(m.stateDir, pendingUpdateFile)
	if err := os.WriteFile(pendingPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write pending update state: %w", err)
	}

	m.logInfo("Wrote pending update state", "target_version", targetVersion, "method", method, "path", pendingPath)
	return nil
}

// ValidatePostUpdate checks if this is a fresh start after an update and validates
// the version is correct. Returns (wasUpdated bool, fromVersion string, err error).
// Call this on startup to verify updates completed successfully.
func (m *Manager) ValidatePostUpdate() (bool, string, error) {
	pendingPath := filepath.Join(m.stateDir, pendingUpdateFile)

	data, err := os.ReadFile(pendingPath)
	if os.IsNotExist(err) {
		// No pending update - normal startup
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("failed to read pending update state: %w", err)
	}

	// Always clean up the file regardless of validation result
	defer func() {
		if removeErr := os.Remove(pendingPath); removeErr != nil {
			m.logWarn("Failed to remove pending update state file", "error", removeErr)
		}
	}()

	var state PendingUpdateState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, "", fmt.Errorf("failed to parse pending update state: %w", err)
	}

	m.logInfo("Checking post-update validation",
		"expected_version", state.TargetVersion,
		"current_version", m.currentVersion,
		"from_version", state.FromVersion,
		"method", state.Method)

	// Validate that current version matches expected target
	if m.currentVersion != state.TargetVersion {
		// Version mismatch - update may have failed
		m.logError("Post-update validation FAILED: version mismatch",
			"expected", state.TargetVersion,
			"actual", m.currentVersion,
			"from_version", state.FromVersion)
		return true, state.FromVersion, fmt.Errorf("update validation failed: expected version %s but running %s",
			state.TargetVersion, m.currentVersion)
	}

	// Success!
	m.logInfo("Post-update validation PASSED",
		"new_version", m.currentVersion,
		"previous_version", state.FromVersion,
		"update_method", state.Method,
		"update_duration", m.clock().Sub(state.StartedAt).String())

	return true, state.FromVersion, nil
}

// ValidatePostUpdateWithHealthCheck performs post-update validation including a health check.
// It waits for the health endpoint to respond and validates the version.
func (m *Manager) ValidatePostUpdateWithHealthCheck(healthURL string, timeout time.Duration) error {
	if healthURL == "" {
		healthURL = "http://127.0.0.1:8080/api/version"
	}
	if timeout == 0 {
		timeout = defaultHealthCheckTimeout
	}

	m.logInfo("Performing health check", "url", healthURL, "timeout", timeout)

	client := &http.Client{Timeout: 10 * time.Second}
	deadline := m.clock().Add(timeout)

	for m.clock().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err != nil {
			m.logDebug("Health check attempt failed", "error", err)
			time.Sleep(time.Second)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var versionInfo map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&versionInfo); err == nil {
				if version, ok := versionInfo["version"]; ok {
					m.logInfo("Health check passed", "version", version)
					resp.Body.Close()
					return nil
				}
			}
			resp.Body.Close()
		} else {
			resp.Body.Close()
		}

		time.Sleep(time.Second)
	}

	return fmt.Errorf("health check timed out after %s", timeout)
}

func (m *Manager) restartService() error {
	m.logInfo("Triggering restart for update")

	// On Linux with systemd, we need to explicitly request a restart because:
	// - The service file uses Restart=on-failure
	// - os.Exit(0) is a clean exit, so systemd won't auto-restart
	// We use "systemctl restart --no-block" which returns immediately while
	// systemd handles stopping us and starting the new version.
	if runtime.GOOS == "linux" && m.isService {
		serviceName := m.serviceName
		if serviceName == "" {
			serviceName = "printmaster-agent" // Default service name
		}
		// Always use .service suffix for explicit matching with sudoers rules
		if !strings.HasSuffix(serviceName, ".service") {
			serviceName += ".service"
		}

		m.logInfo("Requesting systemd restart", "service", serviceName)

		// Use --no-block so the command returns immediately.
		// systemd will handle stopping this process and starting the new one.
		cmd := exec.Command("sudo", "systemctl", "restart", "--no-block", serviceName)
		if err := cmd.Start(); err != nil {
			m.logWarn("Failed to request systemd restart, falling back to exit", "error", err)
			// Fall through to os.Exit - hopefully Restart=on-failure triggers
			os.Exit(1) // Use exit code 1 so on-failure restart triggers
			return nil
		}

		// Give systemd a moment to receive the request, then exit cleanly.
		// The restart command has already been issued, so we can exit now.
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
		return nil // Never reached
	}

	// For Windows or non-service mode, just exit.
	// On Windows, the service manager (SCM) should handle restart.
	// In non-service mode, the user will need to restart manually.
	os.Exit(0)
	return nil // Never reached
}

func (m *Manager) isUpdateNeeded(manifest *UpdateManifest) bool {
	if manifest == nil || manifest.Version == "" {
		return false
	}

	// Simple string comparison fallback
	if m.currentSemver == nil {
		return manifest.Version != m.currentVersion
	}

	targetSemver, err := parseSemverVersion(manifest.Version)
	if err != nil {
		return manifest.Version != m.currentVersion
	}

	// Check version pin strategy
	if m.policyProvider != nil {
		spec, _, ok := m.policyProvider.EffectivePolicy()
		if ok {
			if !m.isVersionAllowed(targetSemver, spec) {
				return false
			}
		}
	}

	return targetSemver.GreaterThan(m.currentSemver)
}

func (m *Manager) isVersionAllowed(target *semver.Version, spec updatepolicy.PolicySpec) bool {
	if spec.TargetVersion != "" {
		// Specific target version pinned
		targetPinned, err := semver.NewVersion(strings.TrimPrefix(spec.TargetVersion, "v"))
		if err == nil && !target.Equal(targetPinned) {
			return false
		}
	}

	switch spec.VersionPinStrategy {
	case updatepolicy.VersionPinMajor:
		// Only allow same major, or allow major if explicitly enabled
		if target.Major() != m.currentSemver.Major() && !spec.AllowMajorUpgrade {
			return false
		}
	case updatepolicy.VersionPinMinor:
		// Only allow same major.minor
		if target.Major() != m.currentSemver.Major() || target.Minor() != m.currentSemver.Minor() {
			if !spec.AllowMajorUpgrade {
				return false
			}
		}
	case updatepolicy.VersionPinPatch:
		// Only allow same major.minor.patch line (shouldn't reach here if same)
		return false
	}

	return true
}

func (m *Manager) isInMaintenanceWindow() bool {
	if m.policyProvider == nil {
		return true // No policy = always allowed
	}

	spec, _, ok := m.policyProvider.EffectivePolicy()
	if !ok || !spec.MaintenanceWindow.Enabled {
		return true
	}

	window := spec.MaintenanceWindow
	now := m.clock()

	// Load timezone
	loc := time.UTC
	if window.Timezone != "" {
		if tz, err := time.LoadLocation(window.Timezone); err == nil {
			loc = tz
		}
	}

	localNow := now.In(loc)

	// Check day of week
	if len(window.DaysOfWeek) > 0 {
		dayAllowed := false
		currentDay := int(localNow.Weekday())
		for _, d := range window.DaysOfWeek {
			if d == currentDay {
				dayAllowed = true
				break
			}
		}
		if !dayAllowed {
			return false
		}
	}

	// Check time of day
	currentMinutes := localNow.Hour()*60 + localNow.Minute()
	startMinutes := window.StartHour*60 + window.StartMin
	endMinutes := window.EndHour*60 + window.EndMin

	if startMinutes <= endMinutes {
		// Same day window
		return currentMinutes >= startMinutes && currentMinutes < endMinutes
	}
	// Overnight window
	return currentMinutes >= startMinutes || currentMinutes < endMinutes
}

func (m *Manager) checkDiskSpace(requiredBytes int64) error {
	// Need space for: download + staging + backup
	requiredMB := (requiredBytes * 3) / (1024 * 1024)
	if requiredMB < m.minDiskSpace {
		requiredMB = m.minDiskSpace
	}

	available, err := getAvailableDiskSpaceMB(m.stateDir)
	if err != nil {
		m.logWarn("Failed to check disk space", "error", err)
		return nil // Don't block on disk check failure
	}

	if available < requiredMB {
		return fmt.Errorf("insufficient disk space: need %d MB, have %d MB", requiredMB, available)
	}

	return nil
}

func parseSemverVersion(raw string) (*semver.Version, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if trimmed == "" {
		return nil, fmt.Errorf("empty version")
	}
	if ver, err := semver.NewVersion(trimmed); err == nil {
		return ver, nil
	}
	core := trimmed
	build := ""
	if plus := strings.Index(core, "+"); plus != -1 {
		build = core[plus:]
		core = core[:plus]
	}
	prerelease := ""
	if dash := strings.Index(core, "-"); dash != -1 {
		prerelease = core[dash+1:]
		core = core[:dash]
	}
	segments := strings.Split(core, ".")
	if len(segments) <= 3 {
		return semver.NewVersion(trimmed)
	}
	base := strings.Join(segments[:3], ".")
	extra := strings.Join(segments[3:], ".")
	preParts := []string{}
	if extra != "" {
		preParts = append(preParts, extra)
	}
	if prerelease != "" {
		preParts = append(preParts, prerelease)
	}
	normalized := base
	if len(preParts) > 0 {
		normalized += "-" + strings.Join(preParts, ".")
	}
	if build != "" {
		normalized += build
	}
	return semver.NewVersion(normalized)
}

func (m *Manager) setStatus(s Status) {
	m.setStatusWithProgress(s, -1, "")
}

func (m *Manager) setStatusWithProgress(s Status, progress int, message string) {
	m.mu.Lock()
	m.status = s
	targetVersion := ""
	if m.currentRun != nil {
		targetVersion = m.currentRun.TargetVersion
	} else if m.latestManifest != nil {
		targetVersion = m.latestManifest.Version
	}
	m.mu.Unlock()

	// Invoke progress callback if set
	if m.progressCallback != nil {
		go m.progressCallback(s, targetVersion, progress, message, nil)
	}
}

func (m *Manager) setStatusWithError(s Status, errCode, errMsg string) {
	m.mu.Lock()
	m.status = s
	targetVersion := ""
	if m.currentRun != nil {
		targetVersion = m.currentRun.TargetVersion
	}
	m.mu.Unlock()

	// Invoke progress callback with error
	if m.progressCallback != nil {
		go m.progressCallback(s, targetVersion, -1, errMsg, fmt.Errorf("%s: %s", errCode, errMsg))
	}
}

// Cancel attempts to cancel an in-progress update. Returns true if cancellation
// was successful, false if the update is in a non-cancellable phase (restarting).
func (m *Manager) Cancel() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Cannot cancel once we're restarting - it's too late
	if m.status == StatusRestarting || m.status == StatusApplying {
		return false
	}

	// If not in an active update phase, nothing to cancel
	if m.status == StatusIdle || m.status == StatusSucceeded || m.status == StatusFailed {
		return false
	}

	m.cancelled = true
	return true
}

// IsCancelled checks if the current update has been cancelled.
func (m *Manager) IsCancelled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cancelled
}

func (m *Manager) reportTelemetry(ctx context.Context, status Status, errCode, errMsg string) {
	if m.telemetry == nil {
		return
	}

	payload := TelemetryPayload{
		Status:         status,
		CurrentVersion: m.currentVersion,
		Timestamp:      m.clock(),
	}

	m.mu.RLock()
	if m.currentRun != nil {
		payload.RunID = m.currentRun.ID
		payload.TargetVersion = m.currentRun.TargetVersion
		payload.SizeBytes = m.currentRun.SizeBytes
		payload.DownloadTimeMs = m.currentRun.DownloadTimeMs
	}
	m.mu.RUnlock()

	if errCode != "" {
		payload.ErrorCode = errCode
		payload.ErrorMessage = errMsg
	}

	go func() {
		if err := m.telemetry.ReportUpdateStatus(context.Background(), payload); err != nil {
			m.logWarn("Failed to report telemetry", "error", err)
		}
	}()
}

func (m *Manager) binaryExtension() string {
	if m.platform == "windows" {
		if m.useMSI {
			return ".msi"
		}
		return ".exe"
	}
	return ""
}

func (m *Manager) logInfo(msg string, args ...interface{}) {
	if m.log != nil {
		m.log.Info(msg, args...)
	}
}

func (m *Manager) logDebug(msg string, args ...interface{}) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

func (m *Manager) logWarn(msg string, args ...interface{}) {
	if m.log != nil {
		m.log.Warn(msg, args...)
	}
}

func (m *Manager) logError(msg string, args ...interface{}) {
	if m.log != nil {
		m.log.Error(msg, args...)
	}
}

// checkBinaryUpdateMethod determines how the agent can be updated:
// - Returns (packageName, "apt", "") if managed by dpkg and apt-get can be used
// - Returns (packageName, "dnf", "") if managed by rpm and dnf can be used
// - Returns ("", "", "") if directory is writable and direct update is possible
// - Returns ("", "", reason) if update is not possible (read-only, no permissions)
func checkBinaryUpdateMethod(binaryPath, binaryDir string, log *logger.Logger) (packageName string, packageManager string, disabledReason string) {
	if runtime.GOOS != "linux" {
		// Non-Linux: check directory writability only
		return checkDirectoryWritable(binaryDir, log)
	}

	baseName := filepath.Base(binaryPath)
	pathsToCheck := buildPathsToCheck(binaryPath, baseName)

	// Try dpkg/apt first (Debian/Ubuntu)
	if pkgName, found := checkDpkg(pathsToCheck, log); found {
		if _, aptErr := exec.LookPath("apt-get"); aptErr == nil {
			if log != nil {
				log.Info("Binary managed by dpkg, will use apt-get for updates",
					"package", pkgName, "path", binaryPath)
			}
			return pkgName, "apt", ""
		}
		// dpkg but no apt-get - can't update via package manager
		if log != nil {
			log.Info("Binary managed by dpkg but apt-get not available",
				"package", pkgName, "path", binaryPath)
		}
		return "", "", fmt.Sprintf("binary managed by package %s but apt-get not available", pkgName)
	}

	// Try rpm/dnf (Fedora/RHEL/CentOS)
	if pkgName, found := checkRpm(pathsToCheck, log); found {
		if _, dnfErr := exec.LookPath("dnf"); dnfErr == nil {
			if log != nil {
				log.Info("Binary managed by rpm, will use dnf for updates",
					"package", pkgName, "path", binaryPath)
			}
			return pkgName, "dnf", ""
		}
		// rpm but no dnf - try yum as fallback
		if _, yumErr := exec.LookPath("yum"); yumErr == nil {
			if log != nil {
				log.Info("Binary managed by rpm, will use yum for updates",
					"package", pkgName, "path", binaryPath)
			}
			return pkgName, "yum", ""
		}
		// rpm but no dnf/yum - can't update via package manager
		if log != nil {
			log.Info("Binary managed by rpm but dnf/yum not available",
				"package", pkgName, "path", binaryPath)
		}
		return "", "", fmt.Sprintf("binary managed by package %s but dnf/yum not available", pkgName)
	}

	// Not package-managed, check directory writability
	return checkDirectoryWritable(binaryDir, log)
}

// buildPathsToCheck builds a list of paths to check for package management
func buildPathsToCheck(binaryPath, baseName string) []string {
	pathsToCheck := []string{binaryPath}

	// Add symlink-resolved path if different
	if resolved, err := filepath.EvalSymlinks(binaryPath); err == nil && resolved != binaryPath {
		pathsToCheck = append(pathsToCheck, resolved)
	}

	// Also check common package installation paths
	for _, commonPath := range []string{"/usr/bin/", "/usr/local/bin/", "/usr/sbin/"} {
		candidate := filepath.Join(commonPath, baseName)
		found := false
		for _, p := range pathsToCheck {
			if p == candidate {
				found = true
				break
			}
		}
		if !found {
			pathsToCheck = append(pathsToCheck, candidate)
		}
	}

	return pathsToCheck
}

// checkDpkg checks if the binary is managed by dpkg (Debian/Ubuntu)
func checkDpkg(pathsToCheck []string, log *logger.Logger) (packageName string, found bool) {
	dpkgPath, err := exec.LookPath("dpkg-query")
	if err != nil {
		if log != nil {
			log.Debug("dpkg-query not available", "error", err)
		}
		return "", false
	}

	for _, checkPath := range pathsToCheck {
		cmd := exec.Command(dpkgPath, "-S", checkPath)
		output, err := cmd.Output()
		if err != nil {
			if log != nil {
				log.Debug("dpkg-query check failed for path", "path", checkPath, "error", err)
			}
			continue
		}
		if len(output) == 0 {
			continue
		}
		// dpkg-query -S returns "package: /path/to/file" if managed
		pkgInfo := strings.TrimSpace(string(output))
		if pkgInfo == "" || strings.Contains(pkgInfo, "no path found") {
			continue
		}
		return strings.Split(pkgInfo, ":")[0], true
	}

	if log != nil {
		log.Debug("dpkg-query did not find package for any path variant", "paths_checked", pathsToCheck)
	}
	return "", false
}

// checkRpm checks if the binary is managed by rpm (Fedora/RHEL/CentOS)
func checkRpm(pathsToCheck []string, log *logger.Logger) (packageName string, found bool) {
	rpmPath, err := exec.LookPath("rpm")
	if err != nil {
		if log != nil {
			log.Debug("rpm not available", "error", err)
		}
		return "", false
	}

	for _, checkPath := range pathsToCheck {
		// rpm -qf returns the package name that owns a file
		cmd := exec.Command(rpmPath, "-qf", checkPath)
		output, err := cmd.Output()
		if err != nil {
			if log != nil {
				log.Debug("rpm -qf check failed for path", "path", checkPath, "error", err)
			}
			continue
		}
		if len(output) == 0 {
			continue
		}
		pkgInfo := strings.TrimSpace(string(output))
		if pkgInfo == "" || strings.Contains(pkgInfo, "not owned by any package") {
			continue
		}
		// rpm -qf returns full package name with version, extract base name
		// e.g., "printmaster-agent-1.2.3-1.fc41.x86_64" -> "printmaster-agent"
		// We want the package name without version for dnf install
		pkgName := pkgInfo
		// Try to get just the name without version using rpm -q --qf
		nameCmd := exec.Command(rpmPath, "-q", "--qf", "%{NAME}", pkgInfo)
		if nameOutput, err := nameCmd.Output(); err == nil && len(nameOutput) > 0 {
			pkgName = strings.TrimSpace(string(nameOutput))
		}
		return pkgName, true
	}

	if log != nil {
		log.Debug("rpm did not find package for any path variant", "paths_checked", pathsToCheck)
	}
	return "", false
}

// checkDirectoryWritable checks if the binary directory is writable for direct updates
func checkDirectoryWritable(binaryDir string, log *logger.Logger) (string, string, string) {
	testFile := filepath.Join(binaryDir, ".printmaster-update-check")
	f, err := os.Create(testFile)
	if err != nil {
		if os.IsPermission(err) {
			if log != nil {
				log.Info("Binary directory not writable (permission denied), self-update disabled",
					"dir", binaryDir, "error", err)
			}
			return "", "", "binary directory not writable: permission denied (run with elevated privileges)"
		}
		if strings.Contains(err.Error(), "read-only file system") {
			if log != nil {
				log.Info("Binary directory on read-only filesystem, self-update disabled",
					"dir", binaryDir, "error", err)
			}
			return "", "", "binary on read-only filesystem"
		}
		// Other errors - log but don't disable (might be transient)
		if log != nil {
			log.Warn("Could not verify binary directory writability",
				"dir", binaryDir, "error", err)
		}
		return "", "", ""
	}
	// Clean up test file
	f.Close()
	os.Remove(testFile)

	return "", "", ""
}

// copyFile copies a file from src to dst.
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
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Close()
}
