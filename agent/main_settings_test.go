package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	agentpkg "printmaster/agent/agent"
	pmsettings "printmaster/common/settings"
)

func TestLoadUnifiedSettingsHonorsEverySectionOwnershipCombination(t *testing.T) {
	store := newFakeConfigStore()
	local := pmsettings.DefaultSettings()
	local.Discovery.Concurrency = 23
	local.Discovery.RangesText = "198.51.100.0/24"
	local.Discovery.ShowDiscoverButtonAnyway = true
	local.Discovery.ShowDiscoveredDevicesAnyway = true
	local.SNMP.TimeoutMS = 4321
	local.Features.AgentUpdateChannel = "beta"
	local.Spooler.PollIntervalSeconds = 45
	local.Logging.Level = "debug"
	local.Web.HTTPPort = "8181"
	store.values["discovery_settings"] = structToMap(local.Discovery)
	store.ranges = local.Discovery.RangesText
	store.values["settings"] = structToMap(local)
	mgr := NewSettingsManager(store)
	prev := settingsManager
	settingsManager = mgr
	t.Cleanup(func() { settingsManager = prev })

	server := pmsettings.DefaultSettings()
	server.Discovery.Concurrency = 71
	server.Discovery.RangesText = "192.0.2.0/24"
	server.SNMP.TimeoutMS = 7654
	server.Features.AgentUpdateChannel = "dev"
	server.Spooler.PollIntervalSeconds = 90
	sections := []string{"discovery", "snmp", "features", "spooler"}
	for mask := 0; mask < 16; mask++ {
		t.Run(fmt.Sprintf("mask-%d", mask), func(t *testing.T) {
			managed := []string{}
			for i, section := range sections {
				if mask&(1<<i) != 0 {
					managed = append(managed, section)
				}
			}
			snapshot := &agentpkg.SettingsSnapshot{Version: fmt.Sprintf("v-%d", mask), Settings: server, ManagedSections: managed}
			got, err := mgr.ApplyServerSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			want := local
			if mask&1 != 0 {
				want.Discovery = server.Discovery
				want.Discovery.ShowDiscoverButtonAnyway = true
				want.Discovery.ShowDiscoveredDevicesAnyway = true
			}
			if mask&2 != 0 {
				want.SNMP = server.SNMP
			}
			if mask&4 != 0 {
				want.Features = server.Features
			}
			if mask&8 != 0 {
				want.Spooler = server.Spooler
			}
			want.Discovery.DetectedSubnet = got.Discovery.DetectedSubnet
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("settings ownership mismatch:\ngot %+v\nwant %+v", got, want)
			}
			settingsManager = NewSettingsManager(store)
			if reloaded := loadUnifiedSettings(store); !reflect.DeepEqual(reloaded, got) {
				t.Fatalf("ownership changed on restart: got %+v want %+v", reloaded, got)
			}
			mgr = settingsManager
		})
	}
}

func TestDiscoveryManagedChangesAndRanges(t *testing.T) {
	if hasFleetDiscoveryChanges(map[string]interface{}{"show_discover_button_anyway": true, "show_discovered_devices_anyway": false}) {
		t.Fatal("local display preferences must remain editable")
	}
	if !hasFleetDiscoveryChanges(map[string]interface{}{"ip_scanning_enabled": false}) {
		t.Fatal("fleet discovery changes must be locked")
	}
	cfg := pmsettings.DefaultSettings().Discovery
	cfg.RangesText = "192.0.2.1\n\n 192.0.2.0/24 "
	if len(discoveryRanges(cfg)) != 0 {
		t.Fatal("disabled manual ranges must not be scanned")
	}
	cfg.ManualRanges = true
	if got := discoveryRanges(cfg); !reflect.DeepEqual(got, []string{"192.0.2.1", "192.0.2.0/24"}) {
		t.Fatalf("unexpected effective ranges: %v", got)
	}
}

func TestLoadUnifiedSettingsRetainsDiscoveryToggles(t *testing.T) {
	store := newFakeConfigStore()
	store.values["discovery_settings"] = map[string]interface{}{
		"auto_discover_enabled":          true,
		"autosave_discovered_devices":    true,
		"show_discover_button_anyway":    true,
		"show_discovered_devices_anyway": true,
		"passive_discovery_enabled":      true,
	}

	snapshot := loadUnifiedSettings(store)
	discMap := structToMap(snapshot.Discovery)

	expectations := map[string]bool{
		"auto_discover_enabled":          true,
		"autosave_discovered_devices":    true,
		"show_discover_button_anyway":    true,
		"show_discovered_devices_anyway": true,
		"passive_discovery_enabled":      true,
	}

	for key, want := range expectations {
		got, ok := discMap[key]
		if !ok {
			t.Fatalf("expected discovery key %s to be present", key)
		}
		gotBool, _ := got.(bool)
		if gotBool != want {
			t.Fatalf("unexpected value for %s: got %v want %v", key, gotBool, want)
		}
	}
}

func TestLoadUnifiedSettingsUsesManagedSnapshotButAllowsLocalLoggingOverrides(t *testing.T) {
	store := newFakeConfigStore()
	mgr := NewSettingsManager(store)
	prev := settingsManager
	settingsManager = mgr
	t.Cleanup(func() { settingsManager = prev })

	snap := &agentpkg.SettingsSnapshot{
		Version:       "abc",
		SchemaVersion: "schema-1",
		UpdatedAt:     time.Unix(300, 0),
		Settings:      pmsettings.DefaultSettings(),
	}
	snap.Settings.SNMP.TimeoutMS = 3333
	snap.Settings.Logging.Level = "warn"

	if _, err := mgr.ApplyServerSnapshot(snap); err != nil {
		t.Fatalf("apply snapshot failed: %v", err)
	}

	store.values["settings"] = map[string]interface{}{
		"logging": map[string]interface{}{
			"level": "debug",
		},
		"snmp": map[string]interface{}{
			"timeout_ms": 1234,
		},
	}

	result := loadUnifiedSettings(store)
	if result.SNMP.TimeoutMS != 3333 {
		t.Fatalf("expected server-managed value to persist, got %d", result.SNMP.TimeoutMS)
	}
	if result.Logging.Level != "debug" {
		t.Fatalf("expected local logging overrides to apply, got %s", result.Logging.Level)
	}
}
