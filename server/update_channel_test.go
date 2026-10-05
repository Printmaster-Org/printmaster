package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pmsettings "printmaster/common/settings"
	"printmaster/server/releases"
	serversettings "printmaster/server/settings"
	"printmaster/server/storage"
)

func TestLegacyAgentManifestUsesSavedFleetChannel(t *testing.T) {
	f := newBoundaryFixture(t)
	previousResolver, previousReleases := settingsResolver, releaseManager
	t.Cleanup(func() { settingsResolver, releaseManager = previousResolver, previousReleases })
	var err error
	settingsResolver, err = serversettings.NewResolver(f.store)
	if err != nil {
		t.Fatal(err)
	}
	releaseManager, err = releases.NewManager(f.store, nil, releases.ManagerOptions{ManifestVersion: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	settings := pmsettings.DefaultSettings()
	settings.Features.AgentUpdateChannel = "dev"
	if err := f.store.UpsertGlobalSettings(ctx, &storage.SettingsRecord{Settings: settings, ManagedSections: []string{"features"}}); err != nil {
		t.Fatal(err)
	}
	for _, release := range []struct{ channel, version string }{{"stable", "0.31.1"}, {"dev", "0.31.2-dev.1"}, {"beta", "0.31.2-beta.1"}} {
		artifact := &storage.ReleaseArtifact{Component: "agent", Version: release.version, Channel: release.channel, Platform: "linux", Arch: "amd64", SourceURL: "https://example.com/agent", SHA256: strings.Repeat("ab", 32), SizeBytes: 100}
		if err := f.store.UpsertReleaseArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
		if _, err := releaseManager.EnsureManifestForArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	request := func(token, id string, explicit bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]interface{}{"agent_id": id, "component": "agent", "platform": "linux", "arch": "amd64", "channel": "stable", "explicit_channel": explicit})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/update/manifest", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		requireAuth(handleAgentUpdateManifest)(w, r)
		return w
	}
	assertChannel := func(w *httptest.ResponseRecorder, want string) {
		t.Helper()
		var response struct {
			Success  bool `json:"success"`
			Manifest struct {
				Channel string `json:"channel"`
			} `json:"manifest"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.Success || response.Manifest.Channel != want {
			t.Fatalf("want %s got %d %s", want, w.Code, w.Body.String())
		}
	}
	// Exactly the old Agent's stable request selects dev, without new commands.
	assertChannel(request("boundary-machine-a", "boundary-agent-a", false), "dev")
	assertChannel(request("boundary-machine-a", "boundary-agent-a", true), "stable")
	if err := f.store.UpsertTenantSettings(ctx, &storage.TenantSettingsRecord{TenantID: "boundary-a", Overrides: map[string]interface{}{"features": map[string]interface{}{"agent_update_channel": "beta"}}}); err != nil {
		t.Fatal(err)
	}
	assertChannel(request("boundary-machine-a", "boundary-agent-a", false), "beta")
	assertChannel(request("boundary-machine-b", "boundary-agent-b", false), "dev")
	if w := request("boundary-machine-a", "boundary-agent-b", false); w.Code != http.StatusForbidden {
		t.Fatalf("foreign Agent selected: %d %s", w.Code, w.Body.String())
	}
	if w := request("invalid", "boundary-agent-a", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated selection: %d", w.Code)
	}
	settings.Features.AgentUpdateChannel = ""
	if err := f.store.UpsertGlobalSettings(ctx, &storage.SettingsRecord{Settings: settings, ManagedSections: []string{"features"}}); err != nil {
		t.Fatal(err)
	}
	assertChannel(request("boundary-machine-b", "boundary-agent-b", false), "stable")
	settings.Features.AgentUpdateChannel = "dev"
	if err := f.store.UpsertGlobalSettings(ctx, &storage.SettingsRecord{Settings: settings, ManagedSections: []string{}}); err != nil {
		t.Fatal(err)
	}
	assertChannel(request("boundary-machine-b", "boundary-agent-b", false), "stable")
	settingsResolver = nil
	if w := request("boundary-machine-b", "boundary-agent-b", false); w.Code != http.StatusInternalServerError {
		t.Fatalf("unavailable settings silently chose channel: %d", w.Code)
	}
}

func TestAgentChannelCommandValidationAndTenantBoundary(t *testing.T) {
	f := newBoundaryFixture(t)
	for _, channel := range []string{"", "nightly", "stable", "beta", "dev"} {
		body := `{"command":"install_channel","data":{"channel":"` + channel + `"}}`
		w := f.request(t, handleAgentCommand, "operator", http.MethodPost, "/api/v1/agents/command/boundary-agent-a", body)
		want := http.StatusServiceUnavailable
		if channel == "" || channel == "nightly" {
			want = http.StatusBadRequest
		}
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", channel, w.Code, want, w.Body.String())
		}
		w = f.request(t, handleAgentCommand, "operator", http.MethodPost, "/api/v1/agents/command/boundary-agent-b", body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("foreign command: %d", w.Code)
		}
	}
}

func TestPrereleaseIntakeSettingValidation(t *testing.T) {
	previousPath := loadedConfigPath
	loadedConfigPath = filepath.Join(t.TempDir(), "config.toml")
	t.Cleanup(func() { loadedConfigPath = previousPath })
	for _, value := range []string{"", "true", "false", "invalid"} {
		t.Run(value, func(t *testing.T) {
			cfg := DefaultConfig()
			previous := cfg.Releases.IncludePrerelease
			result, err := applyServerSettings(cfg, &serverSettingsRequest{Releases: &serverSettingsReleasesSection{IncludePrerelease: &value}})
			if value == "invalid" {
				if err == nil || cfg.Releases.IncludePrerelease != previous {
					t.Fatal("invalid intake changed config")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Releases.IncludePrerelease != value {
				t.Fatal("intake setting not saved")
			}
			if previous != value && !result.RestartRequired {
				t.Fatal("worker change must require restart")
			}
		})
	}
}
