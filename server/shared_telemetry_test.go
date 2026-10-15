package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/common/protocol"
	"printmaster/server/storage"
)

func TestSharedTelemetryMachineUploads(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	replacement := &storage.Agent{
		AgentID: "replacement", Token: "replacement-token", TenantID: "boundary-a",
		RegisteredAt: time.Now(), LastSeen: time.Now(),
	}
	if err := f.store.RegisterAgent(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	// The original Agent is not involved in this request. Its persisted
	// identity only anchors management ownership and the tenant boundary.
	request := func(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+replacement.Token)
		w := httptest.NewRecorder()
		requireAuth(handler)(w, r)
		return w
	}
	w := request(handleDevicesBatch, `{"agent_id":"replacement","devices":[
		{"serial":"boundary-device-a","model":"must-not-overwrite","ip":"192.0.2.99"},
		{"serial":"replacement-extra","model":"new discovery","ip":"192.0.2.100"}]}`)
	var devices struct{ Stored int }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &devices) != nil || devices.Stored != 2 {
		t.Fatalf("shared inventory: %d %s", w.Code, w.Body.String())
	}
	ownerDevice, err := f.store.GetDevice(ctx, "boundary-device-a")
	if err != nil || ownerDevice.AgentID != "boundary-agent-a" || ownerDevice.Model != "original" || ownerDevice.IP == "192.0.2.99" {
		t.Fatalf("secondary claimed inventory: %+v %v", ownerDevice, err)
	}
	w = request(handleMetricsBatch, `{"agent_id":"replacement","timestamp":"2026-10-15T12:00:00Z","metrics":[
		{"serial":"boundary-device-a","agent_id":"spoofed","page_count":123},
		{"serial":"replacement-extra","page_count":456},
		{"serial":"boundary-device-b","page_count":999}]}`)
	var metrics protocol.MetricsBatchResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &metrics) != nil ||
		metrics.Success || metrics.Received != 3 || metrics.Stored != 2 || len(metrics.Failures) != 1 ||
		metrics.Failures[0].Index != 2 || metrics.Failures[0].Code != protocol.MetricsOwnershipConflict {
		t.Fatalf("shared metrics: %d %s", w.Code, w.Body.String())
	}
	latest, err := f.store.GetLatestMetrics(ctx, "boundary-device-a")
	if err != nil || latest == nil || latest.AgentID != replacement.AgentID || latest.PageCount != 123 {
		t.Fatalf("reporter attribution: %+v %v", latest, err)
	}
	rows := f.request(t, handleDevicesMetricsQuery, "operator", http.MethodPost, "/api/v1/devices/metrics/query",
		`{"serials":["boundary-device-a","replacement-extra","boundary-device-b"]}`)
	var result []storage.MetricsSnapshot
	if rows.Code != http.StatusOK || json.Unmarshal(rows.Body.Bytes(), &result) != nil || len(result) != 2 {
		t.Fatalf("tenant user metrics read: %d %s", rows.Code, rows.Body.String())
	}
	for _, metric := range result {
		if metric.AgentID != replacement.AgentID {
			t.Fatalf("read rewrote reporting identity: %+v", metric)
		}
	}
	enriched := enrichDevicesWithMetrics(ctx, []*storage.Device{ownerDevice})
	if len(enriched) != 1 || enriched[0].PageCount != 123 || enriched[0].AgentID != ownerDevice.AgentID {
		t.Fatalf("legacy list must enrich shared metrics without changing management owner: %+v", enriched)
	}
	if err := f.store.UpsertDeviceCredentials(ctx, &storage.DeviceCredentials{
		Serial: "boundary-device-a", TenantID: "boundary-a", Username: "private",
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/agents/device-credentials?serial=boundary-device-a", nil)
	r.Header.Set("Authorization", "Bearer "+replacement.Token)
	w = httptest.NewRecorder()
	requireAuth(handleAgentDeviceCredentials)(w, r)
	var credentialResult struct {
		Exists bool   `json:"exists"`
		Error  string `json:"error"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &credentialResult) != nil ||
		credentialResult.Exists || credentialResult.Error != "device not owned by this agent" || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("reporting must not grant credential access: %d %s", w.Code, w.Body.String())
	}
}
