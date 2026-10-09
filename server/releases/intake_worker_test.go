package releases

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestIntakeWorkerCachesArtifacts(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	var downloadURL string
	downloadHits := 0

	mux.HandleFunc("/repos/test-owner/printmaster/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload := `[
            {
                "tag_name": "server-v0.9.16",
                "draft": false,
                "prerelease": false,
                "body": "notes",
                "published_at": "2025-11-20T12:00:00Z",
                "assets": [
                    {
                        "name": "printmaster-server-v0.9.16-windows-amd64.exe",
                        "browser_download_url": "` + downloadURL + `",
                        "size": 12,
                        "updated_at": "2025-11-20T12:00:00Z"
                    }
                ]
            }
        ]`
		_, _ = w.Write([]byte(payload))
	})

	mux.HandleFunc("/downloads/server", func(w http.ResponseWriter, r *http.Request) {
		downloadHits++
		_, _ = w.Write([]byte("printmaster"))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	downloadURL = server.URL + "/downloads/server"

	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	worker, err := NewIntakeWorker(store, nil, Options{
		CacheDir:     t.TempDir(),
		RepoOwner:    "test-owner",
		RepoName:     "printmaster",
		BaseAPIURL:   server.URL,
		HTTPClient:   server.Client(),
		PollInterval: time.Hour,
		UserAgent:    "test",
	})
	if err != nil {
		t.Fatalf("failed to create worker: %v", err)
	}

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("run once failed: %v", err)
	}

	art, err := store.GetReleaseArtifact(context.Background(), "server", "0.9.16", "windows", "amd64", "")
	if err != nil {
		t.Fatalf("artifact not persisted: %v", err)
	}
	if art.CachePath == "" {
		t.Fatalf("expected cache path to be set")
	}
	if _, err := os.Stat(art.CachePath); err != nil {
		t.Fatalf("cached file missing: %v", err)
	}

	firstHits := downloadHits
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if downloadHits != firstHits {
		t.Fatalf("expected cached artifact to skip download")
	}

}

// TestIntakeWorkerCachesBinaryAndMSISeparately covers the Windows release
// layout where the .exe and .msi share a platform/arch. Each must get its own
// cache entry and manifest, and OS packages (delivered by package repos) must
// not be downloaded at all.
func TestIntakeWorkerCachesBinaryAndMSISeparately(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"printmaster-agent-v0.32.0-windows-amd64.exe": "raw-exe",
		"printmaster-agent-v0.32.0-windows-amd64.msi": "msi-package",
		"printmaster-agent_0.32.0_amd64.deb":          "deb-package",
		"printmaster-agent-0.32.0-1.fc44.x86_64.rpm":  "rpm-package",
	}
	hits := map[string]int{}
	mux := http.NewServeMux()
	var baseURL string
	mux.HandleFunc("/repos/test-owner/printmaster/releases", func(w http.ResponseWriter, r *http.Request) {
		var assets []string
		for name, body := range bodies {
			assets = append(assets, `{"name":"`+name+`","browser_download_url":"`+baseURL+`/dl/`+name+`","size":`+strconv.Itoa(len(body))+`}`)
		}
		_, _ = w.Write([]byte(`[{"tag_name":"agent-v0.32.0","published_at":"2025-11-20T12:00:00Z","assets":[` + strings.Join(assets, ",") + `]}]`))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		hits[name]++
		_, _ = w.Write([]byte(bodies[name]))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	baseURL = server.URL

	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewManager(store, nil, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewIntakeWorker(store, nil, Options{CacheDir: t.TempDir(), RepoOwner: "test-owner", RepoName: "printmaster",
		BaseAPIURL: server.URL, HTTPClient: server.Client(), PollInterval: time.Hour, UserAgent: "test", ManifestManager: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for format, want := range map[string]string{"binary": "raw-exe", "msi": "msi-package"} {
		art, err := store.GetReleaseArtifact(ctx, "agent", "0.32.0", "windows", "amd64", format)
		if err != nil {
			t.Fatalf("%s artifact missing: %v", format, err)
		}
		cached, err := os.ReadFile(art.CachePath)
		if err != nil || string(cached) != want {
			t.Fatalf("%s cache holds %q (%v), want %q", format, cached, err, want)
		}
		manifest, err := manager.GetLatestManifest(ctx, "agent", "windows", "amd64", "stable", format)
		if err != nil || manifest.Format != format || manifest.SHA256 != art.SHA256 {
			t.Fatalf("%s manifest mismatch: %+v %v", format, manifest, err)
		}
	}
	for _, pkg := range []string{"printmaster-agent_0.32.0_amd64.deb", "printmaster-agent-0.32.0-1.fc44.x86_64.rpm"} {
		if hits[pkg] != 0 {
			t.Fatalf("OS package %s was downloaded", pkg)
		}
	}
	if _, err := store.GetReleaseArtifact(ctx, "agent", "0.32.0", "linux", "amd64", "deb"); err == nil {
		t.Fatal("deb package cached")
	}
}
