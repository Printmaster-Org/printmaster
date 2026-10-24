// Package email provides themed HTML email templates for PrintMaster.
package email

import (
	"bytes"
	"fmt"
	"html"
	htmltemplate "html/template"
	"strings"
	"text/template"
)

// Theme represents the email color scheme
type Theme string

const (
	ThemeDark  Theme = "dark"
	ThemeLight Theme = "light"
	ThemeAuto  Theme = "auto" // Include both themes with @media query
)

// Colors for Solarized themes (matching server web UI)
type themeColors struct {
	Background    string
	Panel         string
	Text          string
	TextMuted     string
	Accent        string // Gold/Yellow
	Highlight     string // Blue/Orange
	Border        string
	ButtonBg      string
	ButtonText    string
	ButtonHover   string
	Success       string
	Warning       string
	Danger        string
	PreBackground string
}

var darkColors = themeColors{
	Background:    "#002b36",
	Panel:         "#073642",
	Text:          "#93a1a1",
	TextMuted:     "#586e75",
	Accent:        "#b58900",
	Highlight:     "#268bd2",
	Border:        "#004b56",
	ButtonBg:      "#268bd2",
	ButtonText:    "#002b36",
	ButtonHover:   "#2aa198",
	Success:       "#2aa198",
	Warning:       "#cb4b16",
	Danger:        "#dc322f",
	PreBackground: "#073642",
}

var lightColors = themeColors{
	Background:    "#fdf6e3",
	Panel:         "#eee8d5",
	Text:          "#586e75",
	TextMuted:     "#93a1a1",
	Accent:        "#b58900",
	Highlight:     "#cb4b16",
	Border:        "#d3cbb8",
	ButtonBg:      "#268bd2",
	ButtonText:    "#fdf6e3",
	ButtonHover:   "#859900",
	Success:       "#859900",
	Warning:       "#cb4b16",
	Danger:        "#dc322f",
	PreBackground: "#eee8d5",
}

// InviteEmailData holds data for the invitation email template
type InviteEmailData struct {
	Email      string
	Role       string
	InviteURL  string
	ExpiresIn  string
	InvitedBy  string // Name of the user who sent the invitation
	TenantName string // Name of the tenant (organization), empty if global
	ServerURL  string // Server base URL for context
}

// PasswordResetEmailData holds data for the password reset email template
type PasswordResetEmailData struct {
	Email     string
	ResetURL  string
	ExpiresIn string
}

// AgentDeploymentEmailData holds data for the agent deployment script email
type AgentDeploymentEmailData struct {
	RecipientEmail     string // The email address receiving this
	Platform           string // linux, windows, darwin
	OneLiner           string // The one-liner command to run
	Script             string // The full bootstrap script
	DownloadURL        string // The hosted URL for the script
	InstructionsURL    string // Browser instructions using the same expiring install link
	InsecureSkipVerify bool   // Disable TLS certificate validation for the initial Agent connection
	ExpiresIn          string // Human-readable TTL
	TenantName         string // Tenant/customer name
	ServerURL          string // Server base URL
	SentBy             string // Name of the user who sent this
}

// GenerateInviteEmail generates a themed HTML invitation email
func GenerateInviteEmail(theme Theme, data InviteEmailData) (htmlBody, textBody string, err error) {
	colors := getThemeColors(theme)

	htmlTmpl := template.Must(template.New("invite").Parse(inviteHTMLTemplate))
	textTmpl := template.Must(template.New("inviteText").Parse(inviteTextTemplate))

	tmplData := struct {
		InviteEmailData
		themeColors
		Theme       Theme
		DarkColors  themeColors
		LightColors themeColors
	}{
		InviteEmailData: data,
		themeColors:     colors,
		Theme:           theme,
		DarkColors:      darkColors,
		LightColors:     lightColors,
	}

	var htmlBuf, textBuf bytes.Buffer
	if err := htmlTmpl.Execute(&htmlBuf, tmplData); err != nil {
		return "", "", fmt.Errorf("html template: %w", err)
	}
	if err := textTmpl.Execute(&textBuf, tmplData); err != nil {
		return "", "", fmt.Errorf("text template: %w", err)
	}

	return htmlBuf.String(), textBuf.String(), nil
}

// GeneratePasswordResetEmail generates a themed HTML password reset email
func GeneratePasswordResetEmail(theme Theme, data PasswordResetEmailData) (htmlBody, textBody string, err error) {
	colors := getThemeColors(theme)

	htmlTmpl := template.Must(template.New("reset").Parse(resetHTMLTemplate))
	textTmpl := template.Must(template.New("resetText").Parse(resetTextTemplate))

	tmplData := struct {
		PasswordResetEmailData
		themeColors
		Theme       Theme
		DarkColors  themeColors
		LightColors themeColors
	}{
		PasswordResetEmailData: data,
		themeColors:            colors,
		Theme:                  theme,
		DarkColors:             darkColors,
		LightColors:            lightColors,
	}

	var htmlBuf, textBuf bytes.Buffer
	if err := htmlTmpl.Execute(&htmlBuf, tmplData); err != nil {
		return "", "", fmt.Errorf("html template: %w", err)
	}
	if err := textTmpl.Execute(&textBuf, tmplData); err != nil {
		return "", "", fmt.Errorf("text template: %w", err)
	}

	return htmlBuf.String(), textBuf.String(), nil
}

// GenerateAgentDeploymentEmail generates a themed HTML email with agent deployment instructions
func GenerateAgentDeploymentEmail(theme Theme, data AgentDeploymentEmailData) (htmlBody, textBody string, err error) {
	colors := getThemeColors(theme)

	htmlTmpl := htmltemplate.Must(htmltemplate.New("deploy").Parse(agentDeployHTMLTemplate))
	textTmpl := template.Must(template.New("deployText").Parse(agentDeployTextTemplate))

	tmplData := struct {
		AgentDeploymentEmailData
		themeColors
		Theme       Theme
		DarkColors  themeColors
		LightColors themeColors
		UsesHTTP    bool
	}{
		AgentDeploymentEmailData: data,
		themeColors:              colors,
		Theme:                    theme,
		DarkColors:               darkColors,
		LightColors:              lightColors,
		UsesHTTP:                 strings.Contains(data.OneLiner, "http://"),
	}

	var htmlBuf, textBuf bytes.Buffer
	if err := htmlTmpl.Execute(&htmlBuf, tmplData); err != nil {
		return "", "", fmt.Errorf("html template: %w", err)
	}
	if err := textTmpl.Execute(&textBuf, tmplData); err != nil {
		return "", "", fmt.Errorf("text template: %w", err)
	}

	return htmlBuf.String(), textBuf.String(), nil
}

func getThemeColors(theme Theme) themeColors {
	switch theme {
	case ThemeLight:
		return lightColors
	case ThemeDark:
		return darkColors
	default:
		// For "auto", we use dark as the base (most email clients default to light, so dark makes a statement)
		return darkColors
	}
}

// NormalizeTheme converts a theme string to a valid Theme constant
func NormalizeTheme(s string) Theme {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dark":
		return ThemeDark
	case "light":
		return ThemeLight
	case "auto", "":
		return ThemeAuto
	default:
		return ThemeAuto
	}
}

// EscapeHTML safely escapes HTML entities
func EscapeHTML(s string) string {
	return html.EscapeString(s)
}

// Plain text invitation template
const inviteTextTemplate = `Hello,

{{if .InvitedBy}}{{.InvitedBy}} has invited you to join {{else}}You have been invited to join {{end}}{{if .TenantName}}{{.TenantName}} on {{end}}PrintMaster as a {{.Role}}.

Click the link below to set up your account:
{{.InviteURL}}

This invitation expires in {{.ExpiresIn}}.{{if .ServerURL}}

Server: {{.ServerURL}}{{end}}

If you did not expect this invitation, you can safely ignore this email.

---
PrintMaster - Printer Fleet Management
`

// Plain text password reset template
const resetTextTemplate = `Hello,

You requested a password reset for {{.Email}}.

Use the following link to reset your password:
{{.ResetURL}}

This link is valid for {{.ExpiresIn}}.

If you did not request this, ignore this message.

---
PrintMaster - Printer Fleet Management
`

// Plain text agent deployment template
const agentDeployTextTemplate = `Hello,

{{if .SentBy}}{{.SentBy}} has sent{{else}}You have been sent{{end}} PrintMaster agent installation instructions{{if .TenantName}} for {{.TenantName}}{{end}}.

Platform: {{.Platform}}
{{if .ServerURL}}Server: {{.ServerURL}}
{{end}}Expires in: {{.ExpiresIn}}

Only run this command if you trust the Server and expected these instructions.
It downloads and executes a script with administrator privileges.
Keep this enrollment link private. Do not forward it or include it in screenshots or logs.
Initial enrollment trusts this Server. Signed update verification is a separate step for later updates.
{{if .InsecureSkipVerify}}WARNING: TLS certificate validation is disabled for Agent-to-Server connections. Use only for a self-signed Server and understand that a network attacker could impersonate it.
{{end}}
{{if eq .Platform "windows"}}Windows: use Administrator PowerShell on a machine without an existing Agent.
This bootstrap will not update, repair or remove an existing Agent.
{{else}}Run the command in a terminal; it uses sudo for installation.
{{end}}{{if .UsesHTTP}}Warning: this command uses unencrypted HTTP. Network interception can replace the script.
Use a trusted HTTPS installation path instead.
{{end}}{{if .InstructionsURL}}
=== INSTALLATION INSTRUCTIONS ===
{{.InstructionsURL}}
{{end}}

=== QUICK INSTALL (one-liner) ===
{{.OneLiner}}

=== DOWNLOAD URL ===
{{.DownloadURL}}

=== FULL SCRIPT ===
{{.Script}}

This installation script expires in {{.ExpiresIn}}.{{if .ServerURL}}

Server: {{.ServerURL}}{{end}}

---
PrintMaster - Printer Fleet Management
`

// HTML invitation template with responsive design
const inviteHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="color-scheme" content="{{if eq .Theme "light"}}light{{else if eq .Theme "dark"}}dark{{else}}light dark{{end}}">
    <title>PrintMaster Invitation</title>
    <!--[if mso]>
    <noscript>
        <xml>
            <o:OfficeDocumentSettings>
                <o:PixelsPerInch>96</o:PixelsPerInch>
            </o:OfficeDocumentSettings>
        </xml>
    </noscript>
    <![endif]-->
    <style>
        /* Reset and base styles */
        body, table, td, p, a, li {
            -webkit-text-size-adjust: 100%;
            -ms-text-size-adjust: 100%;
        }
        table, td {
            mso-table-lspace: 0pt;
            mso-table-rspace: 0pt;
        }
        img {
            -ms-interpolation-mode: bicubic;
            border: 0;
            height: auto;
            line-height: 100%;
            outline: none;
            text-decoration: none;
        }
        body {
            margin: 0 !important;
            padding: 0 !important;
            width: 100% !important;
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
        }
        a {
            text-decoration: none;
        }
        
        /* Theme styles */
        {{if eq .Theme "auto"}}
        /* Light mode (default for most email clients) */
        .email-body {
            background-color: {{.LightColors.Background}};
        }
        .email-container {
            background-color: {{.LightColors.Panel}};
            border-color: {{.LightColors.Border}};
        }
        .email-header {
            border-bottom-color: {{.LightColors.Border}};
        }
        .email-title {
            color: {{.LightColors.Accent}};
        }
        .email-text {
            color: {{.LightColors.Text}};
        }
        .email-muted {
            color: {{.LightColors.TextMuted}};
        }
        .email-button {
            background-color: {{.LightColors.ButtonBg}};
            color: {{.LightColors.ButtonText}} !important;
        }
        .email-link {
            color: {{.LightColors.Highlight}};
        }
        .email-footer {
            border-top-color: {{.LightColors.Border}};
        }
        .email-role-badge {
            background-color: {{.LightColors.Background}};
            color: {{.LightColors.Accent}};
            border-color: {{.LightColors.Accent}};
        }
        .email-url-box {
            background-color: {{.LightColors.PreBackground}};
            border-color: {{.LightColors.Border}};
        }
        
        /* Dark mode override */
        @media (prefers-color-scheme: dark) {
            .email-body {
                background-color: {{.DarkColors.Background}} !important;
            }
            .email-container {
                background-color: {{.DarkColors.Panel}} !important;
                border-color: {{.DarkColors.Border}} !important;
            }
            .email-header {
                border-bottom-color: {{.DarkColors.Border}} !important;
            }
            .email-title {
                color: {{.DarkColors.Accent}} !important;
            }
            .email-text {
                color: {{.DarkColors.Text}} !important;
            }
            .email-muted {
                color: {{.DarkColors.TextMuted}} !important;
            }
            .email-button {
                background-color: {{.DarkColors.ButtonBg}} !important;
                color: {{.DarkColors.ButtonText}} !important;
            }
            .email-link {
                color: {{.DarkColors.Highlight}} !important;
            }
            .email-footer {
                border-top-color: {{.DarkColors.Border}} !important;
            }
            .email-role-badge {
                background-color: {{.DarkColors.Background}} !important;
                color: {{.DarkColors.Accent}} !important;
                border-color: {{.DarkColors.Accent}} !important;
            }
            .email-url-box {
                background-color: {{.DarkColors.PreBackground}} !important;
                border-color: {{.DarkColors.Border}} !important;
            }
        }
        {{else}}
        /* Static theme */
        .email-body {
            background-color: {{.Background}};
        }
        .email-container {
            background-color: {{.Panel}};
            border-color: {{.Border}};
        }
        .email-header {
            border-bottom-color: {{.Border}};
        }
        .email-title {
            color: {{.Accent}};
        }
        .email-text {
            color: {{.Text}};
        }
        .email-muted {
            color: {{.TextMuted}};
        }
        .email-button {
            background-color: {{.ButtonBg}};
            color: {{.ButtonText}} !important;
        }
        .email-link {
            color: {{.Highlight}};
        }
        .email-footer {
            border-top-color: {{.Border}};
        }
        .email-role-badge {
            background-color: {{.Background}};
            color: {{.Accent}};
            border-color: {{.Accent}};
        }
        .email-url-box {
            background-color: {{.PreBackground}};
            border-color: {{.Border}};
        }
        {{end}}
    </style>
</head>
<body class="email-body" style="margin: 0; padding: 0;">
    <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" class="email-body">
        <tr>
            <td align="center" style="padding: 40px 20px;">
                <!-- Email container -->
                <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="max-width: 600px;" class="email-container" style="border-radius: 12px; border-width: 1px; border-style: solid;">
                    <tr>
                        <td style="border-radius: 12px; overflow: hidden;">
                            <!-- Header -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td class="email-header" style="padding: 30px 40px; border-bottom-width: 1px; border-bottom-style: solid;">
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                            <tr>
                                                <td>
                                                    <h1 class="email-title" style="margin: 0; font-size: 28px; font-weight: 700; letter-spacing: -0.5px;">
                                                        🖨️ PrintMaster
                                                    </h1>
                                                </td>
                                            </tr>
                                        </table>
                                    </td>
                                </tr>
                            </table>
                            
                            <!-- Body -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td style="padding: 40px;">
                                        <h2 class="email-text" style="margin: 0 0 20px 0; font-size: 22px; font-weight: 600;">
                                            You're Invited!
                                        </h2>
                                        
                                        <p class="email-text" style="margin: 0 0 20px 0; font-size: 16px; line-height: 1.6;">
                                            {{if .InvitedBy}}<strong>{{.InvitedBy}}</strong> has invited you to join {{else}}You have been invited to join {{end}}{{if .TenantName}}<strong>{{.TenantName}}</strong> on {{end}}<strong>PrintMaster</strong> with the following role:
                                        </p>
                                        
                                        <!-- Role badge -->
                                        <table role="presentation" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-role-badge" style="padding: 8px 16px; border-radius: 20px; border-width: 1px; border-style: solid; font-size: 14px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px;">
                                                    {{.Role}}
                                                </td>
                                            </tr>
                                        </table>
                                        
                                        <p class="email-text" style="margin: 0 0 30px 0; font-size: 16px; line-height: 1.6;">
                                            Click the button below to set up your account and get started with printer fleet management.
                                        </p>
                                        
                                        <!-- CTA Button -->
                                        <table role="presentation" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-button" style="border-radius: 8px;">
                                                    <a href="{{.InviteURL}}" class="email-button" style="display: inline-block; padding: 14px 32px; font-size: 16px; font-weight: 600; text-decoration: none; border-radius: 8px;">
                                                        Accept Invitation
                                                    </a>
                                                </td>
                                            </tr>
                                        </table>
                                        
                                        <p class="email-muted" style="margin: 0 0 20px 0; font-size: 14px; line-height: 1.6;">
                                            Or copy and paste this link into your browser:
                                        </p>
                                        
                                        <!-- URL box -->
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-url-box" style="padding: 12px 16px; border-radius: 6px; border-width: 1px; border-style: solid; word-break: break-all;">
                                                    <a href="{{.InviteURL}}" class="email-link" style="font-size: 13px; font-family: 'SF Mono', Monaco, 'Courier New', monospace;">
                                                        {{.InviteURL}}
                                                    </a>
                                                </td>
                                            </tr>
                                        </table>
                                        
                                        <p class="email-muted" style="margin: 0; font-size: 14px; line-height: 1.6;">
                                            ⏱️ This invitation expires in <strong>{{.ExpiresIn}}</strong>.
                                        </p>
                                    </td>
                                </tr>
                            </table>
                            
                            <!-- Footer -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td class="email-footer" style="padding: 24px 40px; border-top-width: 1px; border-top-style: solid;">
                                        <p class="email-muted" style="margin: 0; font-size: 13px; line-height: 1.5;">
                                            If you did not expect this invitation, you can safely ignore this email.
                                        </p>
                                        {{if .ServerURL}}<p class="email-muted" style="margin: 12px 0 0 0; font-size: 12px;">
                                            Server: <a href="{{.ServerURL}}" class="email-link" style="font-size: 12px;">{{.ServerURL}}</a>
                                        </p>{{end}}
                                        <p class="email-muted" style="margin: 12px 0 0 0; font-size: 12px;">
                                            PrintMaster - Printer Fleet Management
                                        </p>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`

// HTML password reset template with responsive design
const resetHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="color-scheme" content="{{if eq .Theme "light"}}light{{else if eq .Theme "dark"}}dark{{else}}light dark{{end}}">
    <title>PrintMaster Password Reset</title>
    <!--[if mso]>
    <noscript>
        <xml>
            <o:OfficeDocumentSettings>
                <o:PixelsPerInch>96</o:PixelsPerInch>
            </o:OfficeDocumentSettings>
        </xml>
    </noscript>
    <![endif]-->
    <style>
        /* Reset and base styles */
        body, table, td, p, a, li {
            -webkit-text-size-adjust: 100%;
            -ms-text-size-adjust: 100%;
        }
        table, td {
            mso-table-lspace: 0pt;
            mso-table-rspace: 0pt;
        }
        img {
            -ms-interpolation-mode: bicubic;
            border: 0;
            height: auto;
            line-height: 100%;
            outline: none;
            text-decoration: none;
        }
        body {
            margin: 0 !important;
            padding: 0 !important;
            width: 100% !important;
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
        }
        a {
            text-decoration: none;
        }
        
        /* Theme styles */
        {{if eq .Theme "auto"}}
        /* Light mode (default for most email clients) */
        .email-body {
            background-color: {{.LightColors.Background}};
        }
        .email-container {
            background-color: {{.LightColors.Panel}};
            border-color: {{.LightColors.Border}};
        }
        .email-header {
            border-bottom-color: {{.LightColors.Border}};
        }
        .email-title {
            color: {{.LightColors.Accent}};
        }
        .email-text {
            color: {{.LightColors.Text}};
        }
        .email-muted {
            color: {{.LightColors.TextMuted}};
        }
        .email-button {
            background-color: {{.LightColors.ButtonBg}};
            color: {{.LightColors.ButtonText}} !important;
        }
        .email-link {
            color: {{.LightColors.Highlight}};
        }
        .email-footer {
            border-top-color: {{.LightColors.Border}};
        }
        .email-warning-box {
            background-color: rgba(203, 75, 22, 0.1);
            border-color: {{.LightColors.Warning}};
        }
        .email-warning-text {
            color: {{.LightColors.Warning}};
        }
        .email-url-box {
            background-color: {{.LightColors.PreBackground}};
            border-color: {{.LightColors.Border}};
        }
        
        /* Dark mode override */
        @media (prefers-color-scheme: dark) {
            .email-body {
                background-color: {{.DarkColors.Background}} !important;
            }
            .email-container {
                background-color: {{.DarkColors.Panel}} !important;
                border-color: {{.DarkColors.Border}} !important;
            }
            .email-header {
                border-bottom-color: {{.DarkColors.Border}} !important;
            }
            .email-title {
                color: {{.DarkColors.Accent}} !important;
            }
            .email-text {
                color: {{.DarkColors.Text}} !important;
            }
            .email-muted {
                color: {{.DarkColors.TextMuted}} !important;
            }
            .email-button {
                background-color: {{.DarkColors.ButtonBg}} !important;
                color: {{.DarkColors.ButtonText}} !important;
            }
            .email-link {
                color: {{.DarkColors.Highlight}} !important;
            }
            .email-footer {
                border-top-color: {{.DarkColors.Border}} !important;
            }
            .email-warning-box {
                background-color: rgba(203, 75, 22, 0.15) !important;
                border-color: {{.DarkColors.Warning}} !important;
            }
            .email-warning-text {
                color: {{.DarkColors.Warning}} !important;
            }
            .email-url-box {
                background-color: {{.DarkColors.PreBackground}} !important;
                border-color: {{.DarkColors.Border}} !important;
            }
        }
        {{else}}
        /* Static theme */
        .email-body {
            background-color: {{.Background}};
        }
        .email-container {
            background-color: {{.Panel}};
            border-color: {{.Border}};
        }
        .email-header {
            border-bottom-color: {{.Border}};
        }
        .email-title {
            color: {{.Accent}};
        }
        .email-text {
            color: {{.Text}};
        }
        .email-muted {
            color: {{.TextMuted}};
        }
        .email-button {
            background-color: {{.ButtonBg}};
            color: {{.ButtonText}} !important;
        }
        .email-link {
            color: {{.Highlight}};
        }
        .email-footer {
            border-top-color: {{.Border}};
        }
        .email-warning-box {
            background-color: rgba(203, 75, 22, 0.1);
            border-color: {{.Warning}};
        }
        .email-warning-text {
            color: {{.Warning}};
        }
        .email-url-box {
            background-color: {{.PreBackground}};
            border-color: {{.Border}};
        }
        {{end}}
    </style>
</head>
<body class="email-body" style="margin: 0; padding: 0;">
    <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" class="email-body">
        <tr>
            <td align="center" style="padding: 40px 20px;">
                <!-- Email container -->
                <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="max-width: 600px;" class="email-container" style="border-radius: 12px; border-width: 1px; border-style: solid;">
                    <tr>
                        <td style="border-radius: 12px; overflow: hidden;">
                            <!-- Header -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td class="email-header" style="padding: 30px 40px; border-bottom-width: 1px; border-bottom-style: solid;">
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                            <tr>
                                                <td>
                                                    <h1 class="email-title" style="margin: 0; font-size: 28px; font-weight: 700; letter-spacing: -0.5px;">
                                                        🖨️ PrintMaster
                                                    </h1>
                                                </td>
                                            </tr>
                                        </table>
                                    </td>
                                </tr>
                            </table>
                            
                            <!-- Body -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td style="padding: 40px;">
                                        <h2 class="email-text" style="margin: 0 0 20px 0; font-size: 22px; font-weight: 600;">
                                            Password Reset Request
                                        </h2>
                                        
                                        <p class="email-text" style="margin: 0 0 20px 0; font-size: 16px; line-height: 1.6;">
                                            You requested a password reset for your PrintMaster account associated with <strong>{{.Email}}</strong>.
                                        </p>
                                        
                                        <p class="email-text" style="margin: 0 0 30px 0; font-size: 16px; line-height: 1.6;">
                                            Click the button below to reset your password:
                                        </p>
                                        
                                        <!-- CTA Button -->
                                        <table role="presentation" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-button" style="border-radius: 8px;">
                                                    <a href="{{.ResetURL}}" class="email-button" style="display: inline-block; padding: 14px 32px; font-size: 16px; font-weight: 600; text-decoration: none; border-radius: 8px;">
                                                        Reset Password
                                                    </a>
                                                </td>
                                            </tr>
                                        </table>
                                        
                                        <p class="email-muted" style="margin: 0 0 20px 0; font-size: 14px; line-height: 1.6;">
                                            Or copy and paste this link into your browser:
                                        </p>
                                        
                                        <!-- URL box -->
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-url-box" style="padding: 12px 16px; border-radius: 6px; border-width: 1px; border-style: solid; word-break: break-all;">
                                                    <a href="{{.ResetURL}}" class="email-link" style="font-size: 13px; font-family: 'SF Mono', Monaco, 'Courier New', monospace;">
                                                        {{.ResetURL}}
                                                    </a>
                                                </td>
                                            </tr>
                                        </table>
                                        
                                        <p class="email-muted" style="margin: 0 0 30px 0; font-size: 14px; line-height: 1.6;">
                                            ⏱️ This link is valid for <strong>{{.ExpiresIn}}</strong>.
                                        </p>
                                        
                                        <!-- Warning box -->
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                            <tr>
                                                <td class="email-warning-box" style="padding: 16px; border-radius: 8px; border-width: 1px; border-style: solid; border-left-width: 4px;">
                                                    <p class="email-warning-text" style="margin: 0; font-size: 14px; line-height: 1.5;">
                                                        ⚠️ <strong>Didn't request this?</strong><br>
                                                        If you didn't request a password reset, you can safely ignore this email. Your password will remain unchanged.
                                                    </p>
                                                </td>
                                            </tr>
                                        </table>
                                    </td>
                                </tr>
                            </table>
                            
                            <!-- Footer -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td class="email-footer" style="padding: 24px 40px; border-top-width: 1px; border-top-style: solid;">
                                        <p class="email-muted" style="margin: 0; font-size: 13px; line-height: 1.5;">
                                            This is an automated message from PrintMaster. Please do not reply to this email.
                                        </p>
                                        <p class="email-muted" style="margin: 12px 0 0 0; font-size: 12px;">
                                            PrintMaster - Printer Fleet Management
                                        </p>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`

// HTML agent deployment template with responsive design
const agentDeployHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="color-scheme" content="{{if eq .Theme "light"}}light{{else if eq .Theme "dark"}}dark{{else}}light dark{{end}}">
    <title>PrintMaster Agent Installation</title>
    <!--[if mso]>
    <noscript>
        <xml>
            <o:OfficeDocumentSettings>
                <o:PixelsPerInch>96</o:PixelsPerInch>
            </o:OfficeDocumentSettings>
        </xml>
    </noscript>
    <![endif]-->
    <style>
        /* Reset and base styles */
        body, table, td, p, a, li {
            -webkit-text-size-adjust: 100%;
            -ms-text-size-adjust: 100%;
        }
        table, td {
            mso-table-lspace: 0pt;
            mso-table-rspace: 0pt;
        }
        img {
            -ms-interpolation-mode: bicubic;
            border: 0;
            height: auto;
            line-height: 100%;
            outline: none;
            text-decoration: none;
        }
        body {
            margin: 0 !important;
            padding: 0 !important;
            width: 100% !important;
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
        }
        a {
            text-decoration: none;
        }
        
        /* Theme styles */
        {{if eq .Theme "auto"}}
        /* Light mode (default for most email clients) */
        .email-body {
            background-color: {{.LightColors.Background}};
        }
        .email-container {
            background-color: {{.LightColors.Panel}};
            border-color: {{.LightColors.Border}};
        }
        .email-header {
            border-bottom-color: {{.LightColors.Border}};
        }
        .email-title {
            color: {{.LightColors.Accent}};
        }
        .email-text {
            color: {{.LightColors.Text}};
        }
        .email-muted {
            color: {{.LightColors.TextMuted}};
        }
        .email-button {
            background-color: {{.LightColors.ButtonBg}};
            color: {{.LightColors.ButtonText}} !important;
        }
        .email-link {
            color: {{.LightColors.Highlight}};
        }
        .email-footer {
            border-top-color: {{.LightColors.Border}};
        }
        .email-code-box {
            background-color: {{.LightColors.PreBackground}};
            border-color: {{.LightColors.Border}};
            color: {{.LightColors.Text}};
        }
        .email-platform-badge {
            background-color: {{.LightColors.Background}};
            color: {{.LightColors.Accent}};
            border-color: {{.LightColors.Accent}};
        }
        
        /* Dark mode override */
        @media (prefers-color-scheme: dark) {
            .email-body {
                background-color: {{.DarkColors.Background}} !important;
            }
            .email-container {
                background-color: {{.DarkColors.Panel}} !important;
                border-color: {{.DarkColors.Border}} !important;
            }
            .email-header {
                border-bottom-color: {{.DarkColors.Border}} !important;
            }
            .email-title {
                color: {{.DarkColors.Accent}} !important;
            }
            .email-text {
                color: {{.DarkColors.Text}} !important;
            }
            .email-muted {
                color: {{.DarkColors.TextMuted}} !important;
            }
            .email-button {
                background-color: {{.DarkColors.ButtonBg}} !important;
                color: {{.DarkColors.ButtonText}} !important;
            }
            .email-link {
                color: {{.DarkColors.Highlight}} !important;
            }
            .email-footer {
                border-top-color: {{.DarkColors.Border}} !important;
            }
            .email-code-box {
                background-color: {{.DarkColors.PreBackground}} !important;
                border-color: {{.DarkColors.Border}} !important;
                color: {{.DarkColors.Text}} !important;
            }
            .email-platform-badge {
                background-color: {{.DarkColors.Background}} !important;
                color: {{.DarkColors.Accent}} !important;
                border-color: {{.DarkColors.Accent}} !important;
            }
        }
        {{else}}
        /* Static theme */
        .email-body {
            background-color: {{.Background}};
        }
        .email-container {
            background-color: {{.Panel}};
            border-color: {{.Border}};
        }
        .email-header {
            border-bottom-color: {{.Border}};
        }
        .email-title {
            color: {{.Accent}};
        }
        .email-text {
            color: {{.Text}};
        }
        .email-muted {
            color: {{.TextMuted}};
        }
        .email-button {
            background-color: {{.ButtonBg}};
            color: {{.ButtonText}} !important;
        }
        .email-link {
            color: {{.Highlight}};
        }
        .email-footer {
            border-top-color: {{.Border}};
        }
        .email-code-box {
            background-color: {{.PreBackground}};
            border-color: {{.Border}};
            color: {{.Text}};
        }
        .email-platform-badge {
            background-color: {{.Background}};
            color: {{.Accent}};
            border-color: {{.Accent}};
        }
        {{end}}
    </style>
</head>
<body class="email-body" style="margin: 0; padding: 0;">
    <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" class="email-body">
        <tr>
            <td align="center" style="padding: 40px 20px;">
                <!-- Email container -->
                <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="max-width: 600px;" class="email-container" style="border-radius: 12px; border-width: 1px; border-style: solid;">
                    <tr>
                        <td style="border-radius: 12px; overflow: hidden;">
                            <!-- Header -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td class="email-header" style="padding: 30px 40px; border-bottom-width: 1px; border-bottom-style: solid;">
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                            <tr>
                                                <td>
                                                    <h1 class="email-title" style="margin: 0; font-size: 28px; font-weight: 700; letter-spacing: -0.5px;">
                                                        🖨️ PrintMaster
                                                    </h1>
                                                </td>
                                            </tr>
                                        </table>
                                    </td>
                                </tr>
                            </table>
                            
                            <!-- Body -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td style="padding: 40px;">
                                        <h2 class="email-text" style="margin: 0 0 20px 0; font-size: 22px; font-weight: 600;">
                                            Agent Installation Instructions
                                        </h2>
                                        
                                        <p class="email-text" style="margin: 0 0 20px 0; font-size: 16px; line-height: 1.6;">
                                            {{if .SentBy}}<strong>{{.SentBy}}</strong> has sent{{else}}You have been sent{{end}} agent installation instructions{{if .TenantName}} for <strong>{{.TenantName}}</strong>{{end}}.
                                        </p>
                                        
                                        <!-- Platform badge -->
                                        <table role="presentation" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-platform-badge" style="padding: 8px 16px; border-radius: 20px; border-width: 1px; border-style: solid; font-size: 14px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px;">
                                                    {{.Platform}}
                                                </td>
                                            </tr>
                                        </table>
                                        
                                        <!-- Quick Install Section -->
                                        <p class="email-text" style="margin: 0 0 16px 0; font-size: 14px; line-height: 1.6;">
                                            Only run this command if you trust the Server and expected these instructions.
                                            It downloads and executes a script with administrator privileges.
                                            Keep this enrollment link private; do not forward it or include it in screenshots or logs.
                                            Initial enrollment trusts this Server. Signed update verification is a separate step for later updates.
                                        </p>
                                        {{if eq .Platform "windows"}}<p class="email-text" style="margin: 0 0 16px 0; font-size: 14px; line-height: 1.6;">
                                            Use Administrator PowerShell on a machine without an existing Agent.
                                            This bootstrap will not update, repair or remove an existing Agent.
                                        </p>{{end}}
                                        <p class="email-text" style="margin: 0 0 16px 0; font-size: 14px; line-height: 1.6;">
                                            {{if .ServerURL}}Server: {{.ServerURL}}<br>{{end}}
                                            Link expires in {{.ExpiresIn}}.
                                        </p>
                                        {{if .UsesHTTP}}<p class="email-text" style="margin: 0 0 16px 0; font-size: 14px; line-height: 1.6;">
                                            <strong>This command uses unencrypted HTTP.</strong> Network interception can replace the script.
                                            Use a trusted HTTPS installation path instead.
                                        </p>{{end}}
                                        {{if .InstructionsURL}}<p style="margin: 0 0 20px 0;">
                                            <a href="{{.InstructionsURL}}" class="email-button" style="display:inline-block;padding:12px 20px;border-radius:6px;font-weight:600;text-decoration:none;">View installation instructions</a>
                                        </p>{{end}}
                                        {{if .InsecureSkipVerify}}<p class="email-text" style="margin:0 0 16px 0;font-size:14px;line-height:1.6;border-left:3px solid {{.Warning}};padding:0 12px;">
                                            <strong>Warning: TLS certificate validation is disabled</strong> for Agent-to-Server connections. Use only for a self-signed Server; a network attacker could impersonate it.
                                        </p>{{end}}
                                        <h3 class="email-text" style="margin: 0 0 12px 0; font-size: 18px; font-weight: 600;">
                                            ⚡ Quick Install (One-liner)
                                        </h3>
                                        <p class="email-muted" style="margin: 0 0 12px 0; font-size: 14px; line-height: 1.6;">
                                            Run this command in {{if eq .Platform "windows"}}an elevated PowerShell terminal{{else}}your terminal{{end}}:
                                        </p>
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-code-box" style="padding: 16px; border-radius: 8px; border-width: 1px; border-style: solid; word-break: break-all;">
                                                    <code style="font-size: 13px; font-family: 'SF Mono', Monaco, 'Courier New', monospace; white-space: pre-wrap;">{{.OneLiner}}</code>
                                                </td>
                                            </tr>
                                        </table>

                                        <!-- Download URL Section -->
                                        {{if .DownloadURL}}
                                        <h3 class="email-text" style="margin: 0 0 12px 0; font-size: 18px; font-weight: 600;">
                                            📥 Download URL
                                        </h3>
                                        <p class="email-muted" style="margin: 0 0 12px 0; font-size: 14px; line-height: 1.6;">
                                            Or download the script directly:
                                        </p>
                                        <table role="presentation" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-button" style="border-radius: 8px;">
                                                    <a href="{{.DownloadURL}}" class="email-button" style="display: inline-block; padding: 14px 32px; font-size: 16px; font-weight: 600; text-decoration: none; border-radius: 8px;">
                                                        Download Script
                                                    </a>
                                                </td>
                                            </tr>
                                        </table>
                                        {{end}}
                                        
                                        <!-- Full Script Section -->
                                        <details style="margin: 0 0 20px 0;">
                                        <summary class="email-text" style="font-size: 16px; font-weight: 600; cursor: pointer;">View full installation script</summary>
                                        <p class="email-muted" style="margin: 0 0 12px 0; font-size: 14px; line-height: 1.6;">
                                            If you prefer, save this script to a file and run it manually:
                                        </p>
                                        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="margin: 0 0 30px 0;">
                                            <tr>
                                                <td class="email-code-box" style="padding: 16px; border-radius: 8px; border-width: 1px; border-style: solid; max-height: 300px; overflow: auto;">
                                                    <pre style="margin: 0; font-size: 11px; font-family: 'SF Mono', Monaco, 'Courier New', monospace; white-space: pre-wrap; word-wrap: break-word;">{{.Script}}</pre>
                                                </td>
                                            </tr>
                                        </table>
                                        </details>
                                        
                                        <p class="email-muted" style="margin: 0; font-size: 14px; line-height: 1.6;">
                                            ⏱️ This installation script expires in <strong>{{.ExpiresIn}}</strong>.
                                        </p>
                                    </td>
                                </tr>
                            </table>
                            
                            <!-- Footer -->
                            <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">
                                <tr>
                                    <td class="email-footer" style="padding: 24px 40px; border-top-width: 1px; border-top-style: solid;">
                                        <p class="email-muted" style="margin: 0; font-size: 13px; line-height: 1.5;">
                                            If you did not expect these instructions, you can safely ignore this email.
                                        </p>
                                        {{if .ServerURL}}<p class="email-muted" style="margin: 12px 0 0 0; font-size: 12px;">
                                            Server: <a href="{{.ServerURL}}" class="email-link" style="font-size: 12px;">{{.ServerURL}}</a>
                                        </p>{{end}}
                                        <p class="email-muted" style="margin: 12px 0 0 0; font-size: 12px;">
                                            PrintMaster - Printer Fleet Management
                                        </p>
                                    </td>
                                </tr>
                            </table>
                        </td>
                    </tr>
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`
