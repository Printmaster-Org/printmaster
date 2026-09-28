package adminnotify

import (
	"strings"

	emailtpl "printmaster/server/email"
	"printmaster/server/storage"
)

const timestampLayout = "2006-01-02 15:04 MST"

// renderAlertEmail builds the HTML and plain-text bodies for a single alert.
// On template failure it degrades to a plain-text-only message rather than
// dropping the notification.
func renderAlertEmail(theme, serverURL string, alert *storage.Alert) (htmlBody, textBody string) {
	data := emailtpl.AdminAlertEmailData{
		Severity:  alert.Severity,
		Title:     alert.Title,
		Message:   alert.Message,
		Details:   alert.Details,
		Scope:     scopeLabel(alert.TenantID, ""),
		Timestamp: alert.TriggeredAt.Format(timestampLayout),
		ServerURL: strings.TrimRight(serverURL, "/"),
	}
	html, text, err := emailtpl.GenerateAdminAlertEmail(emailtpl.NormalizeTheme(theme), data)
	if err != nil {
		return "", fallbackAlertText(data)
	}
	return html, text
}

// renderSummaryEmail builds the HTML and plain-text bodies for a daily digest.
func renderSummaryEmail(theme, serverURL string, summary Summary) (htmlBody, textBody string) {
	rows := make([]emailtpl.AdminAlertRow, 0, len(summary.Alerts))
	for _, a := range summary.Alerts {
		rows = append(rows, emailtpl.AdminAlertRow{
			Severity:    a.Severity,
			Title:       a.Title,
			Message:     a.Message,
			TriggeredAt: a.TriggeredAt.Format(timestampLayout),
			Scope:       a.Scope,
		})
	}
	data := emailtpl.AdminSummaryEmailData{
		Scope:     scopeLabel(summary.TenantID, summary.TenantName),
		Date:      summary.Date,
		Critical:  summary.Critical,
		Warning:   summary.Warning,
		Info:      summary.Info,
		Total:     summary.Total(),
		Alerts:    rows,
		Truncated: summary.Total() > len(rows),
		ServerURL: strings.TrimRight(serverURL, "/"),
	}
	html, text, err := emailtpl.GenerateAdminSummaryEmail(emailtpl.NormalizeTheme(theme), data)
	if err != nil {
		return "", fallbackSummaryText(data)
	}
	return html, text
}

func scopeLabel(tenantID, tenantName string) string {
	if strings.TrimSpace(tenantID) == "" {
		return "Fleet"
	}
	if strings.TrimSpace(tenantName) != "" {
		return tenantName
	}
	return tenantID
}

func fallbackAlertText(d emailtpl.AdminAlertEmailData) string {
	var b strings.Builder
	b.WriteString("[" + d.Severity + "] " + d.Title + "\n\n")
	b.WriteString(d.Message + "\n")
	if d.Details != "" {
		b.WriteString("\nDetails:\n" + d.Details + "\n")
	}
	b.WriteString("\nScope: " + d.Scope + "\nTriggered " + d.Timestamp + "\n")
	return b.String()
}

func fallbackSummaryText(d emailtpl.AdminSummaryEmailData) string {
	var b strings.Builder
	b.WriteString("PrintMaster daily summary - " + d.Scope + " - " + d.Date + "\n\n")
	for _, row := range d.Alerts {
		b.WriteString("- [" + row.Severity + "] " + row.Title + " (" + row.TriggeredAt + ")\n")
	}
	if len(d.Alerts) == 0 {
		b.WriteString("No unresolved alerts.\n")
	}
	return b.String()
}
