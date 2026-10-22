package autoupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"printmaster/agent/agent"
	"printmaster/common/updatetrust"
)

// Both stable tags have the same manifest wire fields and no metadata route:
// agent/server-v0.31.0: 84ed40c6bbed41414c4bf345c7a72e533afad404
// agent/server-v0.31.1: fc829197b991853a78da72cab625b1c821d0d91b
// These are protocol fixtures, not execution of a historical service/install.
func TestModernAgentAgainstLegacyServer(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unprovisioned", "missing-metadata", "msi", "upgraded-server-legacy-manifest"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			body := []byte("publisher authorized artifact")
			sum := sha256.Sum256(body)
			manifest := fixtureManifest(&UpdateManifest{
				Version: "1.0.0", SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body)),
			})
			_, repository := runtimeTrustRepository(t, manifest, body, time.Now())
			root, err := os.ReadFile(filepath.Join(repository, "1.root.json"))
			if err != nil {
				t.Fatal(err)
			}
			var downloads, manifests, metadataRequests atomic.Int64
			files := http.StripPrefix(updatetrust.MetadataRoute, http.FileServer(http.Dir(repository)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer migration-machine" {
					http.Error(w, "machine authentication required", http.StatusUnauthorized)
					return
				}
				switch {
				case r.URL.Path == "/api/v1/agents/update/manifest":
					manifests.Add(1)
					var request struct {
						AgentID   string `json:"agent_id"`
						Component string `json:"component"`
						Platform  string `json:"platform"`
						Arch      string `json:"arch"`
						Channel   string `json:"channel"`
						Format    string `json:"format"`
					}
					if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil ||
						request.AgentID != "migration-agent" || request.Component != "agent" || request.Channel != "stable" ||
						request.Platform != manifest.Platform || request.Arch != manifest.Arch ||
						(request.Format != "binary" && request.Format != "msi") {
						http.Error(w, "invalid manifest request", http.StatusBadRequest)
						return
					}
					// 0.31 decoders ignore format and return a binary manifest.
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]interface{}{
						"success": true,
						"manifest": map[string]interface{}{
							"manifest_version": "1.0", "component": manifest.Component,
							"version": manifest.Version, "minor_line": "1.0",
							"platform": manifest.Platform, "arch": manifest.Arch, "channel": "stable",
							"sha256": manifest.SHA256, "size_bytes": manifest.SizeBytes,
							"source_url":   "https://example.invalid/agent",
							"download_url": "/api/v1/agents/update/download/agent/1.0.0/" + manifest.Platform + "-" + manifest.Arch,
							"generated_at": time.Now().UTC(),
							"signature":    "legacy-server-signature-is-not-publisher-authority",
						},
					}); err != nil {
						t.Errorf("encode legacy response: %v", err)
					}
				case strings.HasPrefix(r.URL.Path, updatetrust.MetadataRoute):
					metadataRequests.Add(1)
					if mode == "upgraded-server-legacy-manifest" {
						files.ServeHTTP(w, r)
					} else {
						http.NotFound(w, r)
					}
				default:
					downloads.Add(1)
					http.Error(w, "test must never download or install", http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			transport := agent.NewServerClient(server.URL, "migration-agent", "migration-machine")
			transport.HTTPClient = server.Client()
			manager := lifecycleManager(t, NewClientAdapter(transport), nil)
			manager.publisherVerifier = nil
			manager.useMSI = mode == "msi"
			cache := filepath.Join(t.TempDir(), "metadata")
			if mode != "unprovisioned" {
				verifier, err := transport.NewPublisherVerifier(root, cache)
				if err != nil {
					t.Fatal(err)
				}
				manager.publisherVerifier = verifier
			}
			entries := []string{"scheduled", "manual", "force", "channel"}
			if mode == "upgraded-server-legacy-manifest" {
				entries = []string{"manual"}
			}
			for _, entry := range entries {
				var err error
				switch entry {
				case "scheduled":
					err = manager.performCheck(t.Context())
				case "manual":
					err = manager.CheckNow(t.Context())
				case "force":
					err = manager.ForceInstallLatest(t.Context(), "migration")
				case "channel":
					err = manager.ForceInstallLatestFromChannel(t.Context(), "migration", "stable")
				}
				if mode == "upgraded-server-legacy-manifest" {
					if err != nil || manager.Progress().Status != "idle" {
						t.Fatalf("authorized legacy binary discovery rejected: %+v %v", manager.Progress(), err)
					}
				} else {
					want := ErrPublisherTrust
					if mode == "msi" {
						want = errManifestFormat
					}
					if !errors.Is(err, want) || manager.Progress().Status != "failed" || manager.Progress().Error == "" {
						t.Fatalf("%s accepted legacy trust/format or hid failure: %+v %v", entry, manager.Progress(), err)
					}
					if manager.latestManifest != nil {
						t.Fatal("unauthorized legacy candidate exposed as available")
					}
				}
				if downloads.Load() != 0 {
					t.Fatal("discovery/authorization failure reached artifact download")
				}
				installed, err := os.ReadFile(manager.binaryPath)
				if err != nil || string(installed) != "old binary" {
					t.Fatalf("legacy compatibility check changed installation: %q %v", installed, err)
				}
				if _, err := os.Stat(filepath.Join(manager.stateDir, pendingUpdateFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("legacy check created pending installation: %v", err)
				}
			}
			if manifests.Load() != int64(len(entries)) {
				t.Fatalf("not every entry exercised legacy HTTP: %d", manifests.Load())
			}
			wantMetadata := mode == "missing-metadata" || mode == "upgraded-server-legacy-manifest"
			if (metadataRequests.Load() > 0) != wantMetadata {
				t.Fatalf("unexpected independent verification requests: %d", metadataRequests.Load())
			}
		})
	}
}
