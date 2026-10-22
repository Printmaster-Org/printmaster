package releases

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestIntakeDeadlineReportsFailure(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker, err := NewIntakeWorker(store, nil, Options{CacheDir: t.TempDir(), BaseAPIURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var last SyncProgress
	err = worker.RunOnceWithProgress(ctx, func(progress SyncProgress) { last = progress })
	if !errors.Is(err, context.DeadlineExceeded) || last.Phase != "error" || last.Error == "" {
		t.Fatalf("deadline must fail visibly, not complete: %+v %v", last, err)
	}
}

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
                        "size": 11,
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
	if err := os.WriteFile(art.CachePath, []byte("corruptdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("cache repair failed: %v", err)
	}
	if downloadHits != firstHits+1 {
		t.Fatal("corrupt cache was not downloaded again")
	}

}

func TestIntakeFailureDoesNotReportCompletion(t *testing.T) {
	t.Parallel()
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			_, _ = w.Write([]byte(`[{"tag_name":"agent-v0.32.0","assets":[{"name":"printmaster-agent-v0.32.0-linux-amd64","browser_download_url":"` + base + `/artifact","size":8}]}]`))
			return
		}
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()
	base = server.URL
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker, err := NewIntakeWorker(store, nil, Options{CacheDir: t.TempDir(), BaseAPIURL: base, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	var last SyncProgress
	err = worker.RunOnceWithProgress(context.Background(), func(p SyncProgress) {
		last = p
		if p.Phase == "complete" {
			t.Error("failed intake reported success")
		}
	})
	if err == nil || last.Phase != "error" || last.CompletedFiles != 0 || last.PercentComplete == 100 {
		t.Fatalf("untruthful failure: %+v err=%v", last, err)
	}
	if _, err := store.GetReleaseArtifact(context.Background(), "agent", "0.32.0", "linux", "amd64", ""); err == nil {
		t.Fatal("incomplete artifact entered cache")
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
