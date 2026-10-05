package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"printmaster/common/config"
	"printmaster/common/updatepolicy"
)

// AgentConfig represents the agent configuration
type AgentConfig struct {
	AssetIDRegex           string                 `toml:"asset_id_regex"`
	Concurrency            int                    `toml:"discovery_concurrency"`
	SNMP                   SNMPConfig             `toml:"snmp"`
	Server                 ServerConnectionConfig `toml:"server"`
	AutoUpdate             AutoUpdateConfig       `toml:"auto_update"`
	Database               config.DatabaseConfig  `toml:"database"`
	Logging                config.LoggingConfig   `toml:"logging"`
	Web                    WebConfig              `toml:"web"`
	EpsonRemoteModeEnabled bool                   `toml:"epson_remote_mode_enabled"`
}

// SNMPConfig holds SNMP client settings
type SNMPConfig struct {
	// Version specifies the SNMP protocol version: "1", "2c", or "3"
	Version string `toml:"version"`
	// Community is the community string for SNMPv1/v2c
	Community string `toml:"community"`
	// TimeoutMs is the SNMP timeout in milliseconds
	TimeoutMs int `toml:"timeout_ms"`
	// Retries is the number of retry attempts for failed queries
	Retries int `toml:"retries"`

	// SNMPv3 security parameters
	// SecurityLevel: "noAuthNoPriv", "authNoPriv", or "authPriv"
	SecurityLevel string `toml:"security_level"`
	// Username is the SNMPv3 security name (USM user)
	Username string `toml:"username"`
	// AuthProtocol: "MD5", "SHA", "SHA224", "SHA256", "SHA384", "SHA512"
	AuthProtocol string `toml:"auth_protocol"`
	// AuthPassword is the authentication passphrase
	AuthPassword string `toml:"auth_password"`
	// PrivProtocol: "DES", "AES", "AES192", "AES256"
	PrivProtocol string `toml:"priv_protocol"`
	// PrivPassword is the privacy passphrase
	PrivPassword string `toml:"priv_password"`
	// ContextName is the SNMPv3 context name (optional)
	ContextName string `toml:"context_name"`
}

// ServerConnectionConfig holds server registration and upload settings
type ServerConnectionConfig struct {
	Enabled            bool   `toml:"enabled"`
	URL                string `toml:"url"`
	Name               string `toml:"name"` // User-friendly name (configurable)
	CAPath             string `toml:"ca_path"`
	InsecureSkipVerify bool   `toml:"insecure_skip_verify"` // Skip TLS verification (dev/testing only)
	UploadInterval     int    `toml:"upload_interval_seconds"`
	HeartbeatInterval  int    `toml:"heartbeat_interval_seconds"`
	Token              string `toml:"token"`    // Stored after registration
	AgentID            string `toml:"agent_id"` // Stable UUID (auto-generated, do not edit)
}

// AutoUpdateConfig captures agent-side override preferences that determine how
// it should behave relative to the fleet's policy.
type AutoUpdateConfig struct {
	Mode        updatepolicy.AgentOverrideMode `toml:"mode"`
	Channel     string                         `toml:"channel"` // Update channel: "stable", "beta", "dev", or "" (auto-detect from build type)
	LocalPolicy updatepolicy.PolicySpec        `toml:"local_policy"`
}

// WebConfig holds web UI settings
type WebConfig struct {
	HTTPPort  int           `toml:"http_port"`
	HTTPSPort int           `toml:"https_port"`
	EnableTLS bool          `toml:"enable_tls"`
	Auth      WebAuthConfig `toml:"auth"`
}

// WebAuthConfig controls agent UI authentication behavior
// Mode:
//
//	"local"    -> only local bypass (loopback treated as admin if allow_local_admin=true)
//	"server"   -> expects server-auth callback flow (future implementation)
//	"disabled" -> no auth at all (legacy behavior)
//
// AllowLocalAdmin: if true, loopback requests get admin principal without login
type WebAuthConfig struct {
	Mode            string `toml:"mode"`
	AllowLocalAdmin bool   `toml:"allow_local_admin"`
}

// DefaultAgentConfig returns agent configuration with sensible defaults
func DefaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		AssetIDRegex:           `\b\d{5}\b`,
		Concurrency:            50,
		EpsonRemoteModeEnabled: false,
		SNMP: SNMPConfig{
			Version:       "2c",
			Community:     "public",
			TimeoutMs:     2000,
			Retries:       1,
			SecurityLevel: "",
			Username:      "",
			AuthProtocol:  "",
			AuthPassword:  "",
			PrivProtocol:  "",
			PrivPassword:  "",
			ContextName:   "",
		},
		Server: ServerConnectionConfig{
			Enabled:            false,
			URL:                "",
			Name:               "",
			CAPath:             "",
			InsecureSkipVerify: false,
			UploadInterval:     300,
			HeartbeatInterval:  60,
			Token:              "",
			AgentID:            "", // Will be auto-generated on first run
		},
		AutoUpdate: defaultAutoUpdateConfig(),
		Database: config.DatabaseConfig{
			Path: "", // Will use default platform-specific path
		},
		Logging: config.LoggingConfig{
			Level: "info",
		},
		Web: WebConfig{
			HTTPPort:  8080,
			HTTPSPort: 8443,
			EnableTLS: false,
			Auth:      WebAuthConfig{Mode: "local", AllowLocalAdmin: true},
		},
	}
}

// LoadAgentConfig loads configuration from TOML file with environment variable overrides.
// Returns an error if the config file does not exist or cannot be parsed.
func LoadAgentConfig(configPath string) (*AgentConfig, error) {
	cfg := DefaultAgentConfig()

	// File must exist - return error if missing
	if _, err := os.Stat(configPath); err != nil {
		return nil, err
	}
	if err := config.LoadTOML(configPath, cfg); err != nil {
		return nil, err
	}

	ApplyEnvironmentOverrides(cfg)
	return cfg, nil
}

// ApplyEnvironmentOverrides applies environment variable overrides to the config.
// This is called automatically by LoadAgentConfig, but can also be called separately
// when using DefaultAgentConfig() without a config file.
func ApplyEnvironmentOverrides(cfg *AgentConfig) {
	if cfg == nil {
		return
	}

	// Override with environment variables
	if val := os.Getenv("ASSET_ID_REGEX"); val != "" {
		cfg.AssetIDRegex = val
	}
	if val := os.Getenv("DISCOVERY_CONCURRENCY"); val != "" {
		if concurrency, err := strconv.Atoi(val); err == nil {
			cfg.Concurrency = concurrency
		}
	}
	// SNMP basic settings
	if val := os.Getenv("SNMP_VERSION"); val != "" {
		cfg.SNMP.Version = val
	}
	if val := os.Getenv("SNMP_COMMUNITY"); val != "" {
		cfg.SNMP.Community = val
	}
	if val := os.Getenv("SNMP_TIMEOUT_MS"); val != "" {
		if timeout, err := strconv.Atoi(val); err == nil {
			cfg.SNMP.TimeoutMs = timeout
		}
	}
	if val := os.Getenv("SNMP_RETRIES"); val != "" {
		if retries, err := strconv.Atoi(val); err == nil {
			cfg.SNMP.Retries = retries
		}
	}
	// SNMPv3 settings
	if val := os.Getenv("SNMP_SECURITY_LEVEL"); val != "" {
		cfg.SNMP.SecurityLevel = val
	}
	if val := os.Getenv("SNMP_USERNAME"); val != "" {
		cfg.SNMP.Username = val
	}
	if val := os.Getenv("SNMP_AUTH_PROTOCOL"); val != "" {
		cfg.SNMP.AuthProtocol = val
	}
	if val := os.Getenv("SNMP_AUTH_PASSWORD"); val != "" {
		cfg.SNMP.AuthPassword = val
	}
	if val := os.Getenv("SNMP_PRIV_PROTOCOL"); val != "" {
		cfg.SNMP.PrivProtocol = val
	}
	if val := os.Getenv("SNMP_PRIV_PASSWORD"); val != "" {
		cfg.SNMP.PrivPassword = val
	}
	if val := os.Getenv("SNMP_CONTEXT_NAME"); val != "" {
		cfg.SNMP.ContextName = val
	}
	if val := os.Getenv("SERVER_ENABLED"); val != "" {
		cfg.Server.Enabled = val == "true" || val == "1"
	}
	if val := os.Getenv("SERVER_URL"); val != "" {
		cfg.Server.URL = val
		// Auto-enable server mode when SERVER_URL is provided (Docker Compose scenario)
		if os.Getenv("SERVER_ENABLED") == "" {
			cfg.Server.Enabled = true
		}
	}
	if val := os.Getenv("AGENT_NAME"); val != "" {
		cfg.Server.Name = val
	}
	if val := os.Getenv("AGENT_ID"); val != "" {
		cfg.Server.AgentID = val
	}
	if val := os.Getenv("SERVER_CA_PATH"); val != "" {
		cfg.Server.CAPath = val
	}
	if val := os.Getenv("SERVER_INSECURE_SKIP_VERIFY"); val != "" {
		// Accept common true-ish values
		lower := strings.ToLower(val)
		cfg.Server.InsecureSkipVerify = (lower == "1" || lower == "true" || lower == "yes")
	}
	if val := os.Getenv("WEB_HTTP_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.Web.HTTPPort = port
		}
	}
	if val := os.Getenv("WEB_HTTPS_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.Web.HTTPSPort = port
		}
	}
	if val := os.Getenv("WEB_AUTH_MODE"); val != "" {
		cfg.Web.Auth.Mode = strings.ToLower(val)
	}
	if val := os.Getenv("WEB_ALLOW_LOCAL_ADMIN"); val != "" {
		lower := strings.ToLower(val)
		cfg.Web.Auth.AllowLocalAdmin = (lower == "1" || lower == "true" || lower == "yes")
	}
	if val := os.Getenv("EPSON_REMOTE_MODE_ENABLED"); val != "" {
		lower := strings.ToLower(val)
		cfg.EpsonRemoteModeEnabled = (lower == "1" || lower == "true" || lower == "yes")
	}
	if val := os.Getenv("AUTO_UPDATE_MODE"); val != "" {
		cfg.AutoUpdate.Mode = normalizeAutoUpdateMode(val)
	}

	// Apply common environment variable overrides (component-specific prefixed env var supported)
	config.ApplyDatabaseEnvOverrides(&cfg.Database, "AGENT")
	config.ApplyLoggingEnvOverrides(&cfg.Logging)

	cfg.AutoUpdate.Mode = normalizeAutoUpdateMode(string(cfg.AutoUpdate.Mode))
}

// WriteDefaultAgentConfig writes a default agent configuration file
func WriteDefaultAgentConfig(configPath string) error {
	cfg := DefaultAgentConfig()
	return config.WriteDefaultTOML(configPath, cfg)
}

// LoadServerToken loads the server authentication token from file
func LoadServerToken(dataDir string) string {
	tokenPath := filepath.Join(dataDir, "agent_token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "" // No token file = not registered yet
	}
	return strings.TrimSpace(string(data))
}

// SaveServerToken saves the server authentication token to file
func SaveServerToken(dataDir, token string) error {
	if token == "" {
		return nil // Don't save empty tokens
	}
	tokenPath := filepath.Join(dataDir, "agent_token")
	// Create directory if needed
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	// Write with restrictive permissions (owner read/write only)
	return os.WriteFile(tokenPath, []byte(token), 0600)
}

// DeleteServerToken removes the persisted server authentication token, if present.
func DeleteServerToken(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("data directory not specified for token removal")
	}
	tokenPath := filepath.Join(dataDir, "agent_token")
	if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadServerJoinToken loads the stored server join token from disk (if any).
func LoadServerJoinToken(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	tokenPath := filepath.Join(dataDir, "server_join_token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveServerJoinToken persists the join token so the agent can re-register later.
func SaveServerJoinToken(dataDir, token string) error {
	if dataDir == "" {
		return fmt.Errorf("data directory not specified for join token storage")
	}
	tokenPath := filepath.Join(dataDir, "server_join_token")
	if token == "" {
		if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(tokenPath, []byte(token), 0600)
}

// LoadOrGenerateAgentID loads the agent ID from file or generates a new UUID
func LoadOrGenerateAgentID(dataDir string) (string, error) {
	idPath := filepath.Join(dataDir, "agent_id")

	// Try to load existing ID
	data, err := os.ReadFile(idPath)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}

	// Generate new UUID
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	// Format as UUID (8-4-4-4-12)
	id := fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])

	// Save for future use
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(idPath, []byte(id), 0600); err != nil {
		return "", err
	}

	return id, nil
}
