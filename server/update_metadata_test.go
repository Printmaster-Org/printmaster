package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"printmaster/common/updatetrust"
)

func TestPublisherMetadataDeliveryBoundary(t *testing.T) {
	fixture := newBoundaryFixture(t)
	previous := serverConfig
	t.Cleanup(func() { serverConfig = previous })
	directory := t.TempDir()
	serverConfig = &Config{Releases: ReleasesConfig{PublisherMetadataDir: directory}}
	original := []byte("{ \"signed\": {\"version\":1}, \"signatures\":[] }\n")
	if err := os.WriteFile(filepath.Join(directory, "timestamp.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "1.root.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method, filename, token string
		status                        int
	}{
		{"timestamp", "GET", "timestamp.json", "boundary-machine-a", 200},
		{"root", "GET", "1.root.json", "boundary-machine-b", 200},
		{"missing next root", "GET", "2.root.json", "boundary-machine-a", 404},
		{"no token", "GET", "timestamp.json", "", 401},
		{"invalid token", "GET", "timestamp.json", "invalid", 401},
		{"user token is not machine token", "GET", "timestamp.json", fixture.sessions["admin"], 401},
		{"method", "POST", "timestamp.json", "boundary-machine-a", 405},
		{"key file", "GET", "targets.pem", "boundary-machine-a", 400},
		{"local state", "GET", "root.json", "boundary-machine-a", 400},
		{"zero version", "GET", "0.root.json", "boundary-machine-a", 400},
		{"nested path", "GET", "nested/timestamp.json", "boundary-machine-a", 400},
		{"backslash", "GET", `..\timestamp.json`, "boundary-machine-a", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, updatetrust.MetadataRoute+test.filename, nil)
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			w := httptest.NewRecorder()
			requireAuth(handleAgentUpdateMetadata)(w, req)
			if w.Code != test.status {
				t.Fatalf("status=%d want %d: %s", w.Code, test.status, w.Body.String())
			}
			if test.status == 200 && (!bytes.Equal(w.Body.Bytes(), original) || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatal("metadata bytes changed or freshness caching enabled")
			}
		})
	}
	serverConfig.Releases.PublisherMetadataDir = ""
	req := httptest.NewRequest("GET", updatetrust.MetadataRoute+"timestamp.json", nil)
	req.Header.Set("Authorization", "Bearer boundary-machine-a")
	w := httptest.NewRecorder()
	requireAuth(handleAgentUpdateMetadata)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured mirror status=%d", w.Code)
	}
}

func TestPublisherMetadataRejectsOversizedAndNonregularFiles(t *testing.T) {
	newBoundaryFixture(t)
	previous := serverConfig
	t.Cleanup(func() { serverConfig = previous })
	directory := t.TempDir()
	serverConfig = &Config{Releases: ReleasesConfig{PublisherMetadataDir: directory}}
	file, err := os.Create(filepath.Join(directory, "timestamp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(updatetrust.MaxMetadataBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "1.root.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"timestamp.json", "1.root.json"} {
		req := httptest.NewRequest("GET", updatetrust.MetadataRoute+name, nil)
		req.Header.Set("Authorization", "Bearer boundary-machine-a")
		w := httptest.NewRecorder()
		requireAuth(handleAgentUpdateMetadata)(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("unsafe file %s status=%d", name, w.Code)
		}
	}
}
