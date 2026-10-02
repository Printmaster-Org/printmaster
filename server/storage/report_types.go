package storage

import (
	"time"
)

// ============================================================
// Report Types
// ============================================================

// ReportType represents the type of report (string type for backwards compatibility)
type ReportType = string

const (
	ReportTypeDeviceInventory  ReportType = "device_inventory"
	ReportTypeAgentInventory   ReportType = "agent_inventory"
	ReportTypeSiteInventory    ReportType = "site_inventory"
	ReportTypeUsageSummary     ReportType = "usage_summary"
	ReportTypeSuppliesStatus   ReportType = "supplies_status"
	ReportTypeSuppliesLow      ReportType = "supplies_low"
	ReportTypeSuppliesCritical ReportType = "supplies_critical"
	ReportTypeAlertHistory     ReportType = "alert_history"
	ReportTypeAlertSummary     ReportType = "alert_summary"
	ReportTypeAgentStatus      ReportType = "agent_status"
	ReportTypeAgentHealth      ReportType = "agent_health"
	ReportTypeFleetHealth      ReportType = "fleet_health"
	ReportTypeHealthSummary    ReportType = "health_summary"
	ReportTypeCostAnalysis     ReportType = "cost_analysis"
	ReportTypeUsageByDevice    ReportType = "usage_by_device"
	ReportTypeUsageByAgent     ReportType = "usage_by_agent"
	ReportTypeUsageBySite      ReportType = "usage_by_site"
	ReportTypeUsageByTenant    ReportType = "usage_by_tenant"
	ReportTypeUsageTrends      ReportType = "usage_trends"
	ReportTypeTopPrinters      ReportType = "top_printers"
	ReportTypeOfflineDevices   ReportType = "offline_devices"
	ReportTypeErrorDevices     ReportType = "error_devices"
	ReportTypeCustom           ReportType = "custom"
)

// GetBuiltInReportTypes returns a list of all built-in report types.
func GetBuiltInReportTypes() []ReportType {
	return []ReportType{
		ReportTypeDeviceInventory,
		ReportTypeUsageSummary,
		ReportTypeSuppliesStatus,
		ReportTypeAlertHistory,
		ReportTypeAgentStatus,
		ReportTypeFleetHealth,
		ReportTypeUsageByDevice,
		ReportTypeUsageByAgent,
		ReportTypeUsageBySite,
		ReportTypeUsageByTenant,
		ReportTypeUsageTrends,
		ReportTypeTopPrinters,
	}
}

// ReportFormat represents the output format of a report (string type for backwards compatibility)
type ReportFormat = string

const (
	ReportFormatJSON ReportFormat = "json"
	ReportFormatCSV  ReportFormat = "csv"
	ReportFormatPDF  ReportFormat = "pdf"
	ReportFormatHTML ReportFormat = "html"
)

// ReportScope represents the scope of a report (string type for backwards compatibility)
type ReportScope = string

const (
	ReportScopeFleet  ReportScope = "fleet"
	ReportScopeTenant ReportScope = "tenant"
	ReportScopeSite   ReportScope = "site"
	ReportScopeAgent  ReportScope = "agent"
	ReportScopeDevice ReportScope = "device"
)

// ReportStatus represents the status of a report run (string type for backwards compatibility)
type ReportStatus = string

const (
	ReportStatusPending   ReportStatus = "pending"
	ReportStatusRunning   ReportStatus = "running"
	ReportStatusCompleted ReportStatus = "completed"
	ReportStatusFailed    ReportStatus = "failed"
	ReportStatusCancelled ReportStatus = "cancelled"
)

// ReportDefinition represents a report template/definition
type ReportDefinition struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	Type            string    `json:"type"`
	Format          string    `json:"format"`
	Scope           string    `json:"scope"`
	TenantIDs       []string  `json:"tenant_ids,omitempty"`
	SiteIDs         []string  `json:"site_ids,omitempty"`
	AgentIDs        []string  `json:"agent_ids,omitempty"`
	DeviceFilter    string    `json:"device_filter,omitempty"`
	TimeRangeType   string    `json:"time_range_type,omitempty"`
	TimeRangeDays   int       `json:"time_range_days,omitempty"`
	TimeRangeStart  string    `json:"time_range_start,omitempty"`
	TimeRangeEnd    string    `json:"time_range_end,omitempty"`
	OptionsJSON     string    `json:"options_json,omitempty"`
	Columns         []string  `json:"columns,omitempty"`
	GroupBy         []string  `json:"group_by,omitempty"`
	OrderBy         string    `json:"order_by,omitempty"`
	Limit           int       `json:"limit,omitempty"`
	EmailRecipients []string  `json:"email_recipients,omitempty"`
	WebhookURL      string    `json:"webhook_url,omitempty"`
	CreatedBy       string    `json:"created_by,omitempty"`
	IsBuiltIn       bool      `json:"is_built_in"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ReportFilter defines filters for listing reports
type ReportFilter struct {
	Type      string `json:"type,omitempty"`
	Scope     string `json:"scope,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	BuiltIn   *bool  `json:"built_in,omitempty"`
	IsBuiltIn *bool  `json:"is_built_in,omitempty"` // Alias for BuiltIn
	TenantID  string `json:"tenant_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

// ReportRunFilter defines filters for listing report runs
type ReportRunFilter struct {
	ReportID   int64      `json:"report_id,omitempty"`
	ScheduleID *int64     `json:"schedule_id,omitempty"`
	Status     string     `json:"status,omitempty"`
	StartTime  *time.Time `json:"start_time,omitempty"`
	EndTime    *time.Time `json:"end_time,omitempty"`
	Since      *time.Time `json:"since,omitempty"` // Alias for StartTime
	Until      *time.Time `json:"until,omitempty"` // Alias for EndTime
	Limit      int        `json:"limit,omitempty"`
	Offset     int        `json:"offset,omitempty"`
	// MetadataOnly excludes inline result bodies from the database projection.
	// Default false preserves existing storage callers' result-bearing reads.
	MetadataOnly bool `json:"-"`
}

// ReportFrequency represents how often a scheduled report runs (string type for backwards compatibility)
type ReportFrequency = string

const (
	ReportFrequencyDaily   ReportFrequency = "daily"
	ReportFrequencyWeekly  ReportFrequency = "weekly"
	ReportFrequencyMonthly ReportFrequency = "monthly"
)

// ScheduleFrequency is an alias for ReportFrequency for backwards compatibility
type ScheduleFrequency = ReportFrequency

const (
	ScheduleFrequencyDaily   ScheduleFrequency = ReportFrequencyDaily
	ScheduleFrequencyWeekly  ScheduleFrequency = ReportFrequencyWeekly
	ScheduleFrequencyMonthly ScheduleFrequency = ReportFrequencyMonthly
)

// ReportSchedule represents a scheduled report run
type ReportSchedule struct {
	ID           int64      `json:"id"`
	ReportID     int64      `json:"report_id"`
	Name         string     `json:"name"`
	Enabled      bool       `json:"enabled"`
	Frequency    string     `json:"frequency"`
	DayOfWeek    int        `json:"day_of_week,omitempty"`
	DayOfMonth   int        `json:"day_of_month,omitempty"`
	TimeOfDay    string     `json:"time_of_day"`
	Timezone     string     `json:"timezone"`
	NextRunAt    time.Time  `json:"next_run_at"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	LastRunID    *int64     `json:"last_run_id,omitempty"`
	FailureCount int        `json:"failure_count"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ReportRunStatus represents the status of a report run (string type for backwards compatibility)
type ReportRunStatus = string

const (
	ReportRunStatusPending   ReportRunStatus = "pending"
	ReportRunStatusRunning   ReportRunStatus = "running"
	ReportRunStatusCompleted ReportRunStatus = "completed"
	ReportRunStatusFailed    ReportRunStatus = "failed"
)

// ReportRun represents a single execution of a report
type ReportRun struct {
	ID              int64      `json:"id"`
	ReportID        int64      `json:"report_id"`
	ReportName      string     `json:"report_name,omitempty"`
	ReportType      string     `json:"report_type,omitempty"`
	ScheduleID      *int64     `json:"schedule_id,omitempty"`
	Status          string     `json:"status"`
	Format          string     `json:"format"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	DurationMs      int64      `json:"duration_ms,omitempty"`
	DurationMS      int64      `json:"-"` // Alias for DurationMs
	ParametersJSON  string     `json:"parameters_json,omitempty"`
	RowCount        int        `json:"row_count,omitempty"`
	ResultSizeBytes int64      `json:"result_size_bytes,omitempty"`
	ResultSize      int64      `json:"-"` // Alias for ResultSizeBytes
	ResultPath      string     `json:"result_path,omitempty"`
	ResultData      string     `json:"result_data,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	RunBy           string     `json:"run_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ReportSummary provides a summary of reporting state
type ReportSummary struct {
	TotalReports      int            `json:"total_reports"`
	BuiltInReports    int            `json:"built_in_reports"`
	CustomReports     int            `json:"custom_reports"`
	ActiveSchedules   int            `json:"active_schedules"`
	TotalSchedules    int            `json:"total_schedules"`
	TotalRuns         int            `json:"total_runs"`
	RunsToday         int            `json:"runs_today"`
	RunsLast24h       int            `json:"runs_last_24h"`
	FailedRunsToday   int            `json:"failed_runs_today"`
	FailedRunsLast24h int            `json:"failed_runs_last_24h"`
	SuccessfulRuns    int            `json:"successful_runs"`
	AverageRunTimeMS  int64          `json:"average_run_time_ms"`
	StorageUsedBytes  int64          `json:"storage_used_bytes"`
	ReportsByType     map[string]int `json:"reports_by_type"`
}
