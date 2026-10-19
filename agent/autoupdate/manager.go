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
	"printmaster/common/updatetrust"

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
	RunID            string                  `json:"run_id,omitempty"`
	TargetVersion    string                  `json:"target_version"`
	FromVersion      string                  `json:"from_version"`
	StartedAt        time.Time               `json:"started_at"`
	Method           string                  `json:"method"` // "binary", "msi", "dnf", "apt", "yum"
	HelperResultPath string                  `json:"helper_result_path,omitempty"`
	Installation     *InstallationDescriptor `json:"installation,omitempty"`
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
	Log               *logger.Logger
	DataDir           string
	Enabled           bool
	CurrentVersion    string
	Platform          string
	Arch              string
	Channel           string
	ChannelProvider   func() string // Optional managed channel, read at each operation.
	BinaryPath        string
	ServiceName       string
	IsService         bool // True if running as a Windows service (determines update restart method)
	MinDiskSpaceMB    int64
	MaxRetries        int
	Clock             func() time.Time
	ServerClient      UpdateClient
	PublisherVerifier PublisherVerifier
	PolicyProvider    PolicyProvider
	TelemetrySink     TelemetrySink
	ProgressCallback  ProgressCallback
	SnapshotCallback  func(updatepolicy.ProgressSnapshot)
}

// PublisherVerifier authorizes the exact candidate against independently pinned
// publisher trust. A Server manifest signature is not an implementation of it.
type PublisherVerifier interface {
	Authorize(context.Context, updatetrust.Target) (updatetrust.Authorization, error)
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
	log               *logger.Logger
	stateDir          string
	enabled           bool
	disabledReason    string
	currentVersion    string
	currentSemver     *semver.Version
	platform          string
	arch              string
	channel           string
	channelProvider   func() string
	binaryPath        string
	serviceName       string
	isService         bool
	minDiskSpace      int64
	maxRetries        int
	clock             func() time.Time
	client            UpdateClient
	publisherVerifier PublisherVerifier
	policyProvider    PolicyProvider
	telemetry         TelemetrySink
	restartFn         func() error
	launchHelperFn    func(helperPath string) error // For testing: allows mocking the helper launch
	progressCallback  ProgressCallback
	snapshotCallback  func(updatepolicy.ProgressSnapshot)
	progressMu        sync.Mutex
	progress          updatepolicy.ProgressSnapshot
	progressErr       error
	// Package manager support (Linux dpkg/apt/dnf/rpm)
	usePackageManager bool   // True if binary is managed by a package manager
	packageName       string // Package name (e.g., "printmaster-agent")
	packageManager    string // Package manager type: "apt", "dnf", or ""

	// MSI support (Windows)
	useMSI         bool   // True if installed via MSI
	msiProductCode string // MSI Product Code GUID for upgrades

	// operationMu owns the entire check/install lifecycle, including pending
	// transitions where status alone does not indicate exclusive ownership.
	operationMu      sync.Mutex
	mu               sync.RWMutex
	status           Status
	lastCheck        time.Time
	nextCheck        time.Time
	latestVersion    string
	latestManifest   *UpdateManifest
	latestApplicable bool
	currentRun       *UpdateRun
	cancelled        bool
	operationCancel  context.CancelFunc
	helperReconcile  bool

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
		// Discovery remains available, but installation fails closed without ordering.
		if opts.Log != nil {
			opts.Log.Warn("Failed to parse current version as semver", "version", opts.CurrentVersion, "error", parseErr)
		}
	}

	stateDir := filepath.Join(dataDir, updateDirName)
	if err := secureUpdaterDirectories(dataDir); err != nil {
		return nil, fmt.Errorf("failed to secure updater directories: %w", err)
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
	if runtime.GOOS == "windows" && opts.IsService && binaryPath != "" {
		if err := secureWindowsServiceExecutable(binaryPath); err != nil {
			return nil, fmt.Errorf("failed to secure Windows service executable: %w", err)
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
		publisherVerifier: opts.PublisherVerifier,
		policyProvider:    opts.PolicyProvider,
		telemetry:         opts.TelemetrySink,
		progressCallback:  opts.ProgressCallback,
		snapshotCallback:  opts.SnapshotCallback,
		usePackageManager: usePackageManager,
		packageName:       packageName,
		packageManager:    packageManager,
		useMSI:            useMSI,
		msiProductCode:    msiProductCode,
		status:            StatusIdle,
		stopCh:            make(chan struct{}),
	}

	mgr.restartFn = mgr.restartService
	if mgr.publisherVerifier == nil {
		mgr.logWarn("Publisher trust not configured; update candidates will be rejected",
			"error_code", ErrCodeManifestError)
	}
	if err := mgr.restoreProgress(); err != nil {
		return nil, err
	}

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
		Progress:          m.progressSnapshotLocked(),
		Enabled:           m.enabled,
		DisabledReason:    m.disabledReason,
		CurrentVersion:    m.currentVersion,
		LatestVersion:     m.latestVersion,
		UpdateAvailable:   m.latestApplicable,
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
	return m.performCheck(ctx)
}

// ForceInstallLatest downloads and installs the latest manifest even when the
// version matches the current build or falls outside maintenance windows.
// Publisher trust, version pins and downgrade restrictions are never bypassed.
func (m *Manager) ForceInstallLatest(ctx context.Context, reason string) error {
	return m.ForceInstallLatestFromChannel(ctx, reason, "")
}

type manualUpdateChannelKey struct{}

// ForceInstallLatestFromChannel selects a channel for this operation only.
// Scheduled policy and the configured channel are not changed by a manual install.
func (m *Manager) ForceInstallLatestFromChannel(ctx context.Context, reason, channel string) (operationErr error) {
	ctx = context.WithValue(ctx, manualInstallKey{}, true)
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "" && !isKnownChannel(channel) {
		return fmt.Errorf("invalid update channel: %s", channel)
	}
	if channel != "" {
		ctx = context.WithValue(ctx, manualUpdateChannelKey{}, channel)
		ctx = updatepolicy.WithExplicitChannel(ctx)
	}
	if err := m.acquireOperation(); err != nil {
		return err
	}
	defer m.operationMu.Unlock()
	ctx, endOperation := m.operationContext(ctx)
	defer endOperation()

	if err := m.beginOperation(ctx); err != nil {
		return err
	}
	defer func() { operationErr = m.finishOperation(operationErr) }()

	if !m.enabled {
		if m.disabledReason != "" {
			return fmt.Errorf("auto-update disabled: %s", m.disabledReason)
		}
		return fmt.Errorf("auto-update disabled")
	}

	if m.client == nil {
		return fmt.Errorf("update client not configured")
	}

	if err := m.validateExplicitChannel(channel); err != nil {
		m.logWarn("Rejected manual update channel", "channel", channel, "error", err)
		m.setStatusWithError(StatusFailed, ErrCodeChannelUnsupported, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeChannelUnsupported, err.Error())
		return err
	}

	manifest, err := m.fetchManifest(ctx)
	if err != nil {
		m.reportTelemetry(ctx, StatusFailed, manifestErrorCode(err), err.Error())
		return fmt.Errorf("failed to fetch manifest: %w", err)
	}
	if manifest == nil {
		return fmt.Errorf("no manifest available")
	}
	if m.IsCancelled() {
		return context.Canceled
	}
	if channel != "" && manifest.Channel != channel {
		return fmt.Errorf("manifest channel %q does not match requested channel %q", manifest.Channel, channel)
	}

	m.mu.Lock()
	m.lastCheck = m.clock()
	m.latestVersion = manifest.Version
	m.latestManifest = manifest
	m.latestApplicable = false
	m.currentRun.TargetVersion = manifest.Version
	m.mu.Unlock()

	if reason == "" {
		reason = "manual"
	}
	m.logInfo("Force update requested", "current", m.currentVersion, "target", manifest.Version, "reason", reason)
	if _, err := m.applicationPolicy(ctx, manifest, true); err != nil {
		m.setStatusWithError(StatusFailed, ErrCodeApplicationPolicy, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeApplicationPolicy, err.Error())
		return err
	}

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

func (m *Manager) performCheck(ctx context.Context) (operationErr error) {
	if err := m.acquireOperation(); err != nil {
		return err
	}
	defer m.operationMu.Unlock()
	ctx, endOperation := m.operationContext(ctx)
	defer endOperation()

	if err := m.beginOperation(ctx); err != nil {
		return err
	}
	defer func() { operationErr = m.finishOperation(operationErr) }()

	if !m.enabled || m.client == nil {
		return fmt.Errorf("auto-update unavailable: %s", m.disabledReason)
	}
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
	if manifest == nil {
		return fmt.Errorf("no manifest available")
	}
	if m.IsCancelled() {
		return context.Canceled
	}

	m.mu.Lock()
	m.lastCheck = m.clock()
	m.latestVersion = manifest.Version
	m.latestManifest = manifest
	m.latestApplicable = false
	m.currentRun.TargetVersion = manifest.Version
	m.mu.Unlock()

	// Check if update is needed
	needed, err := m.applicationPolicy(ctx, manifest, false)
	if err != nil {
		m.setStatusWithError(StatusFailed, ErrCodeApplicationPolicy, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeApplicationPolicy, err.Error())
		return err
	}
	if !needed {
		m.logDebug("No update needed", "current", m.currentVersion, "latest", manifest.Version)
		return nil
	}
	m.mu.Lock()
	m.latestApplicable = true
	m.mu.Unlock()

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
	if err := m.unresolvedHelperHandoff(); err != nil {
		return err
	}
	if m.operationMu.TryLock() {
		m.mu.RLock()
		status := m.status
		m.mu.RUnlock()
		if status == StatusRestarting || status == StatusVerifying {
			m.operationMu.Unlock()
			return fmt.Errorf("update operation already in progress: %s", status)
		}
		return nil
	}
	m.mu.RLock()
	status := m.status
	m.mu.RUnlock()
	return fmt.Errorf("update operation already in progress: %s", status)
}

func (m *Manager) executeUpdate(ctx context.Context, manifest *UpdateManifest) error {
	ctx = context.WithValue(ctx, operationChannelKey{}, m.operationChannel(ctx))
	if manifest != nil {
		candidate := *manifest
		manifest = &candidate
	}
	if err := m.authorizeManifest(ctx, manifest); err != nil {
		m.logWarn("Blocked update before installation", "error", err)
		m.reportTelemetry(ctx, StatusFailed, ErrCodeManifestError, err.Error())
		return err
	}
	manual, _ := ctx.Value(manualInstallKey{}).(bool)
	allowed, err := m.applicationPolicy(ctx, manifest, manual)
	if err != nil || !allowed {
		if err == nil {
			err = fmt.Errorf("%w: candidate is not applicable", ErrApplicationPolicy)
		}
		m.setStatusWithError(StatusFailed, ErrCodeApplicationPolicy, err.Error())
		m.reportTelemetry(ctx, StatusFailed, ErrCodeApplicationPolicy, err.Error())
		return err
	}
	if m.usePackageManager && m.packageName != "" {
		return m.executeUpdateViaPackageManager(ctx, manifest)
	}

	run := &UpdateRun{
		ID:             m.Progress().RunID,
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
	downloadManifest := *manifest
	if err := m.downloadWithRetry(ctx, &downloadManifest, downloadPath); err != nil {
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
	descriptor, err := m.installationDescriptor(ctx, manifest, stagingPath, backupPath)
	if err != nil {
		return err
	}
	if err := m.backupBinary(backupPath); err != nil {
		return fmt.Errorf("cannot apply update without a recovery backup: %w", err)
	}

	updateMethod := "binary"
	if strings.HasSuffix(stagingPath, ".msi") {
		updateMethod = "msi"
	}
	// Apply phase - NO CANCELLATION AFTER THIS POINT (65-80% of overall progress)
	m.setStatusWithProgress(StatusApplying, 68, "Applying update (cannot cancel)...")
	run.Status = StatusApplying
	m.reportTelemetry(ctx, StatusApplying, "", "")

	if err := m.writePendingUpdateState(manifest.Version, updateMethod, &descriptor); err != nil {
		return err
	}
	if err := m.applyUpdate(ctx, descriptor); err != nil {
		run.Status = StatusFailed
		run.ErrorCode = ErrCodeApplyFailed
		if errors.Is(err, errInstallationNotStarted) {
			run.ErrorCode = installationErrorCode(err)
		}
		run.ErrorMessage = err.Error()
		run.CompletedAt = m.clock()
		m.setStatusWithError(StatusFailed, run.ErrorCode, err.Error())
		m.reportTelemetry(ctx, StatusFailed, run.ErrorCode, err.Error())
		// Attempt rollback
		if errors.Is(err, errInstallationNotStarted) {
			if removeErr := os.Remove(filepath.Join(m.stateDir, pendingUpdateFile)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return errors.Join(err, fmt.Errorf("remove pending update after rejected handoff: %w", removeErr))
			}
		} else {
			if rollbackErr := m.rollback(descriptor); rollbackErr != nil {
				m.logError("Rollback failed", "error", rollbackErr)
			}
		}
		return err
	}

	run.Status = StatusRestarting
	m.setStatusWithProgress(StatusRestarting, 80, "Restarting agent...")
	m.reportTelemetry(ctx, StatusRestarting, "", "")

	m.logInfo("Update applied successfully", "from", m.currentVersion, "to", manifest.Version)

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
	// Own a snapshot: the transport must not retain mutable installation inputs.
	candidate := *manifest
	manifest = &candidate
	if err := validateManifestFormat(manifest, format); err != nil {
		m.logWarn("Rejected update manifest", "requested_format", format, "manifest_format", manifest.Format, "version", manifest.Version, "error", err)
		return nil, err
	}
	if err := m.authorizeManifest(ctx, manifest); err != nil {
		m.logWarn("Rejected update publisher authorization", "version", manifest.Version,
			"channel", m.operationChannel(ctx), "error", err)
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
	if errors.Is(err, errManifestFormat) || errors.Is(err, ErrPublisherTrust) {
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

// A verified discovery binary does not authenticate repository packages.
// Keep every package handoff blocked until authenticated package delivery exists.
func (m *Manager) executeUpdateViaPackageManager(ctx context.Context, manifest *UpdateManifest) error {
	err := fmt.Errorf("%w: %s package delivery is not yet publisher-authenticated; unattended update blocked",
		ErrPublisherTrust, m.packageManager)
	m.logWarn("Blocked unauthenticated package update", "package_manager", m.packageManager, "error", err)
	m.reportTelemetry(ctx, StatusFailed, ErrCodeManifestError, err.Error())
	return err
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
			if manifest.SizeBytes > 0 && resumeFrom >= manifest.SizeBytes {
				if resumeFrom == manifest.SizeBytes && m.verifyHash(destPath+".partial", manifest.SHA256) == nil {
					return os.Rename(destPath+".partial", destPath)
				}
				m.logWarn("Discarding invalid completed partial artifact", "bytes", resumeFrom, "expected_bytes", manifest.SizeBytes)
				if err := os.Remove(destPath + ".partial"); err != nil {
					return fmt.Errorf("remove invalid partial artifact: %w", err)
				}
				resumeFrom = 0
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect partial artifact: %w", err)
		}

		downloaded, err := m.client.DownloadArtifactWithProgress(ctx, manifest, destPath+".partial", resumeFrom, progressCb)
		if err == nil && downloaded > 0 {
			if manifest.SizeBytes > 0 && downloaded != manifest.SizeBytes {
				err = fmt.Errorf("artifact size mismatch: expected %d bytes, received %d", manifest.SizeBytes, downloaded)
			} else if verifyErr := m.verifyHash(destPath+".partial", manifest.SHA256); verifyErr != nil {
				err = verifyErr
				m.logWarn("Discarding artifact after verification failure", "attempt", attempt, "error", err)
				if removeErr := os.Remove(destPath + ".partial"); removeErr != nil {
					return errors.Join(err, removeErr)
				}
			} else {
				// Rename partial to final
				if err := os.Rename(destPath+".partial", destPath); err != nil {
					return fmt.Errorf("failed to finalize download: %w", err)
				}
				return nil
			}
		} else if err == nil {
			err = fmt.Errorf("artifact download returned no bytes")
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
	if err := copyFile(m.binaryPath, backupPath); err != nil {
		return err
	}
	return os.Chmod(backupPath, 0o600)
}

func (m *Manager) applyUpdate(ctx context.Context, descriptor InstallationDescriptor) error {
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
	if err := m.validateInstallation(ctx, descriptor); err != nil {
		return fmt.Errorf("%w: %w", errInstallationNotStarted, err)
	}

	// On Windows, we can't replace a running binary directly.
	// We need to use a helper process or schedule the replacement.
	if runtime.GOOS == "windows" {
		return m.applyUpdateWindows(descriptor)
	}

	// On Unix, we can typically just rename
	return m.applyUpdateUnix(descriptor.StagingPath)
}

func (m *Manager) applyUpdateUnix(stagingPath string) error {
	// Make the new binary executable
	if err := os.Chmod(stagingPath, 0o755); err != nil {
		return fmt.Errorf("failed to set executable permission: %w", err)
	}

	// Try to rename the staging file to replace the current binary.
	// This is the fastest approach when both paths are on the same filesystem.
	if err := os.Rename(stagingPath, m.binaryPath); err != nil {
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
			return fmt.Errorf("safe atomic update cannot cross filesystems; install the Agent in a supported location or use its package manager")
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
	if output, err := m.runUpdateCommand(installerCommandTimeout, []string{"DEBIAN_FRONTEND=noninteractive"}, "sudo", "-n", "apt-get", "update", "-qq"); err != nil {
		m.logWarn("apt-get update failed (continuing anyway)", "error", err, "output", string(output))
		// Don't fail here - the package might already be in cache
	}
	progressFn(30, "Package metadata updated")

	// Phase 2: Download and install (30-90%)
	// Format: packagename=version (e.g., printmaster-agent=0.27.4)
	packageSpec := fmt.Sprintf("%s=%s", m.packageName, targetVersion)

	progressFn(35, fmt.Sprintf("Downloading %s...", packageSpec))
	m.logInfo("Running sudo apt-get install", "package", packageSpec)
	output, err := m.runUpdateCommand(installerCommandTimeout, []string{"DEBIAN_FRONTEND=noninteractive"}, "sudo", "-n", "apt-get", "install", "-y", "-qq", "--allow-downgrades", packageSpec)
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

	output, err := m.runUpdateCommand(installerCommandTimeout, nil, "sudo", "-n", "dnf",
		"--setopt=logdir=/tmp",
		"--refresh", // Force metadata refresh
		"install",
		"-y",
		"--allowerasing", // Allow replacing conflicting packages
		packageSpec)
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
	if output, err := m.runUpdateCommand(installerCommandTimeout, nil, "sudo", "-n", "yum", "clean", "metadata", "-q"); err != nil {
		m.logWarn("yum metadata clean failed; using existing metadata", "error", err, "output", string(output))
	}
	progressFn(20, "Cache cleaned")

	// Phase 2: Resolve and download (20-60%)
	packageSpec := fmt.Sprintf("%s-%s", m.packageName, targetVersion)

	progressFn(25, fmt.Sprintf("Resolving dependencies for %s...", packageSpec))
	progressFn(35, fmt.Sprintf("Downloading %s...", packageSpec))

	m.logInfo("Running sudo yum install", "package", packageSpec)
	output, err := m.runUpdateCommand(installerCommandTimeout, nil, "sudo", "-n", "yum", "install", "-y", "-q", packageSpec)
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

func (m *Manager) applyUpdateWindows(descriptor InstallationDescriptor) error {
	// If using MSI, delegate to MSI-specific update method
	if m.useMSI {
		return m.applyUpdateWindowsMSI(descriptor)
	}
	return m.launchWindowsUpdateHelper(descriptor, false)
}

// applyUpdateWindowsMSI applies an update using the Windows MSI installer.
// This method downloads an MSI and runs msiexec to perform an upgrade.
func (m *Manager) applyUpdateWindowsMSI(descriptor InstallationDescriptor) error {
	return m.launchWindowsUpdateHelper(descriptor, true)
}

func (m *Manager) launchWindowsUpdateHelper(descriptor InstallationDescriptor, useMSI bool) error {
	name := "update_helper.ps1"
	if useMSI {
		name = "msi_update_helper.ps1"
	}
	helperPath := filepath.Join(m.stateDir, name)
	resultPath := filepath.Join(m.stateDir, windowsHelperResultFile)
	logPath := filepath.Join(m.stateDir, "msi_update.log")
	serviceName := m.serviceName
	if serviceName == "" {
		serviceName = "PrintMasterAgent"
	}
	script := windowsHelperScript(descriptor, serviceName, m.isService, useMSI, os.Getpid(), resultPath, logPath)
	if err := writeUpdateState(helperPath, []byte(script)); err != nil {
		return fmt.Errorf("write update helper: %w", err)
	}
	if _, err := os.Stat(helperPath); err != nil {
		return fmt.Errorf("inspect update helper: %w", err)
	}

	if m.launchHelperFn != nil {
		if err := m.launchHelperFn(helperPath); err != nil {
			return fmt.Errorf("launch update helper: %w", err)
		}
	} else if err := launchDetachedWindowsHelper(helperPath); err != nil {
		return fmt.Errorf("launch update helper: %w", err)
	}
	m.logInfo("Update helper launched", "helper_path", helperPath, "outcome_path", resultPath,
		"run_id", descriptor.RunID, "msi", useMSI, "is_service", m.isService)
	return nil
}

func (m *Manager) rollback(descriptor InstallationDescriptor) error {
	if m.Progress().Status != string(StatusFailed) ||
		descriptor.RunID == "" || descriptor.RunID != m.Progress().RunID ||
		descriptor.FromVersion != m.currentVersion || descriptor.BinaryPath != m.binaryPath ||
		filepath.Dir(descriptor.BackupPath) != filepath.Join(m.stateDir, backupDirName) {
		return fmt.Errorf("rollback is limited to the recorded previous installation of this failed run")
	}
	for _, path := range []string{descriptor.BinaryPath, descriptor.BackupPath} {
		if err := validateInstallPath(path); err != nil {
			return fmt.Errorf("unsafe rollback path %s: %w", path, err)
		}
	}
	if err := descriptor.Previous.verify(descriptor.BackupPath); err != nil {
		return fmt.Errorf("rollback backup verification failed: %w", err)
	}

	if err := copyFile(descriptor.BackupPath, descriptor.BinaryPath); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		mode := descriptor.Previous.Mode
		if mode == 0 {
			mode = 0o755
		}
		if err := os.Chmod(descriptor.BinaryPath, os.FileMode(mode)); err != nil {
			return fmt.Errorf("restore previous executable permissions: %w", err)
		}
	}
	m.logInfo("Restored recorded previous installation after failed update",
		"run_id", descriptor.RunID, "version", descriptor.FromVersion)
	return nil
}

// writePendingUpdateState saves the expected target version before restart.
// This allows the new version to validate the update was successful on startup.
func (m *Manager) writePendingUpdateState(targetVersion, method string, installation ...*InstallationDescriptor) error {
	m.mu.RLock()
	journalErr := m.progressErr
	m.mu.RUnlock()
	if journalErr != nil {
		return fmt.Errorf("cannot apply update without durable progress: %w", journalErr)
	}
	state := PendingUpdateState{
		RunID:         m.Progress().RunID,
		TargetVersion: targetVersion,
		FromVersion:   m.currentVersion,
		StartedAt:     m.clock(),
		Method:        method,
	}
	if runtime.GOOS == "windows" && len(installation) > 0 && (method == "binary" || method == "msi") {
		state.HelperResultPath = filepath.Join(m.stateDir, windowsHelperResultFile)
	}
	if len(installation) > 0 {
		state.Installation = installation[0]
	}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal pending update state: %w", err)
	}

	pendingPath := filepath.Join(m.stateDir, pendingUpdateFile)
	if err := writeUpdateState(pendingPath, data); err != nil {
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

	var state PendingUpdateState
	if err := json.Unmarshal(data, &state); err != nil {
		if preserveErr := os.Rename(pendingPath, pendingPath+fmt.Sprintf(".corrupt-%d", time.Now().UnixNano())); preserveErr != nil {
			return false, "", errors.Join(err, preserveErr)
		}
		if m.Progress().Terminal() {
			if beginErr := m.beginOperation(context.Background()); beginErr != nil {
				return false, "", errors.Join(err, beginErr)
			}
		}
		m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
		return false, "", fmt.Errorf("failed to parse pending update state: %w", err)
	}
	snapshot := m.Progress()
	if snapshot.RunID == "" || snapshot.Terminal() || state.RunID != "" && state.RunID != snapshot.RunID {
		if err := m.beginOperation(context.Background()); err != nil {
			return true, state.FromVersion, err
		}
	}
	m.mu.Lock()
	m.currentRun = &UpdateRun{ID: m.progress.RunID, TargetVersion: state.TargetVersion, CurrentVersion: state.FromVersion}
	m.mu.Unlock()
	if err := m.publishProgress(StatusVerifying, 90, "Validating the running agent version...", ""); err != nil {
		return true, state.FromVersion, err
	}

	var outcome *helperOutcome
	if state.HelperResultPath != "" {
		result, err := m.readHelperOutcome(state)
		if err != nil {
			m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
			return true, state.FromVersion, err
		}
		outcome = &result
		if result.Status == "running" {
			m.startHelperReconciliation(state)
			return true, state.FromVersion, nil
		}
		if result.Status != "succeeded" {
			message := result.Message
			if message == "" {
				message = fmt.Sprintf("Update helper ended with status %s", result.Status)
			}
			validationErr := fmt.Errorf("update helper failed: %s", message)
			if err := m.publishProgress(StatusFailed, -1, message, validationErr.Error()); err != nil {
				return true, state.FromVersion, errors.Join(validationErr, err)
			}
			if result.Status != "timed_out" {
				if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					return true, state.FromVersion, errors.Join(validationErr, err)
				}
			}
			return true, state.FromVersion, validationErr
		}
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
		message := fmt.Sprintf("update validation failed: expected version %s but running %s",
			state.TargetVersion, m.currentVersion)
		if outcome != nil && outcome.Message != "" {
			message += "; helper: " + outcome.Message
		}
		validationErr := errors.New(message)
		if err := m.publishProgress(StatusFailed, -1, validationErr.Error(), validationErr.Error()); err != nil {
			return true, state.FromVersion, errors.Join(validationErr, err)
		}
		if err := os.Remove(pendingPath); err != nil {
			return true, state.FromVersion, errors.Join(validationErr, err)
		}
		return true, state.FromVersion, validationErr
	}

	if state.Installation != nil {
		descriptor := state.Installation
		if descriptor.RunID != state.RunID || descriptor.FromVersion != state.FromVersion ||
			descriptor.Authorization.Target.Version != state.TargetVersion {
			err := fmt.Errorf("pending installation descriptor does not match the recorded run")
			m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
			return true, state.FromVersion, err
		}
		if state.Method == "binary" {
			target := descriptor.Authorization.Target
			if err := (ArtifactIdentity{SHA256: target.SHA256, SizeBytes: target.SizeBytes}).verify(m.binaryPath); err != nil {
				m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
				return true, state.FromVersion, fmt.Errorf("running installation does not match recorded artifact: %w", err)
			}
		}
		receipt, err := json.Marshal(descriptor)
		if err != nil {
			m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
			return true, state.FromVersion, err
		}
		if err := writeUpdateState(filepath.Join(m.stateDir, "installation.json"), receipt); err != nil {
			m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
			return true, state.FromVersion, fmt.Errorf("persist installation receipt: %w", err)
		}
	}
	// Success!
	m.logInfo("Post-update validation PASSED",
		"new_version", m.currentVersion,
		"previous_version", state.FromVersion,
		"update_method", state.Method,
		"update_duration", m.clock().Sub(state.StartedAt).String())

	if err := m.publishProgress(StatusSucceeded, 100, "Update completed; running version "+m.currentVersion, ""); err != nil {
		return true, state.FromVersion, err
	}
	if err := os.Remove(pendingPath); err != nil {
		return true, state.FromVersion, err
	}
	return true, state.FromVersion, nil
}

func (m *Manager) readHelperOutcome(state PendingUpdateState) (helperOutcome, error) {
	expectedPath := filepath.Join(m.stateDir, windowsHelperResultFile)
	if filepath.Clean(state.HelperResultPath) != filepath.Clean(expectedPath) {
		return helperOutcome{}, fmt.Errorf("pending update contains an unexpected helper outcome path")
	}
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		return helperOutcome{}, fmt.Errorf("read durable update helper outcome: %w", err)
	}
	var outcome helperOutcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		return helperOutcome{}, fmt.Errorf("parse durable update helper outcome: %w", err)
	}
	if outcome.RunID != state.RunID || outcome.Target != state.TargetVersion ||
		outcome.UpdatedAt.IsZero() ||
		(outcome.Status != "running" && outcome.Status != "succeeded" &&
			outcome.Status != "failed" && outcome.Status != "timed_out") {
		return helperOutcome{}, fmt.Errorf("durable update helper outcome does not match the pending run")
	}
	return outcome, nil
}

func (m *Manager) startHelperReconciliation(state PendingUpdateState) {
	m.mu.Lock()
	if m.helperReconcile {
		m.mu.Unlock()
		return
	}
	m.helperReconcile = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.helperReconcile = false
			m.mu.Unlock()
		}()
		reconciliationTimeout := windowsHelperParentExitTimeout + 2*windowsHelperServiceTimeout
		if state.Method == "msi" {
			reconciliationTimeout += windowsHelperMSITimeout
		}
		deadline := time.Now().Add(reconciliationTimeout)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for time.Now().Before(deadline) {
			outcome, err := m.readHelperOutcome(state)
			if err == nil && outcome.Status != "running" {
				if _, _, validationErr := m.ValidatePostUpdate(); validationErr != nil {
					m.logError("Update helper outcome reconciliation failed", "run_id", state.RunID, "error", validationErr)
				}
				return
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				m.setStatusWithError(StatusFailed, ErrCodeHealthCheck, err.Error())
				m.logError("Update helper outcome reconciliation failed", "run_id", state.RunID, "error", err)
				return
			}
			<-ticker.C
		}
		err := fmt.Errorf("update helper did not report a final result before the reconciliation deadline")
		m.setStatusWithError(StatusFailed, ErrCodeApplyFailed, err.Error())
		m.logError("Update helper outcome remains unresolved", "run_id", state.RunID, "error", err)
	}()
}

func (m *Manager) unresolvedHelperHandoff() error {
	data, err := os.ReadFile(filepath.Join(m.stateDir, pendingUpdateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect pending helper handoff: %w", err)
	}
	var state PendingUpdateState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("cannot start another update while pending handoff state is invalid: %w", err)
	}
	if state.HelperResultPath == "" {
		return nil
	}
	outcome, err := m.readHelperOutcome(state)
	if err != nil {
		return fmt.Errorf("cannot start another update before the previous helper outcome is reconciled: %w", err)
	}
	if outcome.Status == "running" || outcome.Status == "timed_out" {
		return fmt.Errorf("cannot start another update while the previous installer handoff is unresolved: %s", outcome.Message)
	}
	return nil
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
		if output, err := m.runUpdateCommand(30*time.Second, nil, "sudo", "-n", "systemctl", "restart", "--no-block", serviceName); err != nil {
			return m.wrapSudoError("systemctl restart", err, output)
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

func (m *Manager) isVersionAllowed(target *semver.Version, spec updatepolicy.PolicySpec) bool {
	if spec.TargetVersion != "" {
		// Specific target version pinned
		targetPinned, err := parseSemverVersion(spec.TargetVersion)
		if err != nil || !target.Equal(targetPinned) {
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
		return target.Equal(m.currentSemver)
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
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if trimmed == "" {
		return nil, fmt.Errorf("empty version")
	}
	if ver, err := semver.NewVersion(trimmed); err == nil {
		return ver, nil
	}
	// Git's short hash can be all digits with a leading zero. That is a valid
	// Dev build identity but not a SemVer numeric prerelease identifier.
	if base, hash, ok := strings.Cut(trimmed, "-dev."); ok && len(hash) >= 7 && len(hash) <= 40 && hash[0] == '0' {
		digits := true
		for _, char := range hash {
			if char < '0' || char > '9' {
				digits = false
				break
			}
		}
		if digits {
			return semver.NewVersion(base + "-dev.g" + hash)
		}
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
	if err := m.publishProgress(s, progress, message, ""); err != nil {
		m.logError("Failed to persist update progress", "error", err)
	}
}

func (m *Manager) setStatusWithError(s Status, errCode, errMsg string) {
	if err := m.publishProgress(s, -1, errMsg, fmt.Sprintf("%s: %s", errCode, errMsg)); err != nil {
		m.logError("Failed to persist update failure", "error", err)
	}
}

// Cancel attempts to cancel an in-progress update. Returns true if cancellation
// was successful, false if the update is in a non-cancellable phase (restarting).
func (m *Manager) Cancel() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Cannot cancel once we're restarting - it's too late
	if m.status == StatusRestarting || m.status == StatusApplying || m.status == StatusVerifying {
		return false
	}

	// If not in an active update phase, nothing to cancel
	if m.status == StatusIdle || m.progress.Terminal() {
		return false
	}

	m.cancelled = true
	if m.operationCancel != nil {
		m.operationCancel()
	}
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
