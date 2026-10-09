package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
	pmsettings "printmaster/common/settings"
	commonstorage "printmaster/common/storage"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Token validation errors for distinguishing token failure reasons
var (
	// ErrTokenInvalid indicates the token format is invalid or completely unknown
	ErrTokenInvalid = errors.New("invalid or unknown token")
	// ErrTokenExpired indicates a known token that has expired (can be captured)
	ErrTokenExpired = errors.New("token expired")
	// ErrTokenRevoked indicates a known token that was manually revoked
	ErrTokenRevoked = errors.New("token revoked")
)

// TokenValidationError wraps a token error with the original tenant for expired tokens
type TokenValidationError struct {
	Err      error
	TenantID string // Set when token was valid but expired/revoked
	TokenID  string // ID of the matched token (if known)
}

func (e *TokenValidationError) Error() string {
	if e.TenantID != "" {
		return fmt.Sprintf("%s (tenant: %s)", e.Err.Error(), e.TenantID)
	}
	return e.Err.Error()
}

func (e *TokenValidationError) Unwrap() error {
	return e.Err
}

// IsExpiredToken checks if an error is specifically an expired token error
func IsExpiredToken(err error) bool {
	var tve *TokenValidationError
	if errors.As(err, &tve) {
		return errors.Is(tve.Err, ErrTokenExpired)
	}
	return false
}

// Role represents the authorization level granted to a user.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// NormalizeRole ensures any persisted or user-provided role maps to a known value.
func NormalizeRole(value string) Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(RoleAdmin):
		return RoleAdmin
	case string(RoleOperator):
		return RoleOperator
	case "user":
		// Legacy value from pre-RBAC builds maps to operator by default.
		return RoleOperator
	default:
		return RoleViewer
	}
}

// DefaultRoles returns a deterministic list of built-in roles ordered by privilege.
func DefaultRoles() []Role {
	return []Role{RoleAdmin, RoleOperator, RoleViewer}
}

// SortTenantIDs normalizes tenant slices for stable storage/JSON.
func SortTenantIDs(ids []string) []string {
	res := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		res = append(res, id)
	}
	sort.Strings(res)
	return res
}

// NormalizeTenantDomain canonicalizes a tenant domain/host hint for comparison.
// It trims whitespace, strips any scheme/port/user portions, removes a leading
// '@' (when raw email addresses are provided), and lowercases the remaining
// domain. Returns an empty string when the input does not contain a usable
// domain segment.
func NormalizeTenantDomain(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	// Remove protocol prefixes if present.
	if idx := strings.Index(trimmed, "://"); idx >= 0 {
		trimmed = trimmed[idx+3:]
	}
	// Drop anything before the last '@' to support raw email inputs.
	if at := strings.LastIndex(trimmed, "@"); at >= 0 {
		trimmed = trimmed[at+1:]
	}
	// Strip path/query fragments.
	if slash := strings.IndexAny(trimmed, "/?"); slash >= 0 {
		trimmed = trimmed[:slash]
	}
	// Remove port suffix when provided.
	if colon := strings.Index(trimmed, ":"); colon >= 0 {
		trimmed = trimmed[:colon]
	}
	trimmed = strings.Trim(trimmed, ".")
	if trimmed == "" {
		return ""
	}
	return trimmed
}

// Agent represents a registered PrintMaster agent
type Agent struct {
	ID              int64     `json:"id"`
	AgentID         string    `json:"agent_id"` // Stable UUID identifier
	Name            string    `json:"name"`     // User-friendly name (defaults to hostname)
	Hostname        string    `json:"hostname"`
	IP              string    `json:"ip"`
	Platform        string    `json:"platform"`         // windows, linux, darwin
	Version         string    `json:"version"`          // Agent version
	ProtocolVersion string    `json:"protocol_version"` // Protocol compatibility
	Token           string    `json:"token"`            // Bearer token for authentication
	RegisteredAt    time.Time `json:"registered_at"`
	LastSeen        time.Time `json:"last_seen"`
	Status          string    `json:"status"` // active, inactive, offline

	// Additional metadata
	OSVersion       string    `json:"os_version,omitempty"`        // Detailed OS version
	GoVersion       string    `json:"go_version,omitempty"`        // Go runtime version
	Architecture    string    `json:"architecture,omitempty"`      // amd64, arm64, etc.
	NumCPU          int       `json:"num_cpu,omitempty"`           // Number of CPUs
	TotalMemoryMB   int64     `json:"total_memory_mb,omitempty"`   // Total system memory
	BuildType       string    `json:"build_type,omitempty"`        // dev, release
	GitCommit       string    `json:"git_commit,omitempty"`        // Git commit hash
	LastHeartbeat   time.Time `json:"last_heartbeat,omitempty"`    // Last heartbeat time
	DeviceCount     int       `json:"device_count,omitempty"`      // Number of devices managed
	LastDeviceSync  time.Time `json:"last_device_sync,omitempty"`  // Last device upload
	LastMetricsSync time.Time `json:"last_metrics_sync,omitempty"` // Last metrics upload
	TenantID        string    `json:"tenant_id,omitempty"`
	SiteIDs         []string  `json:"site_ids,omitempty"` // Sites this agent belongs to (can serve multiple sites)
}

// HeartbeatData contains fields that can be sent with an agent heartbeat.
// Used by both HTTP and WebSocket heartbeat handlers to build agent updates.
type HeartbeatData struct {
	Status          string
	Version         string
	ProtocolVersion string
	Hostname        string
	IP              string
	Platform        string
	OSVersion       string
	GoVersion       string
	Architecture    string
	BuildType       string
	GitCommit       string
	DeviceCount     int
}

// BuildAgentUpdate creates an Agent struct for UpdateAgentInfo if metadata fields are present,
// or returns nil if only a simple heartbeat (UpdateAgentHeartbeat) is needed.
// This centralizes the heartbeat logic used by both HTTP and WebSocket handlers.
func (h *HeartbeatData) BuildAgentUpdate(agentID string) *Agent {
	// Check if any metadata fields are present (beyond just status)
	hasMetadata := h.Version != "" || h.ProtocolVersion != "" || h.Hostname != "" ||
		h.IP != "" || h.Platform != "" || h.OSVersion != "" || h.GoVersion != "" ||
		h.Architecture != "" || h.BuildType != "" || h.GitCommit != ""

	if !hasMetadata {
		return nil
	}

	now := time.Now().UTC()
	status := h.Status
	if status == "" {
		status = "active"
	}

	return &Agent{
		AgentID:         agentID,
		Status:          status,
		Version:         h.Version,
		ProtocolVersion: h.ProtocolVersion,
		Hostname:        h.Hostname,
		IP:              h.IP,
		Platform:        h.Platform,
		OSVersion:       h.OSVersion,
		GoVersion:       h.GoVersion,
		Architecture:    h.Architecture,
		BuildType:       h.BuildType,
		GitCommit:       h.GitCommit,
		DeviceCount:     h.DeviceCount,
		LastSeen:        now,
		LastHeartbeat:   now,
	}
}

// PendingAgentRegistration captures an agent registration attempt using an expired token.
// Only stored when the token was once valid (known hash match) but has since expired.
// This allows admins to review and approve agents that tried to join with stale tokens.
type PendingAgentRegistration struct {
	ID              int64     `json:"id"`
	AgentID         string    `json:"agent_id"`              // UUID from the agent
	Name            string    `json:"name"`                  // Requested display name
	Hostname        string    `json:"hostname"`              // Agent hostname
	IP              string    `json:"ip"`                    // Remote IP address
	Platform        string    `json:"platform"`              // windows, linux, darwin
	AgentVersion    string    `json:"agent_version"`         // Agent version string
	ProtocolVersion string    `json:"protocol_version"`      // Protocol compatibility version
	ExpiredTokenID  string    `json:"expired_token_id"`      // The token that matched but was expired
	ExpiredTenantID string    `json:"expired_tenant_id"`     // Tenant the expired token belonged to
	Status          string    `json:"status"`                // pending, approved, rejected
	CreatedAt       time.Time `json:"created_at"`            // When the registration was attempted
	ReviewedAt      time.Time `json:"reviewed_at,omitempty"` // When admin reviewed
	ReviewedBy      string    `json:"reviewed_by,omitempty"` // Admin who reviewed
	Notes           string    `json:"notes,omitempty"`       // Admin notes
}

// PendingRegistrationStatus constants
const (
	PendingStatusPending  = "pending"
	PendingStatusApproved = "approved"
	PendingStatusRejected = "rejected"
)

// Device represents a printer device discovered by an agent (extends common Device)
type Device struct {
	commonstorage.Device // Embed common fields

	AgentID string `json:"agent_id"`           // Which agent discovered this (server-specific field)
	IsSaved *bool  `json:"is_saved,omitempty"` // nil for Agents that have not reported saved state
}

func (d *Device) loadSavedState() {
	if value, ok := d.RawData["is_saved"].(bool); ok {
		d.IsSaved = &value
	}
}

// DeviceWithMetrics extends Device with latest toner/consumable data for UI display
type DeviceWithMetrics struct {
	Device
	TonerLevels   map[string]interface{} `json:"toner_levels,omitempty"`
	PageCount     int                    `json:"page_count,omitempty"`
	ColorPages    int                    `json:"color_pages,omitempty"`
	MonoPages     int                    `json:"mono_pages,omitempty"`
	ScanCount     int                    `json:"scan_count,omitempty"`
	LastMetricsAt *time.Time             `json:"last_metrics_at,omitempty"`
}

// MetricsSnapshot represents a point-in-time snapshot of device metrics (base struct)
type MetricsSnapshot struct {
	ID          int64                  `json:"id"`
	Serial      string                 `json:"serial"`
	AgentID     string                 `json:"agent_id,omitempty"`
	Timestamp   time.Time              `json:"timestamp"`
	PageCount   int                    `json:"page_count,omitempty"`
	ColorPages  int                    `json:"color_pages,omitempty"`
	MonoPages   int                    `json:"mono_pages,omitempty"`
	ScanCount   int                    `json:"scan_count,omitempty"`
	FaxPages    int                    `json:"fax_pages,omitempty"`
	TonerLevels map[string]interface{} `json:"toner_levels,omitempty"`
	Tier        string                 `json:"tier,omitempty"` // raw, hourly, daily, monthly
}

// MetricSeriesPoint represents a single point in a derived fleet history series.
type MetricSeriesPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     int64     `json:"value"`
}

// FleetHistory holds synchronized series for Netdata-style charting.
type FleetHistory struct {
	TotalImpressions []MetricSeriesPoint `json:"total_impressions"`
	MonoImpressions  []MetricSeriesPoint `json:"mono_impressions"`
	ColorImpressions []MetricSeriesPoint `json:"color_impressions"`
	ScanVolume       []MetricSeriesPoint `json:"scan_volume"`
}

// FleetTotals summarizes fleet-wide counts for devices/agents and lifetime usage.
type FleetTotals struct {
	Agents     int   `json:"agents"`
	Devices    int   `json:"devices"`
	PageCount  int64 `json:"page_count"`
	ColorPages int64 `json:"color_pages"`
	MonoPages  int64 `json:"mono_pages"`
	ScanCount  int64 `json:"scan_count"`
}

// FleetStatuses captures current device health breakdowns.
type FleetStatuses struct {
	Error   int `json:"error"`
	Warning int `json:"warning"`
	Jam     int `json:"jam"`
}

// FleetConsumables buckets devices by toner/ink level severity.
type FleetConsumables struct {
	Critical int `json:"critical"`
	Low      int `json:"low"`
	Medium   int `json:"medium"`
	High     int `json:"high"`
	Unknown  int `json:"unknown"`
}

// FleetMetrics combines current totals, statuses, consumables, and history.
type FleetMetrics struct {
	Totals      FleetTotals      `json:"totals"`
	Statuses    FleetStatuses    `json:"statuses"`
	Consumables FleetConsumables `json:"consumables"`
	History     FleetHistory     `json:"history"`
}

// AggregatedMetrics describes the fleet view returned by the metrics endpoint.
type AggregatedMetrics struct {
	GeneratedAt time.Time    `json:"generated_at"`
	RangeStart  time.Time    `json:"range_start"`
	RangeEnd    time.Time    `json:"range_end"`
	Fleet       FleetMetrics `json:"fleet"`
	Server      ServerStats  `json:"server"`
}

// DatabaseStats summarizes high-level counts stored in SQLite.
type DatabaseStats struct {
	Agents           int64 `json:"agents"`
	Devices          int64 `json:"devices"`
	MetricsSnapshots int64 `json:"metrics_snapshots"`
	Sessions         int64 `json:"sessions"`
	Users            int64 `json:"users"`
	AuditEntries     int64 `json:"audit_entries"`
	ReleaseArtifacts int64 `json:"release_artifacts"`
	ReleaseBytes     int64 `json:"release_artifacts_bytes"`
	SizeBytes        int64 `json:"size_bytes"` // Database size (pg_database_size for PostgreSQL)
}

// MemoryStats reports selected runtime memory gauges.
type MemoryStats struct {
	HeapAlloc  uint64 `json:"heap_alloc_bytes"`
	HeapSys    uint64 `json:"heap_sys_bytes"`
	StackInUse uint64 `json:"stack_in_use_bytes"`
	StackSys   uint64 `json:"stack_sys_bytes"`
	TotalAlloc uint64 `json:"total_alloc_bytes"`
	Sys        uint64 `json:"sys_bytes"`
}

// RuntimeStats captures Go runtime health for the server.
type RuntimeStats struct {
	GoVersion    string      `json:"go_version"`
	NumCPU       int         `json:"num_cpu"`
	NumGoroutine int         `json:"num_goroutine"`
	StartTime    time.Time   `json:"start_time"`
	Memory       MemoryStats `json:"memory"`
}

// ServerStats surfaces process + DB stats for Netdata-like dashboards.
type ServerStats struct {
	Hostname      string         `json:"hostname"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Runtime       RuntimeStats   `json:"runtime"`
	Database      *DatabaseStats `json:"database,omitempty"`
}

// Tenant represents a customer/tenant stored in server DB
type Tenant struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	ContactName  string    `json:"contact_name,omitempty"`
	ContactEmail string    `json:"contact_email,omitempty"`
	ContactPhone string    `json:"contact_phone,omitempty"`
	BusinessUnit string    `json:"business_unit,omitempty"`
	BillingCode  string    `json:"billing_code,omitempty"`
	Address      string    `json:"address,omitempty"`
	LoginDomain  string    `json:"login_domain,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Site represents a location/grouping within a tenant for organizing agents and devices
type Site struct {
	ID          string           `json:"id"`
	TenantID    string           `json:"tenant_id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Address     string           `json:"address,omitempty"`
	FilterRules []SiteFilterRule `json:"filter_rules,omitempty"` // Rules for matching devices to this site
	CreatedAt   time.Time        `json:"created_at"`
	AgentCount  int              `json:"agent_count,omitempty"`  // Computed: number of agents assigned
	DeviceCount int              `json:"device_count,omitempty"` // Computed: number of devices matching
}

// SiteFilterRule defines a rule for matching devices to a site
// When an agent has multiple sites, devices are matched to sites via these rules
type SiteFilterRule struct {
	Type    string `json:"type"`    // "ip_range", "ip_prefix", "hostname_pattern", "serial_pattern"
	Pattern string `json:"pattern"` // The pattern to match (e.g., "192.168.1.0/24", "printer-*", "HP*")
}

// MatchesDevice checks if a device matches this filter rule
func (r SiteFilterRule) MatchesDevice(ip, hostname, serial string) bool {
	pattern := strings.TrimSpace(r.Pattern)
	if pattern == "" {
		return false
	}

	switch r.Type {
	case "ip_range":
		// CIDR notation: "192.168.1.0/24"
		_, network, err := net.ParseCIDR(pattern)
		if err != nil {
			return false
		}
		deviceIP := net.ParseIP(ip)
		if deviceIP == nil {
			return false
		}
		return network.Contains(deviceIP)

	case "ip_prefix":
		// Simple prefix: "192.168.1."
		return strings.HasPrefix(ip, pattern)

	case "hostname_pattern":
		// Glob-style pattern: "printer-*" or "*-floor2"
		return matchGlobPattern(pattern, hostname)

	case "serial_pattern":
		// Glob-style pattern for serial numbers
		return matchGlobPattern(pattern, serial)

	default:
		return false
	}
}

// matchGlobPattern matches a simple glob pattern with * wildcard
func matchGlobPattern(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)

	// Convert glob to regex: * becomes .*, escape other special chars
	regexPattern := "^"
	for _, c := range pattern {
		switch c {
		case '*':
			regexPattern += ".*"
		case '?':
			regexPattern += "."
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			regexPattern += "\\" + string(c)
		default:
			regexPattern += string(c)
		}
	}
	regexPattern += "$"

	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

// MatchesAnyRule checks if a device matches any of the site's filter rules
// Returns true if no rules defined (site matches all devices from its agents)
func (s *Site) MatchesDevice(ip, hostname, serial string) bool {
	if len(s.FilterRules) == 0 {
		return true // No rules = match all
	}
	for _, rule := range s.FilterRules {
		if rule.MatchesDevice(ip, hostname, serial) {
			return true
		}
	}
	return false
}

// JoinToken represents an opaque join token record stored in DB (token value not stored raw)
type JoinToken struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"token_hash"`
	TenantID  string    `json:"tenant_id"`
	ExpiresAt time.Time `json:"expires_at"`
	OneTime   bool      `json:"one_time"`
	CreatedAt time.Time `json:"created_at"`
	UsedAt    time.Time `json:"used_at,omitempty"`
	Revoked   bool      `json:"revoked"`
}

// User represents a local user account for the server UI/API
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	TenantID     string    `json:"tenant_id,omitempty"`
	TenantIDs    []string  `json:"tenant_ids,omitempty"`
	Email        string    `json:"email,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// UserInvitation represents a pending user invitation sent by email
type UserInvitation struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username,omitempty"` // Optional pre-set username
	Role      Role      `json:"role"`
	TenantID  string    `json:"tenant_id,omitempty"`
	TokenHash string    `json:"-"` // Hashed invite token
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"` // Username of inviter
}

// Session represents a short lived session/token for UI auth
type Session struct {
	Token     string    `json:"token"`
	UserID    int64     `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	Username  string    `json:"username,omitempty"`
}

// OIDCProvider represents a configured OIDC identity provider
type OIDCProvider struct {
	ID           int64     `json:"id"`
	Slug         string    `json:"slug"`
	DisplayName  string    `json:"display_name"`
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	Scopes       []string  `json:"scopes"`
	Icon         string    `json:"icon,omitempty"`
	ButtonText   string    `json:"button_text,omitempty"`
	ButtonStyle  string    `json:"button_style,omitempty"`
	AutoLogin    bool      `json:"auto_login"`
	TenantID     string    `json:"tenant_id,omitempty"`
	DefaultRole  Role      `json:"default_role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// OIDCSession represents a pending login attempt (state/nonce)
type OIDCSession struct {
	ID           string    `json:"id"`
	ProviderSlug string    `json:"provider_slug"`
	TenantID     string    `json:"tenant_id,omitempty"`
	Nonce        string    `json:"nonce"`
	State        string    `json:"state"`
	RedirectURL  string    `json:"redirect_url"`
	CreatedAt    time.Time `json:"created_at"`
}

// OIDCLink ties an OIDC subject to a local user
type OIDCLink struct {
	ID           int64     `json:"id"`
	ProviderSlug string    `json:"provider_slug"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email,omitempty"`
	UserID       int64     `json:"user_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// DeviceCredentials stores web UI credentials for proxy auto-login.
// Stored on server so agents remain stateless.
type DeviceCredentials struct {
	Serial            string    `json:"serial"`
	Username          string    `json:"username"`
	EncryptedPassword string    `json:"encrypted_password,omitempty"` // Encrypted, not returned to clients
	AuthType          string    `json:"auth_type"`                    // "basic" or "form"
	AutoLogin         bool      `json:"auto_login"`
	TenantID          string    `json:"tenant_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// AuditActorType enumerates the source of an audit event.
type AuditActorType string

const (
	AuditActorAgent  AuditActorType = "agent"
	AuditActorUser   AuditActorType = "user"
	AuditActorSystem AuditActorType = "system"
)

// AuditSeverity captures the importance of an audit event.
type AuditSeverity string

const (
	AuditSeverityInfo  AuditSeverity = "info"
	AuditSeverityWarn  AuditSeverity = "warn"
	AuditSeverityError AuditSeverity = "error"
)

// AuditEntry represents a structured audit log entry for any security-significant action.
type AuditEntry struct {
	ID         int64                  `json:"id"`
	Timestamp  time.Time              `json:"timestamp"`
	ActorType  AuditActorType         `json:"actor_type"`
	ActorID    string                 `json:"actor_id"`
	ActorName  string                 `json:"actor_name,omitempty"`
	Action     string                 `json:"action"`
	TargetType string                 `json:"target_type,omitempty"`
	TargetID   string                 `json:"target_id,omitempty"`
	TenantID   string                 `json:"tenant_id,omitempty"`
	Severity   AuditSeverity          `json:"severity"`
	Details    string                 `json:"details,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	IPAddress  string                 `json:"ip_address,omitempty"`
	UserAgent  string                 `json:"user_agent,omitempty"`
	RequestID  string                 `json:"request_id,omitempty"`
}

// Store defines the interface for server data storage
type Store interface {
	ReportScopedStore

	// Agent management
	RegisterAgent(ctx context.Context, agent *Agent) error
	GetAgent(ctx context.Context, agentID string) (*Agent, error)
	GetAgentByToken(ctx context.Context, token string) (*Agent, error)
	ListAgents(ctx context.Context) ([]*Agent, error)
	ListAgentsPaginated(ctx context.Context, limit, offset int, tenantIDs []string) ([]*Agent, error)
	CountAgents(ctx context.Context, tenantIDs []string) (int64, error)
	UpdateAgentHeartbeat(ctx context.Context, agentID string, status string) error
	// UpdateAgentInfo updates agent metadata (version, platform, etc.) typically on heartbeat
	UpdateAgentInfo(ctx context.Context, agent *Agent) error
	// UpdateAgentName updates the user-friendly name for an agent
	UpdateAgentName(ctx context.Context, agentID string, name string) error
	DeleteAgent(ctx context.Context, agentID string) error

	// Device management
	UpsertDevice(ctx context.Context, device *Device) error
	GetDevice(ctx context.Context, serial string) (*Device, error)
	ListDevices(ctx context.Context, agentID string) ([]*Device, error)
	ListAllDevices(ctx context.Context) ([]*Device, error)
	ListAllDevicesPaginated(ctx context.Context, limit, offset int, agentIDs []string) ([]*Device, error)
	CountDevices(ctx context.Context, agentIDs []string) (int64, error)
	// DeleteDevice removes a device by serial. If deleteMetrics is true, also deletes metrics history.
	DeleteDevice(ctx context.Context, serial string, deleteMetrics bool) error

	// Metrics
	SaveMetrics(ctx context.Context, metrics *MetricsSnapshot) error
	GetLatestMetrics(ctx context.Context, serial string) (*MetricsSnapshot, error)
	GetLatestMetricsBatch(ctx context.Context, serials []string) (map[string]*MetricsSnapshot, error)
	// GetMetricsAtOrBefore returns the latest snapshot with timestamp <= at, or nil if none.
	GetMetricsAtOrBefore(ctx context.Context, serial string, at time.Time) (*MetricsSnapshot, error)
	GetMetricsHistory(ctx context.Context, serial string, since time.Time) ([]*MetricsSnapshot, error)
	// GetMetricsBounds returns the min/max timestamps and total point count for a device's metrics.
	GetMetricsBounds(ctx context.Context, serial string) (minTS, maxTS time.Time, count int64, err error)
	GetAggregatedMetrics(ctx context.Context, since time.Time, tenantIDs []string) (*AggregatedMetrics, error)
	GetDatabaseStats(ctx context.Context) (*DatabaseStats, error)

	// Audit logging
	SaveAuditEntry(ctx context.Context, entry *AuditEntry) error
	GetAuditLog(ctx context.Context, actorID string, since time.Time) ([]*AuditEntry, error)
	GetAuditLogPaginated(ctx context.Context, actorID string, since time.Time, limit, offset int) ([]*AuditEntry, error)
	CountAuditLog(ctx context.Context, actorID string, since time.Time) (int64, error)

	// Utility
	Close() error
	Path() string // Returns the database path (SQLite) or empty string (PostgreSQL)

	// Tenancy
	CreateTenant(ctx context.Context, tenant *Tenant) error
	UpdateTenant(ctx context.Context, tenant *Tenant) error
	GetTenant(ctx context.Context, id string) (*Tenant, error)
	ListTenants(ctx context.Context) ([]*Tenant, error)
	FindTenantByDomain(ctx context.Context, domain string) (*Tenant, error)
	DeleteTenant(ctx context.Context, id string) error
	// CountTenantAgents returns number of agents assigned to a tenant (for delete safety checks)
	CountTenantAgents(ctx context.Context, tenantID string) (int64, error)

	// Sites (sub-tenant grouping for agents/devices)
	CreateSite(ctx context.Context, site *Site) error
	UpdateSite(ctx context.Context, site *Site) error
	GetSite(ctx context.Context, id string) (*Site, error)
	ListSitesByTenant(ctx context.Context, tenantID string) ([]*Site, error)
	DeleteSite(ctx context.Context, id string) error
	AssignAgentToSite(ctx context.Context, agentID, siteID string) error
	UnassignAgentFromSite(ctx context.Context, agentID, siteID string) error
	GetAgentSiteIDs(ctx context.Context, agentID string) ([]string, error)
	GetSiteAgentIDs(ctx context.Context, siteID string) ([]string, error)
	SetAgentSites(ctx context.Context, agentID string, siteIDs []string) error

	// Join tokens (opaque tokens): CreateJoinToken returns the created JoinToken
	// and the raw token string that should be returned to the caller (raw token
	// is not persisted in plain form).
	CreateJoinToken(ctx context.Context, tenantID string, ttlMinutes int, oneTime bool) (*JoinToken, string, error)
	CreateJoinTokenWithSecret(ctx context.Context, jt *JoinToken, rawSecret string) (*JoinToken, error)
	ValidateJoinToken(ctx context.Context, token string) (*JoinToken, error)

	// Admin: list and revoke join tokens
	ListJoinTokens(ctx context.Context, tenantID string) ([]*JoinToken, error)
	RevokeJoinToken(ctx context.Context, id string) error

	// Pending agent registrations (expired token capture)
	CreatePendingAgentRegistration(ctx context.Context, reg *PendingAgentRegistration) (int64, error)
	GetPendingAgentRegistration(ctx context.Context, id int64) (*PendingAgentRegistration, error)
	ListPendingAgentRegistrations(ctx context.Context, status string) ([]*PendingAgentRegistration, error)
	ApprovePendingRegistration(ctx context.Context, id int64, tenantID, reviewedBy string) error
	// ApprovePendingRegistrationWithToken commits review and token together.
	ApprovePendingRegistrationWithToken(ctx context.Context, id int64, tenantID, reviewedBy string) (*JoinToken, string, error)
	RejectPendingRegistration(ctx context.Context, id int64, reviewedBy, notes string) error
	DeletePendingAgentRegistration(ctx context.Context, id int64) error

	// User & session management (local login)
	CreateUser(ctx context.Context, user *User, rawPassword string) error
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	AuthenticateUser(ctx context.Context, username, rawPassword string) (*User, error)

	// Sessions
	CreateSession(ctx context.Context, userID int64, ttlMinutes int) (*Session, error)
	GetSessionByToken(ctx context.Context, token string) (*Session, error)
	DeleteSession(ctx context.Context, token string) error
	// ListSessions returns all sessions (admin) including username when available
	ListSessions(ctx context.Context) ([]*Session, error)
	// DeleteSessionByHash deletes a session by its stored token hash (DB-side key)
	DeleteSessionByHash(ctx context.Context, tokenHash string) error
	// DeleteSessionByHashWithCount deletes a session by hash and returns the number of rows affected
	DeleteSessionByHashWithCount(ctx context.Context, tokenHash string) (int64, error)
	// DeleteExpiredSessions removes all expired sessions and returns the count deleted
	DeleteExpiredSessions(ctx context.Context) (int64, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)
	// ListUsers returns all local users (admin UI)
	ListUsers(ctx context.Context) ([]*User, error)
	DeleteUser(ctx context.Context, id int64) error
	UpdateUser(ctx context.Context, user *User) error
	GetUserByEmail(ctx context.Context, email string) (*User, error)

	// Password reset tokens (opaque token stored hashed)
	CreatePasswordResetToken(ctx context.Context, userID int64, ttlMinutes int) (string, error)
	ValidatePasswordResetToken(ctx context.Context, token string) (int64, error)
	DeletePasswordResetToken(ctx context.Context, token string) error
	UpdateUserPassword(ctx context.Context, userID int64, rawPassword string) error

	// User invitations (email-based signup)
	CreateUserInvitation(ctx context.Context, inv *UserInvitation, ttlMinutes int) (string, error)
	GetUserInvitation(ctx context.Context, token string) (*UserInvitation, error)
	MarkInvitationUsed(ctx context.Context, id int64) error
	ListUserInvitations(ctx context.Context) ([]*UserInvitation, error)
	DeleteUserInvitation(ctx context.Context, id int64) error

	// OIDC / SSO
	CreateOIDCProvider(ctx context.Context, provider *OIDCProvider) error
	UpdateOIDCProvider(ctx context.Context, provider *OIDCProvider) error
	DeleteOIDCProvider(ctx context.Context, slug string) error
	GetOIDCProvider(ctx context.Context, slug string) (*OIDCProvider, error)
	ListOIDCProviders(ctx context.Context, tenantID string) ([]*OIDCProvider, error)
	CreateOIDCSession(ctx context.Context, sess *OIDCSession) error
	GetOIDCSession(ctx context.Context, id string) (*OIDCSession, error)
	DeleteOIDCSession(ctx context.Context, id string) error
	CreateOIDCLink(ctx context.Context, link *OIDCLink) error
	GetOIDCLink(ctx context.Context, providerSlug, subject string) (*OIDCLink, error)
	ListOIDCLinksForUser(ctx context.Context, userID int64) ([]*OIDCLink, error)
	DeleteOIDCLink(ctx context.Context, providerSlug, subject string) error

	// Settings management
	GetGlobalSettings(ctx context.Context) (*SettingsRecord, error)
	UpsertGlobalSettings(ctx context.Context, rec *SettingsRecord) error
	GetTenantSettings(ctx context.Context, tenantID string) (*TenantSettingsRecord, error)
	UpsertTenantSettings(ctx context.Context, rec *TenantSettingsRecord) error
	DeleteTenantSettings(ctx context.Context, tenantID string) error
	ListTenantSettings(ctx context.Context) ([]*TenantSettingsRecord, error)
	GetAgentSettings(ctx context.Context, agentID string) (*AgentSettingsRecord, error)
	UpsertAgentSettings(ctx context.Context, rec *AgentSettingsRecord) error
	DeleteAgentSettings(ctx context.Context, agentID string) error

	// Admin notification settings (tenant level; fleet level lives in config.toml)
	GetTenantNotificationSettings(ctx context.Context, tenantID string) (*NotificationSettings, error)
	UpsertTenantNotificationSettings(ctx context.Context, rec *NotificationSettings) error
	DeleteTenantNotificationSettings(ctx context.Context, tenantID string) error
	ListTenantNotificationSettings(ctx context.Context) ([]*NotificationSettings, error)

	// Fleet update policy management
	GetFleetUpdatePolicy(ctx context.Context, tenantID string) (*FleetUpdatePolicy, error)
	UpsertFleetUpdatePolicy(ctx context.Context, policy *FleetUpdatePolicy) error
	DeleteFleetUpdatePolicy(ctx context.Context, tenantID string) error
	ListFleetUpdatePolicies(ctx context.Context) ([]*FleetUpdatePolicy, error)

	// Release intake & caching
	UpsertReleaseArtifact(ctx context.Context, artifact *ReleaseArtifact) error
	// GetReleaseArtifact looks up one artifact; an empty format means binary.
	GetReleaseArtifact(ctx context.Context, component, version, platform, arch, format string) (*ReleaseArtifact, error)
	ListReleaseArtifacts(ctx context.Context, component string, limit int) ([]*ReleaseArtifact, error)
	DeleteReleaseArtifact(ctx context.Context, id int64) error
	// ListVersionsForPruning returns distinct versions per component ordered by publish date (oldest first)
	// limited to versions beyond the keepVersions threshold. Returns artifacts to be pruned.
	ListArtifactsForPruning(ctx context.Context, component string, keepVersions int) ([]*ReleaseArtifact, error)

	// Release manifest signing
	CreateSigningKey(ctx context.Context, key *SigningKey) error
	GetSigningKey(ctx context.Context, id string) (*SigningKey, error)
	GetActiveSigningKey(ctx context.Context) (*SigningKey, error)
	ListSigningKeys(ctx context.Context, limit int) ([]*SigningKey, error)
	SetSigningKeyActive(ctx context.Context, id string) error
	UpsertReleaseManifest(ctx context.Context, manifest *ReleaseManifest) error
	// GetReleaseManifest looks up one signed manifest; an empty format means binary.
	GetReleaseManifest(ctx context.Context, component, version, platform, arch, format string) (*ReleaseManifest, error)
	ListReleaseManifests(ctx context.Context, component string, limit int) ([]*ReleaseManifest, error)

	// Self-update tracking
	CreateSelfUpdateRun(ctx context.Context, run *SelfUpdateRun) error
	UpdateSelfUpdateRun(ctx context.Context, run *SelfUpdateRun) error
	GetSelfUpdateRun(ctx context.Context, id int64) (*SelfUpdateRun, error)
	ListSelfUpdateRuns(ctx context.Context, limit int) ([]*SelfUpdateRun, error)

	// Alert management
	CreateAlert(ctx context.Context, alert *Alert) (int64, error)
	GetAlert(ctx context.Context, id int64) (*Alert, error)
	ListActiveAlerts(ctx context.Context, filters AlertFilters) ([]Alert, error)
	UpdateAlertStatus(ctx context.Context, id int64, status AlertStatus) error
	AcknowledgeAlert(ctx context.Context, id int64, username string) error
	ResolveAlert(ctx context.Context, id int64) error
	UpdateAlertNotificationStatus(ctx context.Context, id int64, sent int, lastNotified time.Time) error

	// Alert rule management
	CreateAlertRule(ctx context.Context, rule *AlertRule) (int64, error)
	GetAlertRule(ctx context.Context, id int64) (*AlertRule, error)
	ListAlertRules(ctx context.Context) ([]AlertRule, error)
	UpdateAlertRule(ctx context.Context, rule *AlertRule) error
	DeleteAlertRule(ctx context.Context, id int64) error

	// Notification channel management
	CreateNotificationChannel(ctx context.Context, channel *NotificationChannel) (int64, error)
	GetNotificationChannel(ctx context.Context, id int64) (*NotificationChannel, error)
	ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error)
	UpdateNotificationChannel(ctx context.Context, channel *NotificationChannel) error
	DeleteNotificationChannel(ctx context.Context, id int64) error

	// Escalation policy management
	CreateEscalationPolicy(ctx context.Context, policy *EscalationPolicy) (int64, error)
	GetEscalationPolicy(ctx context.Context, id int64) (*EscalationPolicy, error)
	ListEscalationPolicies(ctx context.Context) ([]EscalationPolicy, error)
	UpdateEscalationPolicy(ctx context.Context, policy *EscalationPolicy) error
	DeleteEscalationPolicy(ctx context.Context, id int64) error

	// Maintenance window management
	CreateAlertMaintenanceWindow(ctx context.Context, window *AlertMaintenanceWindow) (int64, error)
	GetAlertMaintenanceWindow(ctx context.Context, id int64) (*AlertMaintenanceWindow, error)
	ListAlertMaintenanceWindows(ctx context.Context) ([]AlertMaintenanceWindow, error)
	UpdateAlertMaintenanceWindow(ctx context.Context, window *AlertMaintenanceWindow) error
	GetActiveAlertMaintenanceWindows(ctx context.Context) ([]AlertMaintenanceWindow, error)
	DeleteAlertMaintenanceWindow(ctx context.Context, id int64) error

	// Alert settings
	GetAlertSettings(ctx context.Context) (*AlertSettings, error)
	SaveAlertSettings(ctx context.Context, settings *AlertSettings) error

	// Alert summary
	GetAlertSummary(ctx context.Context) (*AlertSummary, error)

	// Alert filters (for reports) - uses AlertFilters from alerts.go
	ListAlerts(ctx context.Context, filter AlertFilters) ([]*Alert, error)

	// Report management
	CreateReport(ctx context.Context, report *ReportDefinition) error
	UpdateReport(ctx context.Context, report *ReportDefinition) error
	GetReport(ctx context.Context, id int64) (*ReportDefinition, error)
	DeleteReport(ctx context.Context, id int64) error
	ListReports(ctx context.Context, filter ReportFilter) ([]*ReportDefinition, error)

	// Report schedules
	CreateReportSchedule(ctx context.Context, schedule *ReportSchedule) error
	UpdateReportSchedule(ctx context.Context, schedule *ReportSchedule) error
	GetReportSchedule(ctx context.Context, id int64) (*ReportSchedule, error)
	DeleteReportSchedule(ctx context.Context, id int64) error
	ListReportSchedules(ctx context.Context, reportID int64) ([]*ReportSchedule, error)
	GetDueSchedules(ctx context.Context, before time.Time) ([]*ReportSchedule, error)
	UpdateScheduleAfterRun(ctx context.Context, scheduleID int64, runID int64, nextRun time.Time, failed bool) error

	// Report runs
	CreateReportRun(ctx context.Context, run *ReportRun) error
	UpdateReportRun(ctx context.Context, run *ReportRun) error
	GetReportRun(ctx context.Context, id int64) (*ReportRun, error)
	GetReportRunMetadata(ctx context.Context, id int64) (*ReportRun, error)
	DeleteReportRun(ctx context.Context, id int64) error
	ListReportRuns(ctx context.Context, filter ReportRunFilter) ([]*ReportRun, error)
	GetReportSummary(ctx context.Context) (*ReportSummary, error)
	CleanupOldReportRuns(ctx context.Context, olderThan time.Time) (int64, error)

	// Server metrics time-series (Netdata-style)
	InsertServerMetrics(ctx context.Context, snapshot *ServerMetricsSnapshot) error
	GetServerMetrics(ctx context.Context, query ServerMetricsQuery) (*ServerMetricsTimeSeries, error)
	GetLatestServerMetrics(ctx context.Context) (*ServerMetricsSnapshot, error)
	AggregateServerMetrics(ctx context.Context) error
	PruneServerMetrics(ctx context.Context) error

	// Device credentials for proxy auto-login (stored on server, agents are stateless)
	GetDeviceCredentials(ctx context.Context, serial string) (*DeviceCredentials, error)
	UpsertDeviceCredentials(ctx context.Context, creds *DeviceCredentials) error
	DeleteDeviceCredentials(ctx context.Context, serial string) error
}

// SettingsRecord captures the canonical global settings payload persisted by the server.
type SettingsRecord struct {
	SchemaVersion   string              `json:"schema_version"`
	Settings        pmsettings.Settings `json:"settings"`
	ManagedSections []string            `json:"managed_sections,omitempty"` // e.g. ["discovery", "snmp", "features"]
	UpdatedAt       time.Time           `json:"updated_at"`
	UpdatedBy       string              `json:"updated_by,omitempty"`
}

// TenantSettingsRecord stores tenant-specific override patches (partial payloads).
type TenantSettingsRecord struct {
	TenantID         string                 `json:"tenant_id"`
	SchemaVersion    string                 `json:"schema_version"`
	Overrides        map[string]interface{} `json:"overrides"`
	EnforcedSections []string               `json:"enforced_sections,omitempty"` // Sections that cannot be overridden per-agent
	UpdatedAt        time.Time              `json:"updated_at"`
	UpdatedBy        string                 `json:"updated_by,omitempty"`
}

// AgentSettingsRecord stores agent-specific override patches (partial payloads).
// Overrides are applied after tenant settings, subject to global managed sections and tenant enforcement.
type AgentSettingsRecord struct {
	AgentID       string                 `json:"agent_id"`
	SchemaVersion string                 `json:"schema_version"`
	Overrides     map[string]interface{} `json:"overrides"`
	UpdatedAt     time.Time              `json:"updated_at"`
	UpdatedBy     string                 `json:"updated_by,omitempty"`
}

// ReleaseArtifact captures cached release metadata and on-disk artifact state.
// Identity is component + version + platform + arch + format; see
// updatepolicy.ArtifactFormat for why the format is part of the key.
type ReleaseArtifact struct {
	ID           int64     `json:"id"`
	Component    string    `json:"component"`
	Version      string    `json:"version"`
	Platform     string    `json:"platform"`
	Arch         string    `json:"arch"`
	Format       string    `json:"format"`
	Channel      string    `json:"channel"`
	SourceURL    string    `json:"source_url"`
	CachePath    string    `json:"cache_path"`
	SHA256       string    `json:"sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	ReleaseNotes string    `json:"release_notes"`
	PublishedAt  time.Time `json:"published_at"`
	DownloadedAt time.Time `json:"downloaded_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SigningKey captures an Ed25519 key pair used for manifest signing.
type SigningKey struct {
	ID         string    `json:"id"`
	Algorithm  string    `json:"algorithm"`
	PublicKey  string    `json:"public_key"`
	PrivateKey string    `json:"-"`
	Notes      string    `json:"notes,omitempty"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
	RotatedAt  time.Time `json:"rotated_at,omitempty"`
}

// ReleaseManifest represents the signed manifest payload for a cached artifact.
// It shares the artifact identity, including Format.
type ReleaseManifest struct {
	ID              int64     `json:"id"`
	Component       string    `json:"component"`
	Version         string    `json:"version"`
	Platform        string    `json:"platform"`
	Arch            string    `json:"arch"`
	Format          string    `json:"format"`
	Channel         string    `json:"channel"`
	ManifestVersion string    `json:"manifest_version"`
	ManifestJSON    string    `json:"manifest_json"`
	Signature       string    `json:"signature"`
	SigningKeyID    string    `json:"signing_key_id"`
	GeneratedAt     time.Time `json:"generated_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SelfUpdateStatus enumerates lifecycle stages for server self-update attempts.
type SelfUpdateStatus string

const (
	SelfUpdateStatusPending     SelfUpdateStatus = "pending"
	SelfUpdateStatusChecking    SelfUpdateStatus = "checking"
	SelfUpdateStatusDownloading SelfUpdateStatus = "downloading"
	SelfUpdateStatusStaging     SelfUpdateStatus = "staging"
	SelfUpdateStatusApplying    SelfUpdateStatus = "applying"
	SelfUpdateStatusSucceeded   SelfUpdateStatus = "succeeded"
	SelfUpdateStatusFailed      SelfUpdateStatus = "failed"
	SelfUpdateStatusSkipped     SelfUpdateStatus = "skipped"
)

// SelfUpdateRun tracks each self-update attempt or evaluation performed by the server.
type SelfUpdateRun struct {
	ID                int64            `json:"id"`
	Status            SelfUpdateStatus `json:"status"`
	RequestedAt       time.Time        `json:"requested_at"`
	StartedAt         time.Time        `json:"started_at"`
	CompletedAt       time.Time        `json:"completed_at"`
	CurrentVersion    string           `json:"current_version"`
	TargetVersion     string           `json:"target_version"`
	Channel           string           `json:"channel"`
	Platform          string           `json:"platform"`
	Arch              string           `json:"arch"`
	ReleaseArtifactID int64            `json:"release_artifact_id"`
	ErrorCode         string           `json:"error_code"`
	ErrorMessage      string           `json:"error_message"`
	Metadata          map[string]any   `json:"metadata"`
	RequestedBy       string           `json:"requested_by"`
}
