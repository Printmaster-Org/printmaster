package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUploadMetricsReceipt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		response  string
		status    int
		sent      int
		wantError string
		rejected  bool
	}{
		{"complete legacy receipt", `{"success":true,"received":2,"stored":2}`, 200, 2, "", false},
		{"empty batch", `{"success":true,"received":0,"stored":0}`, 200, 0, "", false},
		{"partial legacy receipt", `{"success":true,"received":2,"stored":1}`, 200, 2, "1 of 2", true},
		{"partial detailed receipt", `{"success":false,"received":2,"stored":1,"failures":[{"index":1,"serial":"printer-2","code":"ownership_conflict"}]}`, 200, 2, "ownership_conflict", true},
		{"all rejected", `{"success":false,"received":2,"stored":0}`, 200, 2, "0 of 2", true},
		{"explicit failure", `{"success":false,"received":2,"stored":2}`, 200, 2, "rejected", true},
		{"contradictory failures", `{"success":true,"received":2,"stored":2,"failures":[{"index":0,"code":"storage_error"}]}`, 200, 2, "storage_error", true},
		{"missing stored", `{"success":true,"received":2}`, 200, 2, "0 of 2", true},
		{"missing success", `{"received":2,"stored":2}`, 200, 2, "rejected", true},
		{"missing receipt", `{}`, 200, 2, "invalid metrics batch receipt", false},
		{"null receipt", `null`, 200, 2, "invalid metrics batch receipt", false},
		{"wrong received", `{"success":true,"received":1,"stored":1}`, 200, 2, "invalid metrics batch receipt", false},
		{"negative stored", `{"success":true,"received":2,"stored":-1}`, 200, 2, "invalid metrics batch receipt", false},
		{"excess stored", `{"success":true,"received":2,"stored":3}`, 200, 2, "invalid metrics batch receipt", false},
		{"malformed response", `not json`, 200, 2, "failed to decode response", false},
		{"invalid field type", `{"success":true,"received":2,"stored":"2"}`, 200, 2, "failed to decode response", false},
		{"service failure", `unavailable`, 503, 2, "503", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/metrics/batch" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			client := NewServerClient(server.URL, "agent-1", "test-token")
			previous := time.Unix(100, 0)
			client.lastMetricsUpload = previous
			metrics := make([]interface{}, tc.sent)
			for i := range metrics {
				metrics[i] = map[string]interface{}{"serial": "printer"}
			}
			err := client.UploadMetrics(context.Background(), metrics)
			if tc.wantError == "" {
				if err != nil || !client.lastMetricsUpload.After(previous) {
					t.Fatalf("complete receipt: error=%v last_upload=%v", err, client.lastMetricsUpload)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error=%v, want %q", err, tc.wantError)
			}
			var rejected *MetricsBatchError
			if errors.As(err, &rejected) != tc.rejected {
				t.Fatalf("acknowledged rejection=%v, want %v", err, tc.rejected)
			}
			if !client.lastMetricsUpload.Equal(previous) {
				t.Fatalf("failed upload advanced last success: %v", client.lastMetricsUpload)
			}
			if tc.name == "partial detailed receipt" &&
				(len(rejected.Result.Failures) != 1 || rejected.Result.Failures[0].Index != 1 || rejected.Result.Failures[0].Serial != "printer-2") {
				t.Fatalf("failure details lost: %+v", rejected)
			}
		})
	}
}
