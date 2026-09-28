// Package adminnotify raises latched, operator-facing alerts about the server and
// fleet (expiring credentials, failing background jobs, daily summaries) and delivers
// them to administrators at two levels: fleet (server) admins and tenant admins.
package adminnotify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"printmaster/server/storage"
)

// SystemAlertTypePrefix namespaces server-health alerts inside the shared alerts
// table so they never collide with device/agent alert types.
const SystemAlertTypePrefix = "system:"

// Audience selects which admin levels are notified about an event.
type Audience string

const (
	// AudienceFleet notifies server administrators only. Use for conditions that
	// expose server internals (database failures, TLS, self-update).
	AudienceFleet Audience = "fleet"
	// AudienceTenant notifies the tenant's administrators only.
	AudienceTenant Audience = "tenant"
	// AudienceAll notifies both fleet and tenant administrators.
	AudienceAll Audience = "all"
)

// Config is one level's admin notification configuration. The fleet level is
// supplied by server config.toml; tenant levels come from the database.
type Config struct {
	Enabled              bool
	Recipients           []string
	NotifyOnCritical     bool
	NotifyOnWarning      bool
	DailySummaryEnabled  bool
	DailySummaryTime     string // HH:MM
	DailySummaryTimezone string // IANA name or "Local"
}

// Store is the subset of server storage required by the notifier.
type Store interface {
	CreateAlert(ctx context.Context, alert *storage.Alert) (int64, error)
	ListActiveAlerts(ctx context.Context, filters storage.AlertFilters) ([]storage.Alert, error)
	ResolveAlert(ctx context.Context, id int64) error
	UpdateAlertNotificationStatus(ctx context.Context, id int64, sent int, lastNotified time.Time) error
	GetTenantNotificationSettings(ctx context.Context, tenantID string) (*storage.NotificationSettings, error)
	ListTenantNotificationSettings(ctx context.Context) ([]*storage.NotificationSettings, error)
	UpsertTenantNotificationSettings(ctx context.Context, rec *storage.NotificationSettings) error
	DeleteTenantNotificationSettings(ctx context.Context, tenantID string) error
	ListTenants(ctx context.Context) ([]*storage.Tenant, error)
}

// Mailer delivers one message to one recipient.
type Mailer func(to, subject, htmlBody, textBody string) error

// Options configures a Notifier.
type Options struct {
	Store Store
	// Mailer delivers messages; when nil nothing is emailed but alerts are still latched.
	Mailer Mailer
	// FleetConfig returns the current server-level configuration. It is called on
	// every dispatch so settings changes apply without a restart.
	FleetConfig func() Config
	ThemeFn     func() string
	ServerURL   func() string
	Logger      *slog.Logger
	Now         func() time.Time
}

// Event describes a condition worth telling administrators about.
type Event struct {
	// Key is a stable identifier for the condition (e.g. "oidc.client_secret.entra").
	// Repeat raises for the same key are latched until Clear is called, so a
	// continuously failing condition produces exactly one notification.
	Key      string
	Severity string
	Title    string
	Message  string
	Details  string
	// TenantID scopes the event. Empty means server-wide.
	TenantID string
	// Audience defaults to AudienceFleet when TenantID is empty, AudienceAll otherwise.
	Audience Audience
}

// Notifier raises latched alerts and emails fleet and tenant administrators.
type Notifier struct {
	store       Store
	mailer      Mailer
	fleetConfig func() Config
	themeFn     func() string
	serverURL   func() string
	logger      *slog.Logger
	now         func() time.Time

	mu sync.Mutex

	summaryMu   sync.Mutex
	summaryWG   sync.WaitGroup
	stopCh      chan struct{}
	running     bool
	lastSummary map[string]string // level key -> YYYY-MM-DD already delivered
}

// New builds a Notifier. A nil *Notifier is safe to use: every method is a no-op,
// so callers can wire it unconditionally.
func New(opts Options) *Notifier {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	themeFn := opts.ThemeFn
	if themeFn == nil {
		themeFn = func() string { return "auto" }
	}
	serverURL := opts.ServerURL
	if serverURL == nil {
		serverURL = func() string { return "" }
	}
	fleetConfig := opts.FleetConfig
	if fleetConfig == nil {
		fleetConfig = func() Config { return Config{} }
	}
	return &Notifier{
		store:       opts.Store,
		mailer:      opts.Mailer,
		fleetConfig: fleetConfig,
		themeFn:     themeFn,
		serverURL:   serverURL,
		logger:      logger,
		now:         now,
		lastSummary: make(map[string]string),
	}
}

// AlertType returns the alerts-table type string used for a notification key.
func AlertType(key string) string {
	return SystemAlertTypePrefix + strings.TrimSpace(key)
}

// Raise records a latched alert for the event and notifies the matching admin
// levels. If an unresolved alert already exists for the same key and tenant,
// nothing is created and no email is sent. It reports whether a new alert was raised.
func (n *Notifier) Raise(ctx context.Context, ev Event) (bool, error) {
	if n == nil || n.store == nil {
		return false, nil
	}
	key := strings.TrimSpace(ev.Key)
	if key == "" {
		return false, fmt.Errorf("adminnotify: event key is required")
	}
	severity := normalizeSeverity(ev.Severity)
	alertType := AlertType(key)
	tenantID := strings.TrimSpace(ev.TenantID)

	// Serialize latch check + create so concurrent raises cannot both fire.
	n.mu.Lock()
	defer n.mu.Unlock()

	existing, err := n.findLatched(ctx, alertType, tenantID)
	if err != nil {
		return false, err
	}
	if existing != nil {
		n.logger.Debug("system alert already latched", "key", key, "tenant_id", tenantID, "alert_id", existing.ID)
		return false, nil
	}

	now := n.now().UTC()
	scope := storage.AlertScopeFleet
	if tenantID != "" {
		scope = storage.AlertScopeTenant
	}
	alert := &storage.Alert{
		Type:        alertType,
		Severity:    severity,
		Scope:       scope,
		Status:      storage.AlertStatusActive,
		TenantID:    tenantID,
		Title:       ev.Title,
		Message:     ev.Message,
		Details:     ev.Details,
		TriggeredAt: now,
	}
	id, err := n.store.CreateAlert(ctx, alert)
	if err != nil {
		return false, fmt.Errorf("adminnotify: create alert: %w", err)
	}
	n.logger.Warn("system alert raised", "key", key, "severity", severity, "tenant_id", tenantID, "alert_id", id, "title", ev.Title)

	if sent := n.deliverAlert(ctx, alert, resolveAudience(ev)); sent > 0 {
		if err := n.store.UpdateAlertNotificationStatus(ctx, id, sent, now); err != nil {
			n.logger.Warn("failed to record admin notification", "key", key, "error", err)
		}
	}
	return true, nil
}

// Clear resolves the latched alert for the key at the given tenant scope so a
// future occurrence notifies again. Pass an empty tenantID for server-wide events.
func (n *Notifier) Clear(ctx context.Context, key, tenantID string) error {
	if n == nil || n.store == nil {
		return nil
	}
	alertType := AlertType(key)
	tenantID = strings.TrimSpace(tenantID)

	n.mu.Lock()
	defer n.mu.Unlock()

	existing, err := n.store.ListActiveAlerts(ctx, storage.AlertFilters{Type: alertType})
	if err != nil {
		return fmt.Errorf("adminnotify: list alerts for clear: %w", err)
	}
	for i := range existing {
		if strings.TrimSpace(existing[i].TenantID) != tenantID {
			continue
		}
		if err := n.store.ResolveAlert(ctx, existing[i].ID); err != nil {
			return fmt.Errorf("adminnotify: resolve alert %d: %w", existing[i].ID, err)
		}
		n.logger.Info("system alert cleared", "key", key, "tenant_id", tenantID, "alert_id", existing[i].ID)
	}
	return nil
}

// findLatched returns an unresolved alert for the type/tenant pair, if any.
func (n *Notifier) findLatched(ctx context.Context, alertType, tenantID string) (*storage.Alert, error) {
	existing, err := n.store.ListActiveAlerts(ctx, storage.AlertFilters{Type: alertType})
	if err != nil {
		return nil, fmt.Errorf("adminnotify: check existing alert: %w", err)
	}
	for i := range existing {
		if strings.TrimSpace(existing[i].TenantID) == tenantID {
			return &existing[i], nil
		}
	}
	return nil, nil
}

// deliverAlert emails the alert to every applicable admin level and returns the
// number of successful deliveries.
func (n *Notifier) deliverAlert(ctx context.Context, alert *storage.Alert, audience Audience) int {
	if n.mailer == nil {
		return 0
	}
	recipients := n.resolveRecipients(ctx, alert.TenantID, audience, func(cfg Config) bool {
		return severityAllowed(cfg, alert.Severity)
	})
	if len(recipients) == 0 {
		return 0
	}

	subject := fmt.Sprintf("[PrintMaster %s] %s", strings.ToUpper(alert.Severity), alert.Title)
	htmlBody, textBody := renderAlertEmail(n.themeFn(), n.serverURL(), alert)
	return n.send(recipients, subject, htmlBody, textBody, alert.Type)
}

// resolveRecipients collects de-duplicated addresses from every admin level in the
// audience whose configuration passes the supplied gate.
func (n *Notifier) resolveRecipients(ctx context.Context, tenantID string, audience Audience, gate func(Config) bool) []string {
	seen := make(map[string]bool)
	var out []string

	add := func(cfg Config) {
		if !cfg.Enabled || !gate(cfg) {
			return
		}
		for _, addr := range normalizeRecipients(cfg.Recipients) {
			lower := strings.ToLower(addr)
			if seen[lower] {
				continue
			}
			seen[lower] = true
			out = append(out, addr)
		}
	}

	if audience == AudienceFleet || audience == AudienceAll {
		add(n.fleetConfig())
	}
	if tenantID != "" && (audience == AudienceTenant || audience == AudienceAll) {
		cfg, err := n.TenantConfig(ctx, tenantID)
		if err != nil {
			n.logger.Warn("failed to load tenant notification settings", "tenant_id", tenantID, "error", err)
		} else if cfg != nil {
			add(*cfg)
		}
	}
	return out
}

// TenantConfig loads a tenant's notification configuration, or nil when unset.
func (n *Notifier) TenantConfig(ctx context.Context, tenantID string) (*Config, error) {
	if n == nil || n.store == nil {
		return nil, nil
	}
	rec, err := n.store.GetTenantNotificationSettings(ctx, tenantID)
	if err != nil || rec == nil {
		return nil, err
	}
	cfg := configFromRecord(rec)
	return &cfg, nil
}

func (n *Notifier) send(recipients []string, subject, htmlBody, textBody, context string) int {
	sent := 0
	for _, to := range recipients {
		if err := n.mailer(to, subject, htmlBody, textBody); err != nil {
			n.logger.Error("failed to email admin notification", "to", to, "context", context, "error", err)
			continue
		}
		sent++
	}
	return sent
}

// SendTest emails a sample notification to one level's recipients without latching
// or severity filtering, so administrators can verify SMTP and address setup.
// An empty tenantID targets the fleet level.
func (n *Notifier) SendTest(ctx context.Context, tenantID string) (int, error) {
	if n == nil || n.store == nil {
		return 0, fmt.Errorf("admin notifications are not available")
	}
	if n.mailer == nil {
		return 0, fmt.Errorf("no mailer configured; check SMTP settings")
	}
	tenantID = strings.TrimSpace(tenantID)
	audience := AudienceFleet
	if tenantID != "" {
		audience = AudienceTenant
	}
	recipients := n.resolveRecipients(ctx, tenantID, audience, func(Config) bool { return true })
	if len(recipients) == 0 {
		return 0, fmt.Errorf("no enabled recipients configured for this level")
	}

	alert := &storage.Alert{
		Type:        AlertType("test"),
		Severity:    storage.AlertSeverityInfo,
		Scope:       storage.AlertScopeFleet,
		Status:      storage.AlertStatusActive,
		TenantID:    tenantID,
		Title:       "PrintMaster test notification",
		Message:     "This is a test of PrintMaster administrator notifications. No action is required.",
		TriggeredAt: n.now().UTC(),
	}
	htmlBody, textBody := renderAlertEmail(n.themeFn(), n.serverURL(), alert)
	return n.send(recipients, "[PrintMaster] Test notification", htmlBody, textBody, alert.Type), nil
}

func configFromRecord(rec *storage.NotificationSettings) Config {
	return Config{
		Enabled:              rec.Enabled,
		Recipients:           rec.Recipients,
		NotifyOnCritical:     rec.NotifyOnCritical,
		NotifyOnWarning:      rec.NotifyOnWarning,
		DailySummaryEnabled:  rec.DailySummaryEnabled,
		DailySummaryTime:     rec.DailySummaryTime,
		DailySummaryTimezone: rec.DailySummaryTimezone,
	}
}

func resolveAudience(ev Event) Audience {
	switch ev.Audience {
	case AudienceFleet, AudienceTenant, AudienceAll:
		return ev.Audience
	}
	if strings.TrimSpace(ev.TenantID) == "" {
		return AudienceFleet
	}
	return AudienceAll
}

func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case storage.AlertSeverityCritical:
		return storage.AlertSeverityCritical
	case storage.AlertSeverityWarning:
		return storage.AlertSeverityWarning
	default:
		return storage.AlertSeverityInfo
	}
}

func severityAllowed(cfg Config, severity string) bool {
	switch severity {
	case storage.AlertSeverityCritical:
		return cfg.NotifyOnCritical
	case storage.AlertSeverityWarning:
		return cfg.NotifyOnWarning
	default:
		return false
	}
}

func normalizeRecipients(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, raw := range list {
		addr := strings.TrimSpace(raw)
		// Reject header-injection characters before the address reaches the mailer.
		if addr == "" || !strings.Contains(addr, "@") || strings.ContainsAny(addr, "\r\n\x00,;") {
			continue
		}
		lower := strings.ToLower(addr)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, addr)
	}
	return out
}
