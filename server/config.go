package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"printmaster/common/config"
)

// ConfigSourceTracker records which keys were set by environment variables.
// Used to enforce lock semantics: env-set keys cannot be overridden by managed settings.
type ConfigSourceTracker struct {
	EnvKeys map[string]bool // Keys that were set via environment variables
}

func newConfigSourceTracker() *ConfigSourceTracker {
	return &ConfigSourceTracker{
		EnvKeys: make(map[string]bool),
	}
}

// Config represents the server configuration
type Config struct {
	Server        ServerConfig          `toml:"server"`
	Security      SecurityConfig        `toml:"security"`
	TLS           TLSConfigTOML         `toml:"tls"`
	Database      config.DatabaseConfig `toml:"database"`
	Logging       config.LoggingConfig  `toml:"logging"`
	Tenancy       TenancyConfig         `toml:"tenancy"`
	SMTP          SMTPConfig            `toml:"smtp"`
	Notifications NotificationsConfig   `toml:"notifications"`
	Releases      ReleasesConfig        `toml:"releases"`
	SelfUpdate    SelfUpdateConfig      `toml:"self_update"`
}

// NotificationsConfig controls operator-facing notifications about the server itself
// (expiring credentials, failed background jobs, daily fleet summaries).
type NotificationsConfig struct {
	Enabled              bool     `toml:"enabled"`
	AdminEmails          []string `toml:"admin_emails"`
	NotifyOnCritical     bool     `toml:"notify_on_critical"`
	NotifyOnWarning      bool     `toml:"notify_on_warning"`
	DailySummaryEnabled  bool     `toml:"daily_summary_enabled"`
	DailySummaryTime     string   `toml:"daily_summary_time"` // HH:MM in DailySummaryTimezone
	DailySummaryTimezone string   `toml:"daily_summary_timezone"`
}

// ServerConfig holds server-specific settings
type ServerConfig struct {
	HTTPPort            int      `toml:"http_port"`
	HTTPSPort           int      `toml:"https_port"`
	BehindProxy         bool     `toml:"behind_proxy"`
	CloudflareProxy     bool     `toml:"cloudflare_proxy"` // If true, automatically trust Cloudflare IP ranges
	ProxyUseHTTPS       bool     `toml:"proxy_use_https"`  // If true, use HTTPS even when behind proxy (default: false for HTTP)
	TrustedProxies      []string `toml:"trusted_proxies"`  // CIDR ranges, IPs, or hostnames to trust for proxy headers
	BindAddress         string   `toml:"bind_address"`     // Address to bind to (default: 0.0.0.0 for all interfaces, 127.0.0.1 for localhost)
	AutoApproveAgents   bool     `toml:"auto_approve_agents"`
	AgentTimeoutMinutes int      `toml:"agent_timeout_minutes"`
	SelfUpdateEnabled   bool     `toml:"self_update_enabled"`
}

// ReleasesConfig tunes the GitHub release intake worker.
type ReleasesConfig struct {
	MaxReleases         int    `toml:"max_releases"`
	PollIntervalMinutes int    `toml:"poll_interval_minutes"`
	RetentionVersions   int    `toml:"retention_versions"` // 0 = disabled (keep all), N = keep N versions per component
	IncludePrerelease   string `toml:"include_prerelease"` // "true", "false", or "" (auto-detect from build type)
}

// SelfUpdateConfig exposes tweakable server auto-update controls.
type SelfUpdateConfig struct {
	Channel              string `toml:"channel"`
	MaxArtifacts         int    `toml:"max_artifacts"`
	CheckIntervalMinutes int    `toml:"check_interval_minutes"`
}

// SecurityConfig holds security and rate limiting settings
type SecurityConfig struct {
	RateLimitEnabled       bool `toml:"rate_limit_enabled"`        // Enable authentication rate limiting (default: true)
	RateLimitMaxAttempts   int  `toml:"rate_limit_max_attempts"`   // Max failed attempts before blocking (default: 5)
	RateLimitBlockMinutes  int  `toml:"rate_limit_block_minutes"`  // Minutes to block after max attempts (default: 5)
	RateLimitWindowMinutes int  `toml:"rate_limit_window_minutes"` // Minutes window for counting attempts (default: 2)
	PasswordMinLength      int  `toml:"password_min_length"`       // Minimum password length (default: 8)
	PasswordRequireUpper   bool `toml:"password_require_upper"`    // Require uppercase letter (default: false)
	PasswordRequireLower   bool `toml:"password_require_lower"`    // Require lowercase letter (default: false)
	PasswordRequireNumber  bool `toml:"password_require_number"`   // Require number (default: false)
	PasswordRequireSpecial bool `toml:"password_require_special"`  // Require special character (default: false)
}

// TLSConfigTOML holds TLS configuration from TOML
type TLSConfigTOML struct {
	Mode        string            `toml:"mode"`
	Domain      string            `toml:"domain"`
	CertPath    string            `toml:"cert_path"`
	KeyPath     string            `toml:"key_path"`
	LetsEncrypt LetsEncryptConfig `toml:"letsencrypt"`
}

// TenancyConfig holds flags for tenancy feature rollout
type TenancyConfig struct {
	Enabled bool `toml:"enabled"`
}

// SMTPConfig holds SMTP settings for outgoing mail
type SMTPConfig struct {
	Enabled    bool   `toml:"enabled"`
	Host       string `toml:"host"`
	Port       int    `toml:"port"`
	User       string `toml:"user"`
	Pass       string `toml:"pass"`
	From       string `toml:"from"`
	EmailTheme string `toml:"email_theme"` // "dark", "light", or "auto" (default)
}

// LetsEncryptConfig holds Let's Encrypt specific settings
type LetsEncryptConfig struct {
	Domain    string `toml:"domain"`
	Email     string `toml:"email"`
	CacheDir  string `toml:"cache_dir"`
	AcceptTOS bool   `toml:"accept_tos"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			HTTPPort:            9090,
			HTTPSPort:           9443,
			BehindProxy:         false,
			ProxyUseHTTPS:       false,     // Default to HTTP when behind proxy
			BindAddress:         "0.0.0.0", // Bind to all interfaces by default
			AutoApproveAgents:   false,
			AgentTimeoutMinutes: 15,
			SelfUpdateEnabled:   true,
		},
		Security: SecurityConfig{
			RateLimitEnabled:       true, // Enable rate limiting by default
			RateLimitMaxAttempts:   5,    // 5 failed attempts
			RateLimitBlockMinutes:  5,    // Block for 5 minutes
			RateLimitWindowMinutes: 2,    // Within a 2 minute window
			PasswordMinLength:      8,    // Minimum 8 characters
			PasswordRequireUpper:   false,
			PasswordRequireLower:   false,
			PasswordRequireNumber:  false,
			PasswordRequireSpecial: false,
		},
		TLS: TLSConfigTOML{
			Mode:   "self-signed",
			Domain: "localhost",
			LetsEncrypt: LetsEncryptConfig{
				CacheDir:  "letsencrypt-cache",
				AcceptTOS: false,
			},
		},
		Database: config.DatabaseConfig{
			Path: "", // Empty = use platform default (ProgramData on Windows)
		},
		Logging: config.LoggingConfig{
			Level: "info",
		},
		Tenancy: TenancyConfig{
			Enabled: true,
		},
		SMTP: SMTPConfig{
			Enabled:    false,
			Host:       "",
			Port:       587,
			User:       "",
			Pass:       "",
			From:       "",
			EmailTheme: "auto",
		},
		Notifications: NotificationsConfig{
			Enabled:              false,
			AdminEmails:          nil,
			NotifyOnCritical:     true,
			NotifyOnWarning:      false,
			DailySummaryEnabled:  false,
			DailySummaryTime:     "08:00",
			DailySummaryTimezone: "Local",
		},
		Releases: ReleasesConfig{
			MaxReleases:         6,
			PollIntervalMinutes: 240,
			RetentionVersions:   6,  // Keep 6 versions per component; 0 = keep all (no pruning)
			IncludePrerelease:   "", // Auto-detect: dev builds include prereleases, release builds don't
		},
		SelfUpdate: SelfUpdateConfig{
			Channel:              "stable",
			MaxArtifacts:         12,
			CheckIntervalMinutes: 360,
		},
	}
}

// LoadConfig loads configuration from TOML file with environment variable overrides.
// Returns the config and a tracker indicating which keys were set by environment variables.
func LoadConfig(configPath string) (*Config, *ConfigSourceTracker, error) {
	cfg := DefaultConfig()
	tracker := newConfigSourceTracker()

	// Load from file if it exists
	if _, err := os.Stat(configPath); err == nil {
		if err := config.LoadTOML(configPath, cfg); err != nil {
			return nil, nil, err
		}
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg, tracker)

	return cfg, tracker, nil
}

// applyEnvOverrides applies environment variable overrides to the config
// and tracks which keys were set. This is separated out so it can be called
// both when loading a config file and when using defaults (no config file).
func applyEnvOverrides(cfg *Config, tracker *ConfigSourceTracker) {
	// Override with environment variables and track which keys were set
	if val := os.Getenv("SERVER_HTTP_PORT"); val != "" {
		var port int
		if _, err := fmt.Sscanf(val, "%d", &port); err == nil {
			cfg.Server.HTTPPort = port
			tracker.EnvKeys["server.http_port"] = true
		}
	}
	if val := os.Getenv("SERVER_HTTPS_PORT"); val != "" {
		var port int
		if _, err := fmt.Sscanf(val, "%d", &port); err == nil {
			cfg.Server.HTTPSPort = port
			tracker.EnvKeys["server.https_port"] = true
		}
	}
	if val := os.Getenv("BEHIND_PROXY"); val != "" {
		cfg.Server.BehindProxy = val == "true" || val == "1"
		tracker.EnvKeys["server.behind_proxy"] = true
	}
	if val := os.Getenv("TRUSTED_PROXIES"); val != "" {
		cfg.Server.TrustedProxies = parseStringListEnv(val)
		tracker.EnvKeys["server.trusted_proxies"] = true
	}
	if val := os.Getenv("PROXY_USE_HTTPS"); val != "" {
		cfg.Server.ProxyUseHTTPS = val == "true" || val == "1"
		tracker.EnvKeys["server.proxy_use_https"] = true
	}
	if val := os.Getenv("BIND_ADDRESS"); val != "" {
		cfg.Server.BindAddress = val
		tracker.EnvKeys["server.bind_address"] = true
	}
	if val := os.Getenv("AUTO_APPROVE_AGENTS"); val != "" {
		cfg.Server.AutoApproveAgents = val == "true" || val == "1"
		tracker.EnvKeys["server.auto_approve_agents"] = true
	}
	if val := os.Getenv("AGENT_TIMEOUT_MINUTES"); val != "" {
		var v int
		if _, err := fmt.Sscanf(val, "%d", &v); err == nil {
			cfg.Server.AgentTimeoutMinutes = v
			tracker.EnvKeys["server.agent_timeout_minutes"] = true
		}
	}
	if val := os.Getenv("SERVER_SELF_UPDATE_ENABLED"); val != "" {
		cfg.Server.SelfUpdateEnabled = val == "true" || val == "1"
		tracker.EnvKeys["server.self_update_enabled"] = true
	}
	if val := os.Getenv("RELEASES_MAX_RELEASES"); val != "" {
		var v int
		if _, err := fmt.Sscanf(val, "%d", &v); err == nil {
			cfg.Releases.MaxReleases = v
			tracker.EnvKeys["releases.max_releases"] = true
		}
	}
	if val := os.Getenv("RELEASES_POLL_INTERVAL_MINUTES"); val != "" {
		var v int
		if _, err := fmt.Sscanf(val, "%d", &v); err == nil {
			cfg.Releases.PollIntervalMinutes = v
			tracker.EnvKeys["releases.poll_interval_minutes"] = true
		}
	}
	if val := os.Getenv("RELEASES_RETENTION_VERSIONS"); val != "" {
		var v int
		if _, err := fmt.Sscanf(val, "%d", &v); err == nil && v >= 0 {
			cfg.Releases.RetentionVersions = v
			tracker.EnvKeys["releases.retention_versions"] = true
		}
	}
	if val := os.Getenv("RELEASES_INCLUDE_PRERELEASE"); val != "" {
		cfg.Releases.IncludePrerelease = val
		tracker.EnvKeys["releases.include_prerelease"] = true
	}
	if val := os.Getenv("SELF_UPDATE_CHANNEL"); val != "" {
		cfg.SelfUpdate.Channel = val
		tracker.EnvKeys["self_update.channel"] = true
	}
	if val := os.Getenv("SELF_UPDATE_MAX_ARTIFACTS"); val != "" {
		var v int
		if _, err := fmt.Sscanf(val, "%d", &v); err == nil {
			cfg.SelfUpdate.MaxArtifacts = v
			tracker.EnvKeys["self_update.max_artifacts"] = true
		}
	}
	if val := os.Getenv("SELF_UPDATE_CHECK_INTERVAL_MINUTES"); val != "" {
		var v int
		if _, err := fmt.Sscanf(val, "%d", &v); err == nil {
			cfg.SelfUpdate.CheckIntervalMinutes = v
			tracker.EnvKeys["self_update.check_interval_minutes"] = true
		}
	}
	if val := os.Getenv("TLS_MODE"); val != "" {
		cfg.TLS.Mode = val
		tracker.EnvKeys["tls.mode"] = true
	}
	if val := os.Getenv("TLS_CERT_PATH"); val != "" {
		cfg.TLS.CertPath = val
		tracker.EnvKeys["tls.cert_path"] = true
	}
	if val := os.Getenv("TLS_KEY_PATH"); val != "" {
		cfg.TLS.KeyPath = val
		tracker.EnvKeys["tls.key_path"] = true
	}
	if val := os.Getenv("LETSENCRYPT_DOMAIN"); val != "" {
		cfg.TLS.LetsEncrypt.Domain = val
		tracker.EnvKeys["tls.letsencrypt.domain"] = true
	}
	if val := os.Getenv("LETSENCRYPT_EMAIL"); val != "" {
		cfg.TLS.LetsEncrypt.Email = val
		tracker.EnvKeys["tls.letsencrypt.email"] = true
	}
	if val := os.Getenv("LETSENCRYPT_ACCEPT_TOS"); val != "" {
		cfg.TLS.LetsEncrypt.AcceptTOS = val == "true" || val == "1"
		tracker.EnvKeys["tls.letsencrypt.accept_tos"] = true
	}

	// SMTP env overrides
	if val := os.Getenv("SMTP_ENABLED"); val != "" {
		cfg.SMTP.Enabled = val == "true" || val == "1"
		tracker.EnvKeys["smtp.enabled"] = true
	}
	if val := os.Getenv("SMTP_HOST"); val != "" {
		cfg.SMTP.Host = val
		tracker.EnvKeys["smtp.host"] = true
	}
	if val := os.Getenv("SMTP_PORT"); val != "" {
		var p int
		if _, err := fmt.Sscanf(val, "%d", &p); err == nil {
			cfg.SMTP.Port = p
			tracker.EnvKeys["smtp.port"] = true
		}
	}
	if val := os.Getenv("SMTP_USER"); val != "" {
		cfg.SMTP.User = val
		tracker.EnvKeys["smtp.user"] = true
	}
	if val := os.Getenv("SMTP_PASS"); val != "" {
		cfg.SMTP.Pass = val
		tracker.EnvKeys["smtp.pass"] = true
	}
	if val := os.Getenv("SMTP_FROM"); val != "" {
		cfg.SMTP.From = val
		tracker.EnvKeys["smtp.from"] = true
	}
	if val := os.Getenv("SMTP_EMAIL_THEME"); val != "" {
		cfg.SMTP.EmailTheme = val
		tracker.EnvKeys["smtp.email_theme"] = true
	}

	// Admin notification env overrides
	if val := os.Getenv("NOTIFICATIONS_ENABLED"); val != "" {
		cfg.Notifications.Enabled = val == "true" || val == "1"
		tracker.EnvKeys["notifications.enabled"] = true
	}
	if val := os.Getenv("NOTIFICATIONS_ADMIN_EMAILS"); val != "" {
		cfg.Notifications.AdminEmails = parseStringListEnv(val)
		tracker.EnvKeys["notifications.admin_emails"] = true
	}
	if val := os.Getenv("NOTIFICATIONS_NOTIFY_ON_CRITICAL"); val != "" {
		cfg.Notifications.NotifyOnCritical = val == "true" || val == "1"
		tracker.EnvKeys["notifications.notify_on_critical"] = true
	}
	if val := os.Getenv("NOTIFICATIONS_NOTIFY_ON_WARNING"); val != "" {
		cfg.Notifications.NotifyOnWarning = val == "true" || val == "1"
		tracker.EnvKeys["notifications.notify_on_warning"] = true
	}
	if val := os.Getenv("NOTIFICATIONS_DAILY_SUMMARY_ENABLED"); val != "" {
		cfg.Notifications.DailySummaryEnabled = val == "true" || val == "1"
		tracker.EnvKeys["notifications.daily_summary_enabled"] = true
	}
	if val := os.Getenv("NOTIFICATIONS_DAILY_SUMMARY_TIME"); val != "" {
		cfg.Notifications.DailySummaryTime = val
		tracker.EnvKeys["notifications.daily_summary_time"] = true
	}
	if val := os.Getenv("NOTIFICATIONS_DAILY_SUMMARY_TIMEZONE"); val != "" {
		cfg.Notifications.DailySummaryTimezone = val
		tracker.EnvKeys["notifications.daily_summary_timezone"] = true
	}

	// Logging env overrides with tracking (check prefixed first, then generic)
	if val := os.Getenv("SERVER_LOG_LEVEL"); val != "" {
		cfg.Logging.Level = strings.ToLower(val)
		tracker.EnvKeys["logging.level"] = true
	} else if val := os.Getenv("LOG_LEVEL"); val != "" {
		cfg.Logging.Level = strings.ToLower(val)
		tracker.EnvKeys["logging.level"] = true
	}

	// Apply common environment variable overrides for database (component-specific prefixed env var supported)
	config.ApplyDatabaseEnvOverrides(&cfg.Database, "SERVER")
}

func parseStringListEnv(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// Support JSON arrays like ["nginx", "172.16.0.0/12"]
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			out := make([]string, 0, len(arr))
			for _, v := range arr {
				v = strings.TrimSpace(v)
				if v != "" {
					out = append(out, v)
				}
			}
			return out
		}
	}

	// Fallback: comma-separated list (optionally with spaces)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ToTLSConfig converts YAML TLS config to TLSConfig
func (c *Config) ToTLSConfig() *TLSConfig {
	mode := TLSModeSelfSigned
	switch c.TLS.Mode {
	case "letsencrypt":
		mode = TLSModeLetsEncrypt
	case "custom":
		mode = TLSModeCustom
	case "self-signed":
		mode = TLSModeSelfSigned
	}

	return &TLSConfig{
		Mode:              mode,
		Domain:            c.TLS.Domain,
		CertPath:          c.TLS.CertPath,
		KeyPath:           c.TLS.KeyPath,
		LetsEncryptDomain: c.TLS.LetsEncrypt.Domain,
		LetsEncryptEmail:  c.TLS.LetsEncrypt.Email,
		LetsEncryptCache:  c.TLS.LetsEncrypt.CacheDir,
		AcceptTOS:         c.TLS.LetsEncrypt.AcceptTOS,
		HTTPPort:          c.Server.HTTPPort,
		HTTPSPort:         c.Server.HTTPSPort,
		BehindProxy:       c.Server.BehindProxy,
		ProxyUseHTTPS:     c.Server.ProxyUseHTTPS,
		BindAddress:       c.Server.BindAddress,
	}
}

// ValidatePassword checks if a password meets the configured requirements.
// Returns nil if valid, or an error describing what's missing.
func (c *SecurityConfig) ValidatePassword(password string) error {
	if c == nil {
		return nil
	}
	minLen := c.PasswordMinLength
	if minLen <= 0 {
		minLen = 8
	}
	if len(password) < minLen {
		return fmt.Errorf("password must be at least %d characters", minLen)
	}
	if c.PasswordRequireUpper {
		hasUpper := false
		for _, r := range password {
			if r >= 'A' && r <= 'Z' {
				hasUpper = true
				break
			}
		}
		if !hasUpper {
			return fmt.Errorf("password must contain at least one uppercase letter")
		}
	}
	if c.PasswordRequireLower {
		hasLower := false
		for _, r := range password {
			if r >= 'a' && r <= 'z' {
				hasLower = true
				break
			}
		}
		if !hasLower {
			return fmt.Errorf("password must contain at least one lowercase letter")
		}
	}
	if c.PasswordRequireNumber {
		hasNumber := false
		for _, r := range password {
			if r >= '0' && r <= '9' {
				hasNumber = true
				break
			}
		}
		if !hasNumber {
			return fmt.Errorf("password must contain at least one number")
		}
	}
	if c.PasswordRequireSpecial {
		hasSpecial := false
		for _, r := range password {
			if (r >= '!' && r <= '/') || (r >= ':' && r <= '@') || (r >= '[' && r <= '`') || (r >= '{' && r <= '~') {
				hasSpecial = true
				break
			}
		}
		if !hasSpecial {
			return fmt.Errorf("password must contain at least one special character")
		}
	}
	return nil
}

// PasswordRequirements returns a human-readable description of password requirements.
func (c *SecurityConfig) PasswordRequirements() string {
	if c == nil {
		return "Minimum 8 characters"
	}
	minLen := c.PasswordMinLength
	if minLen <= 0 {
		minLen = 8
	}
	parts := []string{fmt.Sprintf("Minimum %d characters", minLen)}
	if c.PasswordRequireUpper {
		parts = append(parts, "uppercase letter")
	}
	if c.PasswordRequireLower {
		parts = append(parts, "lowercase letter")
	}
	if c.PasswordRequireNumber {
		parts = append(parts, "number")
	}
	if c.PasswordRequireSpecial {
		parts = append(parts, "special character")
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return parts[0] + ", requires: " + strings.Join(parts[1:], ", ")
}

// WriteDefaultConfig writes a default configuration file
func WriteDefaultConfig(configPath string) error {
	cfg := DefaultConfig()
	return config.WriteDefaultTOML(configPath, cfg)
}
