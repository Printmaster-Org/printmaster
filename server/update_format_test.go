package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pmsettings "printmaster/common/settings"
	"printmaster/server/releases"
	serversettings "printmaster/server/settings"
	"printmaster/server/storage"
)

// TestAgentUpdateManifestAndDownloadHonorFormat proves that a Windows release
// with both an .exe and an .msi serves each to the Agent that asked for it,
// that old Agents (no format) keep receiving the binary, that OS packages and
// unknown formats are rejected, and that both endpoints still require Agent
// authentication.
func TestAgentUpdateManifestAndDownloadHonorFormat(t *testing.T) {
	f := newBoundaryFixture(t)
	previousResolver, previousReleases := settingsResolver, releaseManager
	t.Cleanup(func() { settingsResolver, releaseManager = previousResolver, previousReleases })
	var err error
	if settingsResolver, err = serversettings.NewResolver(f.store); err != nil {
		t.Fatal(err)
	}
	if releaseManager, err = releases.NewManager(f.store, nil, releases.ManagerOptions{ManifestVersion: "1.0"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := f.store.UpsertGlobalSettings(ctx, &storage.SettingsRecord{Settings: pmsettings.DefaultSettings()}); err != nil {
		t.Fatal(err)
	}

	cacheDir := t.TempDir()
	payloads := map[string]string{"binary": "raw-exe-bytes", "msi": "msi-package-bytes"}
	for format, body := range payloads {
		path := filepath.Join(cacheDir, "agent."+format)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		artifact := &storage.ReleaseArtifact{Component: "agent", Version: "0.32.0", Channel: "stable", Platform: "windows", Arch: "amd64",
			Format: format, SourceURL: "https://example.com/" + format, CachePath: path, SHA256: strings.Repeat(format[:1], 64), SizeBytes: int64(len(body))}
		if err := f.store.UpsertReleaseArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
		if _, err := releaseManager.EnsureManifestForArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}

	manifestRequest := func(token string, fields map[string]interface{}) *httptest.ResponseRecorder {
		body := map[string]interface{}{"agent_id": "boundary-agent-a", "component": "agent", "platform": "windows", "arch": "amd64", "channel": "stable", "explicit_channel": true}
		for k, v := range fields {
			body[k] = v
		}
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/update/manifest", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		requireAuth(handleAgentUpdateManifest)(w, r)
		return w
	}
	type manifestResponse struct {
		Success  bool `json:"success"`
		Manifest struct {
			Format      string `json:"format"`
			SHA256      string `json:"sha256"`
			DownloadURL string `json:"download_url"`
		} `json:"manifest"`
	}
	download := func(token, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		requireAuth(handleAgentUpdateDownload)(w, r)
		return w
	}

	for _, tc := range []struct {
		name, requested, wantFormat, wantURLSuffix string
	}{
		{"legacy agent omits format", "", "binary", "/windows-amd64"},
		{"binary install", "binary", "binary", "/windows-amd64"},
		{"msi install", "msi", "msi", "/windows-amd64?format=msi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]interface{}{}
			if tc.requested != "" {
				fields["format"] = tc.requested
			}
			w := manifestRequest("boundary-machine-a", fields)
			var resp manifestResponse
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil || !resp.Success {
				t.Fatalf("manifest request failed: %d %s", w.Code, w.Body.String())
			}
			if resp.Manifest.Format != tc.wantFormat || resp.Manifest.SHA256 != strings.Repeat(tc.wantFormat[:1], 64) {
				t.Fatalf("got format %q sha %q, want %s artifact", resp.Manifest.Format, resp.Manifest.SHA256, tc.wantFormat)
			}
			if !strings.HasSuffix(resp.Manifest.DownloadURL, tc.wantURLSuffix) {
				t.Fatalf("download URL %q missing suffix %q", resp.Manifest.DownloadURL, tc.wantURLSuffix)
			}
			got := download("boundary-machine-a", resp.Manifest.DownloadURL)
			if got.Code != http.StatusOK || got.Body.String() != payloads[tc.wantFormat] {
				t.Fatalf("download served %d %q, want %s payload", got.Code, got.Body.String(), tc.wantFormat)
			}
		})
	}

	for _, bad := range []string{"deb", "rpm", "zip"} {
		if w := manifestRequest("boundary-machine-a", map[string]interface{}{"format": bad}); w.Code != http.StatusBadRequest {
			t.Fatalf("manifest accepted format %q: %d", bad, w.Code)
		}
		if w := download("boundary-machine-a", "/api/v1/agents/update/download/agent/0.32.0/windows-amd64?format="+bad); w.Code != http.StatusBadRequest {
			t.Fatalf("download accepted format %q: %d", bad, w.Code)
		}
	}

	if w := manifestRequest("invalid", map[string]interface{}{"format": "msi"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated manifest request: %d", w.Code)
	}
	if w := download("invalid", "/api/v1/agents/update/download/agent/0.32.0/windows-amd64?format=msi"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download: %d", w.Code)
	}
}
