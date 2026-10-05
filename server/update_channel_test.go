package main

import (
	"net/http"
	"path/filepath"
	"testing"
)

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
