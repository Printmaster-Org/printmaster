package adminnotify

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"printmaster/server/storage"
)

// summaryCheckInterval controls how often the worker looks for due summaries.
const summaryCheckInterval = time.Minute

// Summary is the payload rendered into a daily digest email.
type Summary struct {
	Level      string // "fleet" or "tenant"
	TenantID   string
	TenantName string
	Date       string
	Critical   int
	Warning    int
	Info       int
	Alerts     []storage.Alert
}

// Total returns the number of unresolved alerts in the summary.
func (s Summary) Total() int { return s.Critical + s.Warning + s.Info }

// StartDailySummaries begins the background worker that delivers daily digests to
// every enabled admin level. It is a no-op on a nil Notifier or if already running.
func (n *Notifier) StartDailySummaries() {
	if n == nil || n.store == nil {
		return
	}
	n.summaryMu.Lock()
	if n.running {
		n.summaryMu.Unlock()
		return
	}
	n.running = true
	n.stopCh = make(chan struct{})
	stop := n.stopCh
	n.summaryMu.Unlock()

	n.summaryWG.Add(1)
	go func() {
		defer n.summaryWG.Done()
		ticker := time.NewTicker(summaryCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				n.DispatchDueSummaries(ctx)
				cancel()
			}
		}
	}()
	n.logger.Info("admin notification summary worker started", "interval", summaryCheckInterval.String())
}

// StopDailySummaries stops the daily digest worker.
func (n *Notifier) StopDailySummaries() {
	if n == nil {
		return
	}
	n.summaryMu.Lock()
	if !n.running {
		n.summaryMu.Unlock()
		return
	}
	n.running = false
	close(n.stopCh)
	n.summaryMu.Unlock()

	n.summaryWG.Wait()
	n.logger.Info("admin notification summary worker stopped")
}

// DispatchDueSummaries sends any daily summary whose scheduled time has passed today.
func (n *Notifier) DispatchDueSummaries(ctx context.Context) {
	if n == nil || n.store == nil {
		return
	}
	levels, err := n.summaryLevels(ctx)
	if err != nil {
		n.logger.Error("failed to resolve admin notification levels", "error", err)
		return
	}
	for _, lvl := range levels {
		if !lvl.cfg.Enabled || !lvl.cfg.DailySummaryEnabled {
			continue
		}
		due, day := summaryDue(n.now(), lvl.cfg)
		if !due {
			continue
		}
		if n.alreadySent(lvl.key(), day) {
			continue
		}
		if err := n.sendSummary(ctx, lvl); err != nil {
			n.logger.Error("failed to send admin daily summary", "level", lvl.key(), "error", err)
			continue
		}
		n.markSent(lvl.key(), day)
	}
}

// level pairs an admin audience with its resolved configuration.
type level struct {
	tenantID   string
	tenantName string
	cfg        Config
}

func (l level) key() string {
	if l.tenantID == "" {
		return "fleet"
	}
	return "tenant:" + l.tenantID
}

func (n *Notifier) summaryLevels(ctx context.Context) ([]level, error) {
	levels := []level{{cfg: n.fleetConfig()}}

	records, err := n.store.ListTenantNotificationSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenant notification settings: %w", err)
	}
	if len(records) == 0 {
		return levels, nil
	}

	names := make(map[string]string)
	if tenants, err := n.store.ListTenants(ctx); err == nil {
		for _, t := range tenants {
			if t != nil {
				names[t.ID] = t.Name
			}
		}
	}
	for _, rec := range records {
		if rec == nil {
			continue
		}
		levels = append(levels, level{
			tenantID:   rec.TenantID,
			tenantName: names[rec.TenantID],
			cfg:        configFromRecord(rec),
		})
	}
	return levels, nil
}

// sendSummary builds and emails the digest for a single admin level.
func (n *Notifier) sendSummary(ctx context.Context, lvl level) error {
	summary, err := n.buildSummary(ctx, lvl)
	if err != nil {
		return err
	}
	if n.mailer == nil {
		return nil
	}
	recipients := n.resolveRecipients(ctx, lvl.tenantID, audienceForLevel(lvl), func(cfg Config) bool {
		return cfg.DailySummaryEnabled
	})
	if len(recipients) == 0 {
		return nil
	}

	scope := "Fleet"
	if lvl.tenantID != "" {
		scope = summary.TenantName
		if scope == "" {
			scope = lvl.tenantID
		}
	}
	subject := fmt.Sprintf("[PrintMaster] Daily summary - %s (%s)", scope, summary.Date)
	htmlBody, textBody := renderSummaryEmail(n.themeFn(), n.serverURL(), summary)
	n.send(recipients, subject, htmlBody, textBody, "daily_summary")
	return nil
}

// audienceForLevel restricts delivery to the level that owns the summary so a
// tenant digest never reaches fleet admins and vice versa.
func audienceForLevel(lvl level) Audience {
	if lvl.tenantID == "" {
		return AudienceFleet
	}
	return AudienceTenant
}

func (n *Notifier) buildSummary(ctx context.Context, lvl level) (Summary, error) {
	filters := storage.AlertFilters{TenantID: lvl.tenantID}
	alerts, err := n.store.ListActiveAlerts(ctx, filters)
	if err != nil {
		return Summary{}, fmt.Errorf("list active alerts: %w", err)
	}

	summary := Summary{
		Level:      "fleet",
		TenantID:   lvl.tenantID,
		TenantName: lvl.tenantName,
		Date:       n.now().In(summaryLocation(lvl.cfg)).Format("2006-01-02"),
	}
	if lvl.tenantID != "" {
		summary.Level = "tenant"
	}
	for _, a := range alerts {
		switch a.Severity {
		case storage.AlertSeverityCritical:
			summary.Critical++
		case storage.AlertSeverityWarning:
			summary.Warning++
		default:
			summary.Info++
		}
	}

	sort.SliceStable(alerts, func(i, j int) bool {
		return alerts[i].TriggeredAt.After(alerts[j].TriggeredAt)
	})
	if len(alerts) > 20 {
		alerts = alerts[:20]
	}
	summary.Alerts = alerts
	return summary, nil
}

func (n *Notifier) alreadySent(key, day string) bool {
	n.summaryMu.Lock()
	defer n.summaryMu.Unlock()
	return n.lastSummary[key] == day
}

func (n *Notifier) markSent(key, day string) {
	n.summaryMu.Lock()
	defer n.summaryMu.Unlock()
	n.lastSummary[key] = day
}

// summaryDue reports whether the configured send time has passed for the current
// day in the level's timezone, along with that day's date key.
func summaryDue(now time.Time, cfg Config) (bool, string) {
	loc := summaryLocation(cfg)
	local := now.In(loc)
	day := local.Format("2006-01-02")

	hour, minute, ok := parseHHMM(cfg.DailySummaryTime)
	if !ok {
		hour, minute = 8, 0
	}
	scheduled := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if local.Before(scheduled) {
		return false, day
	}
	// Skip stale catch-up sends more than an hour past the scheduled slot.
	if local.Sub(scheduled) > time.Hour {
		return false, day
	}
	return true, day
}

func summaryLocation(cfg Config) *time.Location {
	name := strings.TrimSpace(cfg.DailySummaryTimezone)
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

func parseHHMM(value string) (hour, minute int, ok bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// ValidSummaryTime reports whether value is a well-formed HH:MM time of day.
func ValidSummaryTime(value string) bool {
	_, _, ok := parseHHMM(value)
	return ok
}

// ValidTimezone reports whether value names a loadable timezone (or "Local").
func ValidTimezone(value string) bool {
	name := strings.TrimSpace(value)
	if name == "" || strings.EqualFold(name, "local") {
		return true
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
