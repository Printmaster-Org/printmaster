package email

import (
	"strings"
	"testing"
)

// TestThemeConstants verifies theme constants are defined correctly
func TestThemeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		theme Theme
		want  string
	}{
		{ThemeDark, "dark"},
		{ThemeLight, "light"},
		{ThemeAuto, "auto"},
	}

	for _, tt := range tests {
		if string(tt.theme) != tt.want {
			t.Errorf("Theme constant %v = %q, want %q", tt.theme, tt.theme, tt.want)
		}
	}
}

// TestNormalizeTheme verifies theme string normalization
func TestNormalizeTheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  Theme
	}{
		{"dark", ThemeDark},
		{"DARK", ThemeDark},
		{"Dark", ThemeDark},
		{" dark ", ThemeDark},
		{"light", ThemeLight},
		{"LIGHT", ThemeLight},
		{"Light", ThemeLight},
		{" light ", ThemeLight},
		{"auto", ThemeAuto},
		{"AUTO", ThemeAuto},
		{"", ThemeAuto},
		{"invalid", ThemeAuto},
		{"  ", ThemeAuto},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeTheme(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeTheme(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestEscapeHTML verifies HTML escaping
func TestEscapeHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"<script>alert('xss')</script>", "&lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;"},
		{"a & b", "a &amp; b"},
		{`"quotes"`, "&#34;quotes&#34;"},
		{"<>&\"'", "&lt;&gt;&amp;&#34;&#39;"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := EscapeHTML(tt.input)
			if got != tt.want {
				t.Errorf("EscapeHTML(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestGetThemeColors verifies correct color palette selection
func TestGetThemeColors(t *testing.T) {
	t.Parallel()

	// Dark theme should return dark colors
	dark := getThemeColors(ThemeDark)
	if dark.Background != "#002b36" {
		t.Errorf("Dark theme background = %q, want #002b36", dark.Background)
	}

	// Light theme should return light colors
	light := getThemeColors(ThemeLight)
	if light.Background != "#fdf6e3" {
		t.Errorf("Light theme background = %q, want #fdf6e3", light.Background)
	}

	// Auto theme defaults to dark
	auto := getThemeColors(ThemeAuto)
	if auto.Background != "#002b36" {
		t.Errorf("Auto theme background = %q, want #002b36 (dark default)", auto.Background)
	}
}

// TestGenerateInviteEmail tests invitation email generation
func TestGenerateInviteEmail(t *testing.T) {
	t.Parallel()

	data := InviteEmailData{
		Email:      "test@example.com",
		Role:       "Operator",
		InviteURL:  "https://printmaster.example.com/invite/abc123",
		ExpiresIn:  "48 hours",
		InvitedBy:  "Admin User",
		TenantName: "Acme Corp",
		ServerURL:  "https://printmaster.example.com",
	}

	themes := []Theme{ThemeDark, ThemeLight, ThemeAuto}

	for _, theme := range themes {
		t.Run(string(theme), func(t *testing.T) {
			htmlBody, textBody, err := GenerateInviteEmail(theme, data)
			if err != nil {
				t.Fatalf("GenerateInviteEmail(%v) error = %v", theme, err)
			}

			// Verify HTML body contains expected content
			htmlChecks := []string{
				"PrintMaster",
				"You're Invited",
				data.InviteURL,
				data.Role,
				data.ExpiresIn,
				data.InvitedBy,
				data.TenantName,
				"Accept Invitation",
			}
			for _, check := range htmlChecks {
				if !strings.Contains(htmlBody, check) {
					t.Errorf("HTML body missing %q", check)
				}
			}

			// Verify text body contains expected content
			textChecks := []string{
				"PrintMaster",
				data.InviteURL,
				data.Role,
				data.ExpiresIn,
				data.InvitedBy,
				data.TenantName,
			}
			for _, check := range textChecks {
				if !strings.Contains(textBody, check) {
					t.Errorf("Text body missing %q", check)
				}
			}

			// Verify HTML structure
			if !strings.Contains(htmlBody, "<!DOCTYPE html>") {
				t.Error("HTML body missing DOCTYPE")
			}
			if !strings.Contains(htmlBody, "</html>") {
				t.Error("HTML body missing closing html tag")
			}
		})
	}
}

// TestGenerateInviteEmailMinimal tests with minimal data
func TestGenerateInviteEmailMinimal(t *testing.T) {
	t.Parallel()

	data := InviteEmailData{
		Email:     "test@example.com",
		Role:      "Viewer",
		InviteURL: "https://example.com/invite/xyz",
		ExpiresIn: "24 hours",
		// No InvitedBy, TenantName, or ServerURL
	}

	htmlBody, textBody, err := GenerateInviteEmail(ThemeDark, data)
	if err != nil {
		t.Fatalf("GenerateInviteEmail error = %v", err)
	}

	// Should still render without optional fields
	if !strings.Contains(htmlBody, data.InviteURL) {
		t.Error("HTML body missing invite URL")
	}
	if !strings.Contains(textBody, data.InviteURL) {
		t.Error("Text body missing invite URL")
	}

	// Should use generic phrasing when InvitedBy is empty
	if !strings.Contains(textBody, "You have been invited") {
		t.Error("Text body should use generic invitation phrasing")
	}
}

// TestGeneratePasswordResetEmail tests password reset email generation
func TestGeneratePasswordResetEmail(t *testing.T) {
	t.Parallel()

	data := PasswordResetEmailData{
		Email:     "user@example.com",
		ResetURL:  "https://printmaster.example.com/reset/token123",
		ExpiresIn: "1 hour",
	}

	themes := []Theme{ThemeDark, ThemeLight, ThemeAuto}

	for _, theme := range themes {
		t.Run(string(theme), func(t *testing.T) {
			htmlBody, textBody, err := GeneratePasswordResetEmail(theme, data)
			if err != nil {
				t.Fatalf("GeneratePasswordResetEmail(%v) error = %v", theme, err)
			}

			// Verify HTML body contains expected content
			htmlChecks := []string{
				"PrintMaster",
				"Password Reset",
				data.ResetURL,
				data.Email,
				data.ExpiresIn,
				"Reset Password", // Button text
			}
			for _, check := range htmlChecks {
				if !strings.Contains(htmlBody, check) {
					t.Errorf("HTML body missing %q", check)
				}
			}

			// Verify text body contains expected content
			textChecks := []string{
				"password reset",
				data.ResetURL,
				data.Email,
				data.ExpiresIn,
			}
			for _, check := range textChecks {
				if !strings.Contains(strings.ToLower(textBody), strings.ToLower(check)) {
					t.Errorf("Text body missing %q", check)
				}
			}

			// Verify HTML structure
			if !strings.Contains(htmlBody, "<!DOCTYPE html>") {
				t.Error("HTML body missing DOCTYPE")
			}
		})
	}
}

// TestGenerateAgentDeploymentEmail tests agent deployment email generation
func TestGenerateAgentDeploymentEmail(t *testing.T) {
	t.Parallel()

	data := AgentDeploymentEmailData{
		RecipientEmail: "admin@example.com",
		Platform:       "linux",
		OneLiner:       "curl -sSL https://example.com/install.sh | bash",
		Script:         "#!/bin/bash\necho 'Installing PrintMaster Agent'",
		DownloadURL:    "https://printmaster.example.com/scripts/install.sh",
		ExpiresIn:      "7 days",
		TenantName:     "Acme Corp",
		ServerURL:      "https://printmaster.example.com",
		SentBy:         "IT Admin",
	}

	themes := []Theme{ThemeDark, ThemeLight, ThemeAuto}

	for _, theme := range themes {
		t.Run(string(theme), func(t *testing.T) {
			htmlBody, textBody, err := GenerateAgentDeploymentEmail(theme, data)
			if err != nil {
				t.Fatalf("GenerateAgentDeploymentEmail(%v) error = %v", theme, err)
			}

			// Verify HTML body contains expected content
			htmlChecks := []string{
				"PrintMaster",
				data.Platform,
				data.OneLiner,
				data.DownloadURL,
				data.ExpiresIn,
				data.TenantName,
				data.SentBy,
			}
			for _, check := range htmlChecks {
				if !strings.Contains(htmlBody, check) {
					t.Errorf("HTML body missing %q", check)
				}
			}

			// Verify text body contains expected content
			textChecks := []string{
				"PrintMaster",
				data.Platform,
				data.OneLiner,
				data.DownloadURL,
				data.ExpiresIn,
				data.TenantName,
				data.SentBy,
			}
			for _, check := range textChecks {
				if !strings.Contains(textBody, check) {
					t.Errorf("Text body missing %q", check)
				}
			}

			// Verify HTML structure
			if !strings.Contains(htmlBody, "<!DOCTYPE html>") {
				t.Error("HTML body missing DOCTYPE")
			}
		})
	}
}

// TestGenerateAgentDeploymentEmailPlatforms tests different platforms
func TestDeploymentEmailTrustNoticeAndEscaping(t *testing.T) {
	t.Parallel()
	for _, theme := range []Theme{ThemeDark, ThemeLight, ThemeAuto} {
		data := AgentDeploymentEmailData{Platform: "windows", OneLiner: "irm https://example.invalid/install/fixture?a=1&b=2 | iex",
			Script: "<script>alert('fixture')</script>", DownloadURL: "https://example.invalid/install/fixture",
			InstructionsURL: "https://example.invalid/install/fixture/instructions", ExpiresIn: "60 minutes",
			TenantName: "<img src=x onerror=alert(1)>", SentBy: "<b>untrusted sender</b>"}
		htmlBody, textBody, err := GenerateAgentDeploymentEmail(theme, data)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{data.InstructionsURL, "trust the Server", "Keep this enrollment link private", "Administrator PowerShell"} {
			if !strings.Contains(htmlBody, text) || !strings.Contains(textBody, text) {
				t.Fatalf("deployment message missing guidance %q", text)
			}
		}
		if !strings.Contains(htmlBody, "<details") || !strings.Contains(htmlBody, "&lt;script&gt;") ||
			strings.Contains(htmlBody, "<img src=x") || strings.Contains(htmlBody, data.SentBy) || strings.Contains(htmlBody, "<script>") {
			t.Fatal("deployment HTML contains unescaped content or lacks script disclosure")
		}
		instructionsLink := `href="` + data.InstructionsURL + `"`
		downloadLink := `href="` + data.DownloadURL + `"`
		if !strings.Contains(htmlBody, instructionsLink) || !strings.Contains(htmlBody, downloadLink) ||
			strings.Index(htmlBody, instructionsLink) > strings.Index(htmlBody, downloadLink) {
			t.Fatal("instructions must be the primary email link and raw download a fallback")
		}
		if !strings.Contains(textBody, data.OneLiner) || !strings.Contains(textBody, data.Script) {
			t.Fatal("plain-text fallback changed command/script bytes")
		}
	}
}

func TestGenerateAgentDeploymentEmailPlatforms(t *testing.T) {
	t.Parallel()

	platforms := []string{"linux", "windows", "darwin"}

	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			data := AgentDeploymentEmailData{
				RecipientEmail: "admin@example.com",
				Platform:       platform,
				OneLiner:       "install command",
				Script:         "install script",
				DownloadURL:    "https://example.com/install",
				ExpiresIn:      "24 hours",
			}

			htmlBody, textBody, err := GenerateAgentDeploymentEmail(ThemeDark, data)
			if err != nil {
				t.Fatalf("GenerateAgentDeploymentEmail error = %v", err)
			}

			if !strings.Contains(htmlBody, platform) {
				t.Errorf("HTML body missing platform %q", platform)
			}
			if !strings.Contains(textBody, platform) {
				t.Errorf("Text body missing platform %q", platform)
			}
		})
	}
}

// TestGenerateAgentDeploymentEmailMinimal tests with minimal data
func TestGenerateAgentDeploymentEmailMinimal(t *testing.T) {
	t.Parallel()

	data := AgentDeploymentEmailData{
		RecipientEmail: "admin@example.com",
		Platform:       "linux",
		OneLiner:       "install",
		Script:         "script",
		DownloadURL:    "https://example.com",
		ExpiresIn:      "1 hour",
		// No TenantName, ServerURL, or SentBy
	}

	htmlBody, textBody, err := GenerateAgentDeploymentEmail(ThemeDark, data)
	if err != nil {
		t.Fatalf("GenerateAgentDeploymentEmail error = %v", err)
	}

	// Should still render without optional fields
	if !strings.Contains(htmlBody, data.OneLiner) {
		t.Error("HTML body missing one-liner")
	}
	if !strings.Contains(textBody, data.OneLiner) {
		t.Error("Text body missing one-liner")
	}

	// Should use generic phrasing when SentBy is empty
	if !strings.Contains(textBody, "You have been sent") {
		t.Error("Text body should use generic phrasing")
	}
}

// TestThemeColorsComplete verifies all color fields are populated
func TestThemeColorsComplete(t *testing.T) {
	t.Parallel()

	checkColors := func(name string, colors themeColors) {
		if colors.Background == "" {
			t.Errorf("%s: Background is empty", name)
		}
		if colors.Panel == "" {
			t.Errorf("%s: Panel is empty", name)
		}
		if colors.Text == "" {
			t.Errorf("%s: Text is empty", name)
		}
		if colors.TextMuted == "" {
			t.Errorf("%s: TextMuted is empty", name)
		}
		if colors.Accent == "" {
			t.Errorf("%s: Accent is empty", name)
		}
		if colors.Highlight == "" {
			t.Errorf("%s: Highlight is empty", name)
		}
		if colors.Border == "" {
			t.Errorf("%s: Border is empty", name)
		}
		if colors.ButtonBg == "" {
			t.Errorf("%s: ButtonBg is empty", name)
		}
		if colors.ButtonText == "" {
			t.Errorf("%s: ButtonText is empty", name)
		}
		if colors.ButtonHover == "" {
			t.Errorf("%s: ButtonHover is empty", name)
		}
		if colors.Success == "" {
			t.Errorf("%s: Success is empty", name)
		}
		if colors.Warning == "" {
			t.Errorf("%s: Warning is empty", name)
		}
		if colors.Danger == "" {
			t.Errorf("%s: Danger is empty", name)
		}
		if colors.PreBackground == "" {
			t.Errorf("%s: PreBackground is empty", name)
		}
	}

	checkColors("darkColors", darkColors)
	checkColors("lightColors", lightColors)
}

// TestEmailHTMLValidity performs basic HTML validity checks
func TestEmailHTMLValidity(t *testing.T) {
	t.Parallel()

	generators := []struct {
		name     string
		generate func() (string, string, error)
	}{
		{
			"InviteEmail",
			func() (string, string, error) {
				return GenerateInviteEmail(ThemeDark, InviteEmailData{
					Email:     "test@example.com",
					Role:      "Operator",
					InviteURL: "https://example.com/invite",
					ExpiresIn: "24h",
				})
			},
		},
		{
			"PasswordResetEmail",
			func() (string, string, error) {
				return GeneratePasswordResetEmail(ThemeDark, PasswordResetEmailData{
					Email:     "test@example.com",
					ResetURL:  "https://example.com/reset",
					ExpiresIn: "1h",
				})
			},
		},
		{
			"AgentDeploymentEmail",
			func() (string, string, error) {
				return GenerateAgentDeploymentEmail(ThemeDark, AgentDeploymentEmailData{
					RecipientEmail: "test@example.com",
					Platform:       "linux",
					OneLiner:       "curl example",
					Script:         "script",
					DownloadURL:    "https://example.com",
					ExpiresIn:      "7d",
				})
			},
		},
	}

	for _, gen := range generators {
		t.Run(gen.name, func(t *testing.T) {
			htmlBody, _, err := gen.generate()
			if err != nil {
				t.Fatalf("Generation error: %v", err)
			}

			// Check for balanced tags
			checks := []struct {
				open, close string
			}{
				{"<html", "</html>"},
				{"<head>", "</head>"},
				{"<body", "</body>"},
				{"<table", "</table>"},
				{"<style>", "</style>"},
			}

			for _, check := range checks {
				openCount := strings.Count(htmlBody, check.open)
				closeCount := strings.Count(htmlBody, check.close)
				if openCount != closeCount {
					t.Errorf("Unbalanced %s tags: %d open, %d close", check.open, openCount, closeCount)
				}
			}

			// Check for DOCTYPE
			if !strings.HasPrefix(htmlBody, "<!DOCTYPE html>") {
				t.Error("Missing DOCTYPE declaration at start")
			}

			// Check for meta charset
			if !strings.Contains(htmlBody, `charset="UTF-8"`) && !strings.Contains(htmlBody, `charset=UTF-8`) {
				t.Error("Missing UTF-8 charset declaration")
			}

			// Check for viewport meta
			if !strings.Contains(htmlBody, "viewport") {
				t.Error("Missing viewport meta tag")
			}
		})
	}
}

// TestEmailAutoThemeIncludesBothColorSchemes verifies auto theme has media queries
func TestEmailAutoThemeIncludesBothColorSchemes(t *testing.T) {
	t.Parallel()

	htmlBody, _, err := GenerateInviteEmail(ThemeAuto, InviteEmailData{
		Email:     "test@example.com",
		Role:      "Admin",
		InviteURL: "https://example.com",
		ExpiresIn: "24h",
	})
	if err != nil {
		t.Fatalf("GenerateInviteEmail error = %v", err)
	}

	// Auto theme should include prefers-color-scheme media query
	if !strings.Contains(htmlBody, "prefers-color-scheme") {
		t.Error("Auto theme should include prefers-color-scheme media query")
	}

	// Should include both light and dark color values
	if !strings.Contains(htmlBody, darkColors.Background) {
		t.Error("Auto theme should include dark background color")
	}
	if !strings.Contains(htmlBody, lightColors.Background) {
		t.Error("Auto theme should include light background color")
	}
}

// TestEmailSpecialCharacterHandling verifies special chars in data are handled
func TestEmailSpecialCharacterHandling(t *testing.T) {
	t.Parallel()

	data := InviteEmailData{
		Email:      "test@example.com",
		Role:       "Operator",
		InviteURL:  "https://example.com/invite?token=abc&user=123",
		ExpiresIn:  "24 hours",
		InvitedBy:  "Admin <script>alert('xss')</script>",
		TenantName: "Acme & Sons \"Corp\"",
	}

	htmlBody, textBody, err := GenerateInviteEmail(ThemeDark, data)
	if err != nil {
		t.Fatalf("GenerateInviteEmail error = %v", err)
	}

	// URL should be preserved (& is valid in URLs)
	if !strings.Contains(htmlBody, data.InviteURL) {
		t.Error("URL should be preserved in HTML")
	}

	// Text body should contain the URL as-is
	if !strings.Contains(textBody, data.InviteURL) {
		t.Error("URL should be preserved in text body")
	}
}

// TestEmailNonEmptyOutput verifies generators don't return empty strings
func TestEmailNonEmptyOutput(t *testing.T) {
	t.Parallel()

	// Invite email
	html, text, err := GenerateInviteEmail(ThemeDark, InviteEmailData{
		Email:     "a@b.com",
		Role:      "Admin",
		InviteURL: "https://x.com",
		ExpiresIn: "1h",
	})
	if err != nil {
		t.Fatalf("InviteEmail error: %v", err)
	}
	if html == "" {
		t.Error("InviteEmail returned empty HTML")
	}
	if text == "" {
		t.Error("InviteEmail returned empty text")
	}

	// Password reset email
	html, text, err = GeneratePasswordResetEmail(ThemeDark, PasswordResetEmailData{
		Email:     "a@b.com",
		ResetURL:  "https://x.com",
		ExpiresIn: "1h",
	})
	if err != nil {
		t.Fatalf("PasswordResetEmail error: %v", err)
	}
	if html == "" {
		t.Error("PasswordResetEmail returned empty HTML")
	}
	if text == "" {
		t.Error("PasswordResetEmail returned empty text")
	}

	// Agent deployment email
	html, text, err = GenerateAgentDeploymentEmail(ThemeDark, AgentDeploymentEmailData{
		RecipientEmail: "a@b.com",
		Platform:       "linux",
		OneLiner:       "cmd",
		Script:         "script",
		DownloadURL:    "https://x.com",
		ExpiresIn:      "1h",
	})
	if err != nil {
		t.Fatalf("AgentDeploymentEmail error: %v", err)
	}
	if html == "" {
		t.Error("AgentDeploymentEmail returned empty HTML")
	}
	if text == "" {
		t.Error("AgentDeploymentEmail returned empty text")
	}
}

// BenchmarkGenerateInviteEmail benchmarks invite email generation
func BenchmarkGenerateInviteEmail(b *testing.B) {
	data := InviteEmailData{
		Email:      "test@example.com",
		Role:       "Operator",
		InviteURL:  "https://printmaster.example.com/invite/abc123",
		ExpiresIn:  "48 hours",
		InvitedBy:  "Admin User",
		TenantName: "Acme Corp",
		ServerURL:  "https://printmaster.example.com",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = GenerateInviteEmail(ThemeAuto, data)
	}
}

// BenchmarkGeneratePasswordResetEmail benchmarks password reset email generation
func BenchmarkGeneratePasswordResetEmail(b *testing.B) {
	data := PasswordResetEmailData{
		Email:     "user@example.com",
		ResetURL:  "https://printmaster.example.com/reset/token123",
		ExpiresIn: "1 hour",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = GeneratePasswordResetEmail(ThemeAuto, data)
	}
}

// BenchmarkGenerateAgentDeploymentEmail benchmarks agent deployment email generation
func BenchmarkGenerateAgentDeploymentEmail(b *testing.B) {
	data := AgentDeploymentEmailData{
		RecipientEmail: "admin@example.com",
		Platform:       "linux",
		OneLiner:       "curl -sSL https://example.com/install.sh | bash",
		Script:         "#!/bin/bash\necho 'Installing PrintMaster Agent'",
		DownloadURL:    "https://printmaster.example.com/scripts/install.sh",
		ExpiresIn:      "7 days",
		TenantName:     "Acme Corp",
		ServerURL:      "https://printmaster.example.com",
		SentBy:         "IT Admin",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = GenerateAgentDeploymentEmail(ThemeAuto, data)
	}
}
