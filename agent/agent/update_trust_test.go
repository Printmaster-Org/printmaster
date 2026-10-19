package agent

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func publisherTransportFixture(t *testing.T) ([]byte, string, updatetrust.Target) {
	t.Helper()
	directory := t.TempDir()
	artifact := []byte("publisher artifact")
	file := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(file, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	_, rootKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := make(map[string]ed25519.PrivateKey)
	public := make(map[string]crypto.PublicKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		pub, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		keys[role], public[role] = key, pub
	}
	root, err := updatetrust.InitializeRoot(rootKey, public, time.Now().Add(365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(artifact)
	target := updatetrust.Target{Component: "agent", Channel: "stable", Version: "0.32.0",
		Platform: "windows", Arch: "amd64", Format: "binary", SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(artifact))}
	if err := updatetrust.Publish(updatetrust.PublishOptions{Directory: directory, Root: root, Keys: keys,
		Targets: []updatetrust.PublishTarget{{Target: target, File: file}}}); err != nil {
		t.Fatal(err)
	}
	return root, directory, target
}

func TestPublisherTransportUsesTLSAndCurrentAgentToken(t *testing.T) {
	t.Parallel()
	root, directory, target := publisherTransportFixture(t)
	var expected atomic.Value
	expected.Store("first")
	var failures atomic.Int64
	files := http.StripPrefix(updatetrust.MetadataRoute, http.FileServer(http.Dir(directory)))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) || !strings.HasPrefix(r.URL.Path, updatetrust.MetadataRoute) {
			failures.Add(1)
			http.Error(w, "bad authentication", 401)
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer server.Close()
	client := &ServerClient{BaseURL: server.URL, Token: "first", HTTPClient: server.Client()}
	verifier, err := client.NewPublisherVerifier(root, filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	expected.Store("second")
	client.SetToken("second")
	if err := verifier.Verify(context.Background(), target); err != nil || failures.Load() != 0 {
		t.Fatalf("TLS/authentication not preserved: %v failures=%d", err, failures.Load())
	}
	wrongRoot, _, _ := publisherTransportFixture(t)
	untrusted, err := client.NewPublisherVerifier(wrongRoot, filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	if err := untrusted.Verify(context.Background(), target); err == nil {
		t.Fatal("Server metadata replaced independent embedded root authority")
	}
}

func TestPublisherTransportRejectsForeignRedirectBeforeSendingToken(t *testing.T) {
	t.Parallel()
	root, _, target := publisherTransportFixture(t)
	var requests atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be contacted", 500)
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+r.URL.Path, http.StatusFound)
	}))
	defer server.Close()
	client := &ServerClient{BaseURL: server.URL, Token: "secret-fixture", HTTPClient: server.Client()}
	verifier, err := client.NewPublisherVerifier(root, filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background(), target); err == nil {
		t.Fatal("foreign redirect accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("foreign origin contacted")
	}
}

func TestPublisherTransportRequiresEmbeddedRoot(t *testing.T) {
	t.Parallel()
	client := &ServerClient{BaseURL: "https://example.invalid", HTTPClient: &http.Client{}}
	for _, root := range [][]byte{nil, []byte("null\n"), []byte("  ")} {
		if _, err := client.NewPublisherVerifier(root, t.TempDir()); !errors.Is(err, ErrPublisherRootUnprovisioned) {
			t.Fatalf("unprovisioned root accepted: %v", err)
		}
	}
	if _, err := client.NewPublisherVerifier([]byte("{}"), t.TempDir()); err == nil {
		t.Fatal("malformed embedded root accepted")
	}
}

func TestPublisherTransportCancellation(t *testing.T) {
	t.Parallel()
	root, _, target := publisherTransportFixture(t)
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := &ServerClient{BaseURL: server.URL, Token: "fixture", HTTPClient: server.Client()}
	verifier, err := client.NewPublisherVerifier(root, filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- verifier.Verify(ctx, target) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata request did not start")
	}
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metadata request ignored cancellation")
	}
}

func TestPublisherTransportRejectsSameOriginNonmetadataRedirect(t *testing.T) {
	t.Parallel()
	root, _, target := publisherTransportFixture(t)
	var unexpected atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, updatetrust.MetadataRoute) {
			unexpected.Add(1)
			http.Error(w, "unexpected endpoint", 500)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer server.Close()
	client := &ServerClient{BaseURL: server.URL, Token: "fixture", HTTPClient: server.Client()}
	verifier, err := client.NewPublisherVerifier(root, filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background(), target); err == nil || unexpected.Load() != 0 {
		t.Fatalf("metadata credentials reached unrelated endpoint: %v requests=%d", err, unexpected.Load())
	}
}
