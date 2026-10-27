package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

type directoryPinnedSource struct{ directory string }

func (s directoryPinnedSource) GetPinned(_ context.Context, name, pin string) ([]byte, error) {
	parts := strings.Split(name, "/")
	file := parts[len(parts)-1]
	if len(parts) == 2 {
		file = "record.json"
	}
	data, err := os.ReadFile(filepath.Join(s.directory, file))
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != pin {
		return nil, fmt.Errorf("source hash mismatch")
	}
	return data, nil
}

func TestAuthenticatedMirrorServesPublisherTargetToRealVerifier(t *testing.T) {
	newBoundaryFixture(t)
	previous := serverConfig
	t.Cleanup(func() { serverConfig = previous })
	keys := make(map[string]ed25519.PrivateKey)
	pubs := make(map[string]crypto.PublicKey)
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keys[role] = key
		pubs[role] = pub
	}
	root, err := updatetrust.InitializeRoot(keys["root"], pubs, time.Now().Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	delete(keys, "root")
	artifact := []byte("disposable qualification artifact")
	file := filepath.Join(t.TempDir(), "agent.exe")
	if err := os.WriteFile(file, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	target := updatetrust.Target{Component: "agent", Channel: "beta", Version: "0.32.0-beta.qualification",
		Platform: "windows", Arch: "amd64", Format: "exe", SHA256: fmt.Sprintf("%x", sha256.Sum256(artifact)), SizeBytes: int64(len(artifact))}
	workspace, backup, records, local := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := updatetrust.PublishTransaction(updatetrust.TransactionOptions{Publish: updatetrust.PublishOptions{
		Directory: workspace, Root: root, Keys: keys, Targets: []updatetrust.PublishTarget{{Target: target, File: file}}},
		BackupDirectory: backup, RecordDirectory: records, MirrorDirectory: local}); err != nil {
		t.Fatal(err)
	}
	timestamp, err := os.ReadFile(filepath.Join(workspace, "timestamp.json"))
	if err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(backup, fmt.Sprintf("%x", sha256.Sum256(timestamp)))
	identity, err := updatetrust.SealedIdentity(sealed, root)
	if err != nil {
		t.Fatal(err)
	}
	publicMirror := t.TempDir()
	if err := updatetrust.MirrorPublication(context.Background(), publicMirror, root, identity, directoryPinnedSource{sealed}); err != nil {
		t.Fatal(err)
	}
	publicServer := httptest.NewTLSServer(http.FileServer(http.Dir(publicMirror)))
	defer publicServer.Close()
	serverConfig = &Config{Releases: ReleasesConfig{
		PublisherMetadataURL: publicServer.URL,
		publicMetadataClient: publicServer.Client(),
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer boundary-machine-a")
		requireAuth(handleAgentUpdateMetadata)(w, r)
	}))
	defer server.Close()
	client, err := updatetrust.NewClient(updatetrust.Options{TrustedRoot: root,
		MetadataURL: server.URL + updatetrust.MetadataRoute, CacheDir: filepath.Join(t.TempDir(), "metadata"), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	target.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("substituted")))
	if err := client.Verify(context.Background(), target); err == nil {
		t.Fatal("Server substituted target identity")
	}
}

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
