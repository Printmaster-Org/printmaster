package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pmsettings "printmaster/common/settings"
	"printmaster/server/releases"
	serversettings "printmaster/server/settings"
	"printmaster/server/storage"
)

func TestHistoricalLegacyUpdaterAgainstCurrentServer(t *testing.T) {
	if os.Getenv("PRINTMASTER_TEST_LEGACY_UPDATERS") != "1" {
		t.Skip("set PRINTMASTER_TEST_LEGACY_UPDATERS=1 to compile and execute pinned historical updater tests in temporary directories")
	}
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
	if err := f.store.UpsertGlobalSettings(t.Context(), &storage.SettingsRecord{Settings: pmsettings.DefaultSettings()}); err != nil {
		t.Fatal(err)
	}
	body := []byte("bridge qualification artifact, not an executable")
	file := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	artifact := &storage.ReleaseArtifact{Component: "agent", Version: "0.32.0", Channel: "stable",
		Platform: runtime.GOOS, Arch: runtime.GOARCH, Format: "binary", CachePath: file,
		SourceURL: "https://example.invalid/candidate", SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(body))}
	if err := f.store.UpsertReleaseArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseManager.EnsureManifestForArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	var manifests, downloads, resumed atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/update/manifest", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Error(err)
		}
		for _, field := range []string{"format", "explicit_channel"} {
			if _, present := fields[field]; present {
				t.Errorf("historical request unexpectedly contains %s", field)
			}
		}
		manifests.Add(1)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		handleAgentUpdateManifest(w, r)
	}))
	mux.HandleFunc("/api/v1/agents/update/download/", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.Header.Get("Range") != "" {
			resumed.Add(1)
		}
		if r.Header.Get("X-Bridge-Test") == "tampered" {
			w.Write(bytes.Repeat([]byte("x"), len(body)))
			return
		}
		handleAgentUpdateDownload(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, baseline := range []struct{ version, commit string }{
		{"0.30.5", "816c92951c36c57be7a180e5564cf2b0be33c702"},
		{"0.31.0", "84ed40c6bbed41414c4bf345c7a72e533afad404"},
		{"0.31.1", "fc829197b991853a78da72cab625b1c821d0d91b"},
	} {
		t.Run(baseline.version, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			archiveCommand := exec.CommandContext(ctx, "git", "archive", baseline.commit, "agent", "common")
			archiveCommand.Dir = ".."
			archive, err := archiveCommand.Output()
			if err != nil {
				t.Fatalf("read pinned historical source: %v", err)
			}
			root := t.TempDir()
			reader := tar.NewReader(bytes.NewReader(archive))
			for {
				header, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if header.Typeflag != tar.TypeReg {
					continue
				}
				if !filepath.IsLocal(header.Name) || header.Size > 32<<20 {
					t.Fatalf("unsafe historical source path or size: %s", header.Name)
				}
				path := filepath.Join(root, filepath.FromSlash(header.Name))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "agent", "autoupdate", "bridge_qualification_test.go")
			if err := os.WriteFile(path, []byte(historicalBridgeTest), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout=3m", "-run=^TestBridgeQualification$", "-v", "./autoupdate")
			command.Dir = filepath.Join(root, "agent")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "GOWORK=") && !strings.HasPrefix(entry, "PRINTMASTER_LIVE_") {
					command.Env = append(command.Env, entry)
				}
			}
			command.Env = append(command.Env, "GOWORK=off", "BRIDGE_TEST_URL="+server.URL, "BRIDGE_TEST_VERSION="+baseline.version)
			output, err := command.CombinedOutput()
			t.Logf("historical updater %s (%s):\n%s", baseline.version, baseline.commit, output)
			if err != nil {
				t.Fatalf("historical updater failed: %v", err)
			}
		})
	}
	if manifests.Load() < 9 || downloads.Load() < 9 || resumed.Load() < 3 {
		t.Fatalf("historical clients did not exercise expected HTTP paths: manifests=%d downloads=%d resumed=%d", manifests.Load(), downloads.Load(), resumed.Load())
	}
}

const historicalBridgeTest = `package autoupdate

import (
    "context"
    "os"
    "path/filepath"
    "net/http"
    "runtime"
    "strings"
    "testing"
    "printmaster/agent/agent"
)

type bridgeTransport struct { scenario string }
func (transport bridgeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
    cloned := request.Clone(request.Context())
    cloned.Header.Set("X-Bridge-Test", transport.scenario)
    return http.DefaultTransport.RoundTrip(cloned)
}

func TestBridgeQualification(t *testing.T) {
    for _, scenario := range []string{"valid", "tampered", "unauthenticated", "foreign-agent"} {
        t.Run(scenario, func(t *testing.T) {
            directory := t.TempDir()
            binary := filepath.Join(directory, "agent.exe")
            if err := os.WriteFile(binary, []byte("old binary"), 0700); err != nil { t.Fatal(err) }
            token, identity := "boundary-machine-a", "boundary-agent-a"
            if scenario == "unauthenticated" { token = "invalid" }
            if scenario == "foreign-agent" { identity = "boundary-agent-b" }
            client := agent.NewServerClient(os.Getenv("BRIDGE_TEST_URL"), identity, token)
            client.HTTPClient = &http.Client{Transport: bridgeTransport{scenario: scenario}}
            adapter := NewClientAdapter(client)
            manager, err := NewManager(Options{Enabled: false, CurrentVersion: os.Getenv("BRIDGE_TEST_VERSION"),
                DataDir: directory, BinaryPath: binary, Platform: runtime.GOOS, Arch: runtime.GOARCH,
                Channel: "stable", ServerClient: adapter, MaxRetries: 1})
            if err != nil { t.Fatal(err) }
            manager.enabled = true
            manager.useMSI, manager.usePackageManager = false, false
            restarts, helpers := 0, 0
            manager.restartFn = func() error { restarts++; return nil }
            manager.launchHelperFn = func(string) error { helpers++; return nil }
            ctx := context.Background()
            manifest, err := adapter.GetLatestManifest(ctx, "agent", runtime.GOOS, runtime.GOARCH, "stable")
            if scenario == "unauthenticated" || scenario == "foreign-agent" {
                if err == nil { t.Fatal("unauthorized legacy manifest accepted") }
                return
            }
            if err != nil || manifest == nil || manifest.Version != "0.32.0" { t.Fatalf("legacy manifest: %+v %v", manifest, err) }
            if !manager.isUpdateNeeded(manifest) { t.Fatal("historical policy rejected cross-minor stable upgrade") }
            if scenario == "valid" {
                partial := filepath.Join(directory, "resume")
                if err := os.WriteFile(partial, []byte("bridge"), 0600); err != nil { t.Fatal(err) }
                if _, err := adapter.DownloadArtifact(ctx, manifest, partial, 6); err != nil { t.Fatal(err) }
                if err := manager.verifyHash(partial, manifest.SHA256); err != nil { t.Fatalf("resumed hash: %v", err) }
            }
            err = manager.ForceInstallLatest(ctx, "bridge qualification")
            if scenario == "tampered" {
                if err == nil || !strings.Contains(err.Error(), "hash mismatch") { t.Fatalf("tampered download not rejected: %v", err) }
                previous, readErr := os.ReadFile(binary)
                if readErr != nil || string(previous) != "old binary" || restarts != 0 || helpers != 0 { t.Fatal("rejected artifact changed installation") }
                return
            }
            if err != nil || restarts != 1 { t.Fatalf("legacy handoff failed: %v restarts=%d", err, restarts) }
            if runtime.GOOS == "windows" {
                if helpers != 1 { t.Fatal("Windows helper handoff was not observed") }
                staged := filepath.Join(manager.stateDir, stagingDirName, "agent-0.32.0-"+runtime.GOOS+"-"+runtime.GOARCH+manager.binaryExtension())
                if err := manager.verifyHash(staged, manifest.SHA256); err != nil { t.Fatal(err) }
            } else if err := manager.verifyHash(binary, manifest.SHA256); err != nil { t.Fatal(err) }
            pending, err := os.ReadFile(filepath.Join(manager.stateDir, pendingUpdateFile))
            if err != nil || !strings.Contains(string(pending), "0.32.0") { t.Fatalf("legacy pending state missing: %s %v", pending, err) }
        })
    }
}
`
