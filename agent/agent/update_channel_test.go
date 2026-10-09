package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"printmaster/common/updatepolicy"
)

func TestManifestExplicitChannelFlagPreservesLegacyRequests(t *testing.T) {
	t.Parallel()
	var requests []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"manifest":{"version":"0.31.2-dev.1","channel":"dev"}}`))
	}))
	defer server.Close()
	client := NewServerClient(server.URL, "legacy-compatible", "test-token")
	if _, err := client.GetLatestManifest(context.Background(), "agent", "linux", "amd64", "stable", ""); err != nil {
		t.Fatal(err)
	}
	if _, exists := requests[0]["explicit_channel"]; exists {
		t.Fatal("ordinary request opts out of fleet default")
	}
	if _, err := client.GetLatestManifest(updatepolicy.WithExplicitChannel(context.Background()), "agent", "linux", "amd64", "dev", ""); err != nil {
		t.Fatal(err)
	}
	if requests[1]["explicit_channel"] != true {
		t.Fatal("explicit install lost override marker")
	}
}

func TestManifestRequestCarriesArtifactFormat(t *testing.T) {
	t.Parallel()
	var formats []interface{}
	var downloads []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			downloads = append(downloads, r.URL.RequestURI())
			_, _ = w.Write([]byte("artifact"))
			return
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		formats = append(formats, body["format"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"manifest":{"version":"0.32.0","channel":"stable","format":"msi"}}`))
	}))
	defer server.Close()
	client := NewServerClient(server.URL, "format-agent", "test-token")

	// Binary is the default, so the field is omitted for Server compatibility.
	if _, err := client.GetLatestManifest(context.Background(), "agent", "windows", "amd64", "stable", ""); err != nil {
		t.Fatal(err)
	}
	manifest, err := client.GetLatestManifest(context.Background(), "agent", "windows", "amd64", "stable", "msi")
	if err != nil {
		t.Fatal(err)
	}
	if formats[0] != nil || formats[1] != "msi" || manifest.Format != "msi" {
		t.Fatalf("formats sent %v, manifest format %q", formats, manifest.Format)
	}

	// Without a Server-supplied URL the fallback must still select the MSI.
	manifest.Component, manifest.Platform, manifest.Arch = "agent", "windows", "amd64"
	if _, err := client.DownloadArtifact(context.Background(), manifest, filepath.Join(t.TempDir(), "agent.msi"), 0); err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 1 || downloads[0] != "/api/v1/agents/update/download/agent/0.32.0/windows-amd64?format=msi" {
		t.Fatalf("fallback download URL = %v", downloads)
	}
}
