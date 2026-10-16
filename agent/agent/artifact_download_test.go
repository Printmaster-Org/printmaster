package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactDownloadValidatesRangeAndSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, contentRange string
		status                   int
		resume                   int64
		wantError                bool
	}{
		{"full", "abcdefgh", "", 200, 0, false},
		{"resume", "efgh", "bytes 4-7/8", 206, 4, false},
		{"ignored range", "abcdefgh", "", 200, 4, false},
		{"wrong offset", "efgh", "bytes 3-6/8", 206, 4, true},
		{"wrong total", "efgh", "bytes 4-7/9", 206, 4, true},
		{"missing range", "efgh", "", 206, 4, true},
		{"truncated", "abc", "", 200, 0, true},
		{"oversized", "abcdefghi", "", 200, 0, true},
		{"complete partial", "abcdefgh", "", 200, 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.resume > 0 && tc.resume < 8 && r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", tc.resume) {
					t.Error("resume range not requested")
				}
				w.Header().Set("Content-Range", tc.contentRange)
				w.WriteHeader(tc.status)
				// Flush headers so size checking also exercises chunked bodies.
				w.(http.Flusher).Flush()
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewServerClient(server.URL, "agent", "token")
			dest := filepath.Join(t.TempDir(), "artifact")
			if tc.resume > 0 {
				if err := os.WriteFile(dest, []byte("abcdefgh")[:tc.resume], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var lastPercent int
			var lastBytes int64
			n, err := client.DownloadArtifactWithProgress(context.Background(), &UpdateManifest{
				DownloadURL: server.URL, SizeBytes: 8,
			}, dest, tc.resume, func(percent int, bytes int64) {
				lastPercent, lastBytes = percent, bytes
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("bytes=%d err=%v", n, err)
			}
			if !tc.wantError {
				data, err := os.ReadFile(dest)
				if err != nil || string(data) != "abcdefgh" || n != 8 || lastPercent != 100 || lastBytes != 8 {
					t.Fatalf("data=%q bytes=%d progress=%d/%d err=%v", data, n, lastPercent, lastBytes, err)
				}
			}
		})
	}
}

func TestArtifactDownloadDoesNotSendCredentialsToAnotherOrigin(t *testing.T) {
	t.Parallel()
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("foreign origin was contacted")
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusFound)
	}))
	defer server.Close()
	client := NewServerClient(server.URL, "agent", "secret-token")
	for _, source := range []string{foreign.URL, server.URL} {
		_, err := client.DownloadArtifact(context.Background(), &UpdateManifest{DownloadURL: source, SizeBytes: 8}, filepath.Join(t.TempDir(), "artifact"), 0)
		if err == nil || !strings.Contains(err.Error(), "origin") {
			t.Fatalf("cross-origin download not rejected: %v", err)
		}
	}
}
