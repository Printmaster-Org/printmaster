package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

var agentLocalDefaults = DefaultSettings()

// StripAgentLocalFields resets agent-local fields to their defaults so server-managed
// snapshots do not attempt to override runtime-only values.
func StripAgentLocalFields(cfg *Settings) {
	if cfg == nil {
		return
	}
	// Logging section is agent-local
	cfg.Logging = agentLocalDefaults.Logging
	// Web section is agent-local
	cfg.Web = agentLocalDefaults.Web
	cfg.Discovery.ShowDiscoverButtonAnyway = agentLocalDefaults.Discovery.ShowDiscoverButtonAnyway
	cfg.Discovery.ShowDiscoveredDevicesAnyway = agentLocalDefaults.Discovery.ShowDiscoveredDevicesAnyway
	cfg.Discovery.DetectedSubnet = agentLocalDefaults.Discovery.DetectedSubnet
}

// CopyAgentLocalFields copies agent-local fields from src into dst.
func CopyAgentLocalFields(src Settings, dst *Settings) {
	if dst == nil {
		return
	}
	// Logging section is agent-local
	dst.Logging = src.Logging
	// Web section is agent-local
	dst.Web = src.Web
	dst.Discovery.ShowDiscoverButtonAnyway = src.Discovery.ShowDiscoverButtonAnyway
	dst.Discovery.ShowDiscoveredDevicesAnyway = src.Discovery.ShowDiscoveredDevicesAnyway
	dst.Discovery.DetectedSubnet = src.Discovery.DetectedSubnet
}

// ComputeSettingsVersion hashes the schema version, update timestamp, and settings payload
// to produce a deterministic change token for sync.
func ComputeSettingsVersion(schemaVersion string, updatedAt time.Time, cfg Settings) (string, error) {
	return ComputeSettingsVersionWithManagedSections(schemaVersion, updatedAt, cfg, nil)
}

// ComputeSettingsVersionWithManagedSections includes section ownership in the
// sync token so metadata-only fleet changes are delivered to agents.
func ComputeSettingsVersionWithManagedSections(schemaVersion string, updatedAt time.Time, cfg Settings, managedSections []string) (string, error) {
	material := struct {
		SchemaVersion   string    `json:"schema_version"`
		UpdatedAt       time.Time `json:"updated_at"`
		ManagedSections []string  `json:"managed_sections,omitempty"`
		Settings        Settings  `json:"settings"`
	}{
		SchemaVersion:   schemaVersion,
		UpdatedAt:       updatedAt.UTC(),
		ManagedSections: managedSections,
		Settings:        cfg,
	}
	b, err := json.Marshal(material)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
