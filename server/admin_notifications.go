package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	adminnotify "printmaster/server/adminnotify"
	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

// fleetNotificationConfig reads the server-level notification settings on every
// dispatch so changes made through the settings UI apply without a restart.
func fleetNotificationConfig() adminnotify.Config {
	if serverConfig == nil {
		return adminnotify.Config{}
	}
	n := serverConfig.Notifications
	return adminnotify.Config{
		Enabled:              n.Enabled,
		Recipients:           n.AdminEmails,
		NotifyOnCritical:     n.NotifyOnCritical,
		NotifyOnWarning:      n.NotifyOnWarning,
		DailySummaryEnabled:  n.DailySummaryEnabled,
		DailySummaryTime:     n.DailySummaryTime,
		DailySummaryTimezone: n.DailySummaryTimezone,
	}
}

func sendAdminNotificationEmail(to, subject, htmlBody, textBody string) error {
	return sendHTMLEmail(to, subject, htmlBody, textBody)
}

// configuredServerURL derives a public base URL for links in background emails,
// where no inbound request is available to read the host from.
func configuredServerURL() string {
	if serverConfig == nil {
		return ""
	}
	domain := strings.TrimSpace(serverConfig.TLS.LetsEncrypt.Domain)
	if domain == "" {
		domain = strings.TrimSpace(serverConfig.TLS.Domain)
	}
	if domain == "" || domain == "localhost" {
		return ""
	}
	return "https://" + domain
}

// raiseAdminAlert latches a system alert and notifies the matching admin levels.
// Failures are logged rather than propagated: notification problems must never
// break the operation that detected the condition.
func raiseAdminAlert(ctx context.Context, ev adminnotify.Event) {
	if _, err := adminNotifier.Raise(ctx, ev); err != nil {
		logWarn("Failed to raise admin notification", "key", ev.Key, "error", err)
	}
}

// clearAdminAlert resolves a latched system alert so the next occurrence notifies again.
func clearAdminAlert(ctx context.Context, key, tenantID string) {
	if err := adminNotifier.Clear(ctx, key, tenantID); err != nil {
		logWarn("Failed to clear admin notification", "key", key, "error", err)
	}
}

// handleFleetNotificationTest sends a test notification to the server-level recipients.
func handleFleetNotificationTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !authorizeOrReject(w, r, authz.ActionSettingsServerWrite, authz.ResourceRef{}) {
		return
	}
	sent, err := adminNotifier.SendTest(r.Context(), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actorType, actorID, actorName, actorTenant := auditActorFromPrincipal(r)
	logAuditEntry(r.Context(), &storage.AuditEntry{
		ActorType: actorType,
		ActorID:   actorID,
		ActorName: actorName,
		TenantID:  actorTenant,
		Action:    "settings.notifications.test",
		Details:   fmt.Sprintf("sent to %d recipient(s)", sent),
		IPAddress: extractClientIP(r),
		UserAgent: r.Header.Get("User-Agent"),
		Severity:  storage.AuditSeverityInfo,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"sent": sent})
}
