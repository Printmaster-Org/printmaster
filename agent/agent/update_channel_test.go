package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if _, err := client.GetLatestManifest(context.Background(), "agent", "linux", "amd64", "stable"); err != nil {
		t.Fatal(err)
	}
	if _, exists := requests[0]["explicit_channel"]; exists {
		t.Fatal("ordinary request opts out of fleet default")
	}
	if _, err := client.GetLatestManifest(updatepolicy.WithExplicitChannel(context.Background()), "agent", "linux", "amd64", "dev"); err != nil {
		t.Fatal(err)
	}
	if requests[1]["explicit_channel"] != true {
		t.Fatal("explicit install lost override marker")
	}
}
