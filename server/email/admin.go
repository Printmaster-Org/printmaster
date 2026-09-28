package email

import (
	"bytes"
	"fmt"
	"text/template"
)

// AdminAlertRow is one alert line inside an admin email.
type AdminAlertRow struct {
	Severity    string
	Title       string
	Message     string
	TriggeredAt string
	Scope       string
}

// AdminAlertEmailData holds data for a single system alert notification.
type AdminAlertEmailData struct {
	Severity  string
	Title     string
	Message   string
	Details   string
	Scope     string // "Fleet" or the tenant name
	Timestamp string
	ServerURL string
}

// AdminSummaryEmailData holds data for the daily administrator digest.
type AdminSummaryEmailData struct {
	Scope     string
	Date      string
	Critical  int
	Warning   int
	Info      int
	Total     int
	Alerts    []AdminAlertRow
	Truncated bool
	ServerURL string
}

// GenerateAdminAlertEmail renders a themed notification for one system alert.
func GenerateAdminAlertEmail(theme Theme, data AdminAlertEmailData) (htmlBody, textBody string, err error) {
	htmlBody, err = renderAdminTemplate(theme, "adminAlert", adminAlertHTMLTemplate, data.escaped())
	if err != nil {
		return "", "", err
	}
	textBody, err = renderAdminTemplate(theme, "adminAlertText", adminAlertTextTemplate, data)
	if err != nil {
		return "", "", err
	}
	return htmlBody, textBody, nil
}

// GenerateAdminSummaryEmail renders the themed daily administrator digest.
func GenerateAdminSummaryEmail(theme Theme, data AdminSummaryEmailData) (htmlBody, textBody string, err error) {
	htmlBody, err = renderAdminTemplate(theme, "adminSummary", adminSummaryHTMLTemplate, data.escaped())
	if err != nil {
		return "", "", err
	}
	textBody, err = renderAdminTemplate(theme, "adminSummaryText", adminSummaryTextTemplate, data)
	if err != nil {
		return "", "", err
	}
	return htmlBody, textBody, nil
}

// escaped returns a copy with every caller-supplied field HTML-escaped, because
// these templates are parsed with text/template and do not escape automatically.
func (d AdminAlertEmailData) escaped() AdminAlertEmailData {
	return AdminAlertEmailData{
		Severity:  EscapeHTML(d.Severity),
		Title:     EscapeHTML(d.Title),
		Message:   EscapeHTML(d.Message),
		Details:   EscapeHTML(d.Details),
		Scope:     EscapeHTML(d.Scope),
		Timestamp: EscapeHTML(d.Timestamp),
		ServerURL: EscapeHTML(d.ServerURL),
	}
}

func (d AdminSummaryEmailData) escaped() AdminSummaryEmailData {
	out := d
	out.Scope = EscapeHTML(d.Scope)
	out.Date = EscapeHTML(d.Date)
	out.ServerURL = EscapeHTML(d.ServerURL)
	out.Alerts = make([]AdminAlertRow, 0, len(d.Alerts))
	for _, row := range d.Alerts {
		out.Alerts = append(out.Alerts, AdminAlertRow{
			Severity:    EscapeHTML(row.Severity),
			Title:       EscapeHTML(row.Title),
			Message:     EscapeHTML(row.Message),
			TriggeredAt: EscapeHTML(row.TriggeredAt),
			Scope:       EscapeHTML(row.Scope),
		})
	}
	return out
}

func renderAdminTemplate(theme Theme, name, src string, data interface{}) (string, error) {
	tmplData := struct {
		Data interface{}
		themeColors
		Theme       Theme
		DarkColors  themeColors
		LightColors themeColors
	}{
		Data:        data,
		themeColors: getThemeColors(theme),
		Theme:       theme,
		DarkColors:  darkColors,
		LightColors: lightColors,
	}

	tmpl, err := template.New(name).Parse(src)
	if err != nil {
		return "", fmt.Errorf("%s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, tmplData); err != nil {
		return "", fmt.Errorf("%s template: %w", name, err)
	}
	return buf.String(), nil
}

const adminStyleBlock = `
    <style>
        body { margin:0 !important; padding:0 !important; width:100% !important;
               font-family:-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Arial, sans-serif; }
        a { text-decoration:none; }
        {{if eq .Theme "auto"}}
        .email-body { background-color: {{.LightColors.Background}}; }
        .email-container { background-color: {{.LightColors.Panel}}; border-color: {{.LightColors.Border}}; }
        .email-title { color: {{.LightColors.Accent}}; }
        .email-text { color: {{.LightColors.Text}}; }
        .email-muted { color: {{.LightColors.TextMuted}}; }
        .email-link { color: {{.LightColors.Highlight}}; }
        .email-box { background-color: {{.LightColors.PreBackground}}; border-color: {{.LightColors.Border}}; }
        @media (prefers-color-scheme: dark) {
            .email-body { background-color: {{.DarkColors.Background}} !important; }
            .email-container { background-color: {{.DarkColors.Panel}} !important; border-color: {{.DarkColors.Border}} !important; }
            .email-title { color: {{.DarkColors.Accent}} !important; }
            .email-text { color: {{.DarkColors.Text}} !important; }
            .email-muted { color: {{.DarkColors.TextMuted}} !important; }
            .email-link { color: {{.DarkColors.Highlight}} !important; }
            .email-box { background-color: {{.DarkColors.PreBackground}} !important; border-color: {{.DarkColors.Border}} !important; }
        }
        {{else}}
        .email-body { background-color: {{.Background}}; }
        .email-container { background-color: {{.Panel}}; border-color: {{.Border}}; }
        .email-title { color: {{.Accent}}; }
        .email-text { color: {{.Text}}; }
        .email-muted { color: {{.TextMuted}}; }
        .email-link { color: {{.Highlight}}; }
        .email-box { background-color: {{.PreBackground}}; border-color: {{.Border}}; }
        {{end}}
    </style>`

const adminAlertHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="{{if eq .Theme "light"}}light{{else if eq .Theme "dark"}}dark{{else}}light dark{{end}}">
<title>PrintMaster Notification</title>` + adminStyleBlock + `
</head>
<body class="email-body" style="margin:0;padding:24px 0;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center">
<table role="presentation" class="email-container" width="600" cellpadding="0" cellspacing="0"
       style="max-width:600px;border-width:1px;border-style:solid;border-radius:8px;padding:24px;">
<tr><td>
  <div class="email-muted" style="font-size:12px;letter-spacing:1px;text-transform:uppercase;">PrintMaster &middot; {{.Data.Scope}}</div>
  <h1 class="email-title" style="font-size:20px;margin:8px 0 4px 0;">{{.Data.Title}}</h1>
  <div style="display:inline-block;font-size:12px;font-weight:700;text-transform:uppercase;padding:3px 8px;border-radius:4px;color:#ffffff;background-color:{{if eq .Data.Severity "critical"}}{{.Danger}}{{else if eq .Data.Severity "warning"}}{{.Warning}}{{else}}{{.Highlight}}{{end}};">{{.Data.Severity}}</div>
  <p class="email-text" style="font-size:14px;line-height:1.6;">{{.Data.Message}}</p>
  {{if .Data.Details}}
  <div class="email-box" style="border-width:1px;border-style:solid;border-radius:6px;padding:12px;font-family:monospace;font-size:12px;word-break:break-word;">
    <span class="email-text">{{.Data.Details}}</span>
  </div>
  {{end}}
  <p class="email-muted" style="font-size:12px;margin-top:16px;">Triggered {{.Data.Timestamp}}</p>
  {{if .Data.ServerURL}}
  <p style="font-size:13px;"><a class="email-link" href="{{.Data.ServerURL}}/#alerts">Open PrintMaster alerts</a></p>
  {{end}}
  <p class="email-muted" style="font-size:11px;margin-top:20px;">You receive this because you are listed as a PrintMaster administrator. This alert is sent once and will not repeat until it is resolved.</p>
</td></tr>
</table>
</td></tr></table>
</body>
</html>`

const adminAlertTextTemplate = `PrintMaster notification - {{.Data.Scope}}

[{{.Data.Severity}}] {{.Data.Title}}

{{.Data.Message}}
{{if .Data.Details}}
Details:
{{.Data.Details}}
{{end}}
Triggered {{.Data.Timestamp}}
{{if .Data.ServerURL}}
Alerts: {{.Data.ServerURL}}/#alerts
{{end}}
This alert is sent once and will not repeat until it is resolved.
`

const adminSummaryHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="{{if eq .Theme "light"}}light{{else if eq .Theme "dark"}}dark{{else}}light dark{{end}}">
<title>PrintMaster Daily Summary</title>` + adminStyleBlock + `
</head>
<body class="email-body" style="margin:0;padding:24px 0;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center">
<table role="presentation" class="email-container" width="600" cellpadding="0" cellspacing="0"
       style="max-width:600px;border-width:1px;border-style:solid;border-radius:8px;padding:24px;">
<tr><td>
  <div class="email-muted" style="font-size:12px;letter-spacing:1px;text-transform:uppercase;">PrintMaster &middot; {{.Data.Scope}}</div>
  <h1 class="email-title" style="font-size:20px;margin:8px 0 16px 0;">Daily summary for {{.Data.Date}}</h1>
  <table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="margin-bottom:16px;">
    <tr>
      <td align="center" style="padding:8px;"><div style="font-size:24px;font-weight:700;color:{{.Danger}};">{{.Data.Critical}}</div><div class="email-muted" style="font-size:12px;">Critical</div></td>
      <td align="center" style="padding:8px;"><div style="font-size:24px;font-weight:700;color:{{.Warning}};">{{.Data.Warning}}</div><div class="email-muted" style="font-size:12px;">Warning</div></td>
      <td align="center" style="padding:8px;"><div style="font-size:24px;font-weight:700;color:{{.Highlight}};">{{.Data.Info}}</div><div class="email-muted" style="font-size:12px;">Info</div></td>
    </tr>
  </table>
  {{if .Data.Alerts}}
  <table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="font-size:13px;">
    {{range .Data.Alerts}}
    <tr><td style="padding:8px 0;border-bottom:1px solid {{$.Border}};">
      <span style="font-size:11px;font-weight:700;text-transform:uppercase;color:{{if eq .Severity "critical"}}{{$.Danger}}{{else if eq .Severity "warning"}}{{$.Warning}}{{else}}{{$.Highlight}}{{end}};">{{.Severity}}</span>
      <span class="email-text" style="font-weight:600;"> {{.Title}}</span>
      <div class="email-muted" style="font-size:12px;">{{.Scope}} &middot; {{.TriggeredAt}}</div>
      <div class="email-text" style="font-size:12px;">{{.Message}}</div>
    </td></tr>
    {{end}}
  </table>
  {{if .Data.Truncated}}<p class="email-muted" style="font-size:12px;">Showing the {{len .Data.Alerts}} most recent of {{.Data.Total}} unresolved alerts.</p>{{end}}
  {{else}}
  <p class="email-text" style="font-size:14px;">No unresolved alerts. Everything looks healthy.</p>
  {{end}}
  {{if .Data.ServerURL}}
  <p style="font-size:13px;"><a class="email-link" href="{{.Data.ServerURL}}/#alerts">Open PrintMaster alerts</a></p>
  {{end}}
  <p class="email-muted" style="font-size:11px;margin-top:20px;">You receive this because daily summaries are enabled for your PrintMaster administrator address.</p>
</td></tr>
</table>
</td></tr></table>
</body>
</html>`

const adminSummaryTextTemplate = `PrintMaster daily summary - {{.Data.Scope}} - {{.Data.Date}}

Critical: {{.Data.Critical}}   Warning: {{.Data.Warning}}   Info: {{.Data.Info}}
{{if .Data.Alerts}}
Unresolved alerts:
{{range .Data.Alerts}}- [{{.Severity}}] {{.Title}} ({{.Scope}}, {{.TriggeredAt}})
  {{.Message}}
{{end}}{{if .Data.Truncated}}Showing the most recent {{len .Data.Alerts}} of {{.Data.Total}} unresolved alerts.
{{end}}{{else}}
No unresolved alerts. Everything looks healthy.
{{end}}{{if .Data.ServerURL}}
Alerts: {{.Data.ServerURL}}/#alerts
{{end}}`
