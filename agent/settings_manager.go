package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/storage"
	pmsettings "printmaster/common/settings"
)

const serverManagedSettingsKey = "server_managed_settings"

type serverManagedSettings struct {
	Version         string              `json:"version"`
	SchemaVersion   string              `json:"schema_version"`
	UpdatedAt       time.Time           `json:"updated_at"`
	ManagedSections []string            `json:"managed_sections"`
	Settings        pmsettings.Settings `json:"settings"`
}

// SettingsManager tracks server-managed snapshots and composes effective configs.
type SettingsManager struct {
	store   storage.AgentConfigStore
	mu      sync.RWMutex
	managed *serverManagedSettings
}

func NewSettingsManager(store storage.AgentConfigStore) *SettingsManager {
	mgr := &SettingsManager{store: store}
	mgr.reload()
	return mgr
}

func (m *SettingsManager) reload() {
	if m == nil || m.store == nil {
		return
	}
	var payload serverManagedSettings
	if err := m.store.GetConfigValue(serverManagedSettingsKey, &payload); err != nil {
		return
	}
	if strings.TrimSpace(payload.Version) == "" {
		return
	}
	pmsettings.Sanitize(&payload.Settings)
	if payload.ManagedSections == nil {
		payload.ManagedSections = []string{"discovery", "snmp", "features", "spooler"}
	}
	m.mu.Lock()
	m.managed = &payload
	m.mu.Unlock()
}

func (m *SettingsManager) CurrentVersion() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.managed == nil {
		return ""
	}
	return m.managed.Version
}

func (m *SettingsManager) HasManagedSnapshot() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.managed != nil
}

func (m *SettingsManager) ManagedSections() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.managed == nil {
		return nil
	}
	return append([]string{}, m.managed.ManagedSections...)
}

func (m *SettingsManager) baseSettings() (pmsettings.Settings, bool) {
	if m == nil {
		return pmsettings.DefaultSettings(), false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.managed == nil {
		return pmsettings.DefaultSettings(), false
	}
	return m.managed.Settings, true
}

func (m *SettingsManager) snapshot() *serverManagedSettings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.managed == nil {
		return nil
	}
	snapshot := *m.managed
	snapshot.ManagedSections = append([]string{}, m.managed.ManagedSections...)
	return &snapshot
}

func (m *SettingsManager) ApplyServerSnapshot(snapshot *agent.SettingsSnapshot) (pmsettings.Settings, error) {
	if m == nil || m.store == nil {
		return pmsettings.Settings{}, fmt.Errorf("settings manager unavailable")
	}
	if snapshot == nil || strings.TrimSpace(snapshot.Version) == "" {
		return pmsettings.Settings{}, fmt.Errorf("invalid snapshot")
	}
	payload := serverManagedSettings{
		Version:         snapshot.Version,
		SchemaVersion:   snapshot.SchemaVersion,
		UpdatedAt:       snapshot.UpdatedAt,
		ManagedSections: append([]string{}, snapshot.ManagedSections...),
		Settings:        snapshot.Settings,
	}
	pmsettings.Sanitize(&payload.Settings)
	if snapshot.ManagedSections == nil {
		payload.ManagedSections = []string{"discovery", "snmp", "features", "spooler"}
	}
	if err := m.store.SetConfigValue(serverManagedSettingsKey, payload); err != nil {
		return pmsettings.Settings{}, err
	}
	m.mu.Lock()
	m.managed = &payload
	m.mu.Unlock()
	return loadUnifiedSettings(m.store), nil
}

// ClearManagedSnapshot removes the server-managed settings snapshot, unlocking
// all settings for local editing. This should be called when disconnecting from
// the server to allow standalone operation without locked settings.
func (m *SettingsManager) ClearManagedSnapshot() error {
	if m == nil || m.store == nil {
		return nil
	}
	// Delete the server managed settings key from storage
	if err := m.store.DeleteConfigValue(serverManagedSettingsKey); err != nil {
		return fmt.Errorf("failed to clear managed settings: %w", err)
	}
	m.mu.Lock()
	m.managed = nil
	m.mu.Unlock()
	return nil
}
