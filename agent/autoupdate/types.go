package autoupdate

import (
	"time"

	"printmaster/common/updatepolicy"
)

// Status enumerates the lifecycle phases of an agent update attempt.
type Status string

const (
	StatusIdle        Status = "idle"
	StatusChecking    Status = "checking"
	StatusPending     Status = "pending"
	StatusDownloading Status = "downloading"
	StatusStaging     Status = "staging"
	StatusApplying    Status = "applying"
	StatusRestarting  Status = "restarting"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusSkipped     Status = "skipped"
	StatusRolledBack  Status = "rolled_back"
	StatusCancelled   Status = "cancelled"
)

// UpdateRun captures the state and history of a single update attempt.
type UpdateRun struct {
	ID             string         `json:"id"`
	Status         Status         `json:"status"`
	RequestedAt    time.Time      `json:"requested_at"`
	StartedAt      time.Time      `json:"started_at,omitempty"`
	CompletedAt    time.Time      `json:"completed_at,omitempty"`
	CurrentVersion string         `json:"current_version"`
	TargetVersion  string         `json:"target_version,omitempty"`
	Channel        string         `json:"channel"`
	Platform       string         `json:"platform"`
	Arch           string         `json:"arch"`
	SizeBytes      int64          `json:"size_bytes,omitempty"`
	DownloadedAt   time.Time      `json:"downloaded_at,omitempty"`
	DownloadTimeMs int64          `json:"download_time_ms,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	PolicySource   string         `json:"policy_source,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// ManagerStatus exposes the current state of the update manager for UI/API.
type ManagerStatus struct {
	Enabled           bool       `json:"enabled"`
	DisabledReason    string     `json:"disabled_reason,omitempty"`
	CurrentVersion    string     `json:"current_version"`
	LatestVersion     string     `json:"latest_version,omitempty"`
	UpdateAvailable   bool       `json:"update_available"`
	Status            Status     `json:"status"`
	LastCheckAt       *time.Time `json:"last_check_at,omitempty"`
	NextCheckAt       *time.Time `json:"next_check_at,omitempty"`
	PolicySource      string     `json:"policy_source,omitempty"`
	CheckIntervalDays int        `json:"check_interval_days,omitempty"`
	Channel           string     `json:"channel"`
	Platform          string     `json:"platform"`
	Arch              string     `json:"arch"`
	UsePackageManager bool       `json:"use_package_manager,omitempty"` // True if updates use apt-get
	PackageName       string     `json:"package_name,omitempty"`        // Package name (e.g., "printmaster-agent")
	UseMSI            bool       `json:"use_msi,omitempty"`             // True if updates use Windows MSI
}

// UpdateManifest mirrors the server's signed manifest payload.
type UpdateManifest struct {
	ManifestVersion string `json:"manifest_version"`
	Component       string `json:"component"`
	Version         string `json:"version"`
	MinorLine       string `json:"minor_line"`
	Platform        string `json:"platform"`
	Arch            string `json:"arch"`
	// Format is the artifact packaging: "binary" or "msi". Servers that
	// predate formats omit it, which means binary.
	Format      string    `json:"format,omitempty"`
	Channel     string    `json:"channel"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"size_bytes"`
	SourceURL   string    `json:"source_url"`
	DownloadURL string    `json:"download_url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	Signature   string    `json:"signature,omitempty"`
}

// CheckResult captures the outcome of an update availability check.
type CheckResult struct {
	Available      bool                      `json:"available"`
	CurrentVersion string                    `json:"current_version"`
	LatestVersion  string                    `json:"latest_version,omitempty"`
	Manifest       *UpdateManifest           `json:"manifest,omitempty"`
	PolicySource   updatepolicy.PolicySource `json:"policy_source"`
	InWindow       bool                      `json:"in_window"`
	SkipReason     string                    `json:"skip_reason,omitempty"`
	CheckedAt      time.Time                 `json:"checked_at"`
}

// TelemetryPayload is sent to the server to report update progress/status.
type TelemetryPayload struct {
	AgentID        string         `json:"agent_id"`
	RunID          string         `json:"run_id,omitempty"`
	Status         Status         `json:"status"`
	CurrentVersion string         `json:"current_version"`
	TargetVersion  string         `json:"target_version,omitempty"`
	DownloadTimeMs int64          `json:"download_time_ms,omitempty"`
	SizeBytes      int64          `json:"size_bytes,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Timestamp      time.Time      `json:"timestamp"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// ErrorCode constants for structured error reporting.
const (
	ErrCodeDiskSpace      = "DISK_SPACE"
	ErrCodeDownloadFailed = "DOWNLOAD_FAILED"
	ErrCodeHashMismatch   = "HASH_MISMATCH"
	ErrCodeStagingFailed  = "STAGING_FAILED"
	ErrCodeApplyFailed    = "APPLY_FAILED"
	ErrCodeRestartFailed  = "RESTART_FAILED"
	ErrCodeHealthCheck    = "HEALTH_CHECK"
	ErrCodeRollbackFailed = "ROLLBACK_FAILED"
	ErrCodeManifestError  = "MANIFEST_ERROR"
	ErrCodePolicyDisabled = "POLICY_DISABLED"
	ErrCodeOutsideWindow  = "OUTSIDE_WINDOW"
	ErrCodeServerError    = "SERVER_ERROR"
)
