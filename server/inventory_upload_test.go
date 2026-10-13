package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestInventoryUploadPreservesEpsonFactsAndRefreshesStatus(t *testing.T) {
	server, store := setupTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	agent := &storage.Agent{AgentID: "epson-agent", Token: "epson-upload-test", RegisteredAt: now, LastSeen: now, Status: "active"}
	if err := store.RegisterAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	device := &storage.Device{AgentID: "untrusted-owner"}
	device.Serial, device.IP, device.Manufacturer, device.Model = "EPSON-SYNC-1", "192.0.2.91", "Epson", "AM-C550"
	device.AssetNumber, device.Location, device.Description, device.WebUIURL = "05046", "Joe's Office", "Office MFP", "http://192.0.2.91"
	device.SubnetMask, device.Gateway = "255.255.255.0", "192.0.2.1"
	device.Consumables, device.StatusMessages = []string{"Black Ink Cartridge T08M1"}, []string{"Error: Paper jam (Epson error 4)"}
	device.LastSeen, device.FirstSeen, device.CreatedAt = now.Add(-time.Minute), now.Add(-24*time.Hour), now.Add(-24*time.Hour)
	device.RawData = map[string]interface{}{
		"is_inkjet": true, "is_color": true, "is_copier": true,
		"meters":         map[string]interface{}{"copy_pages": 123, "duplex_sheets": 456},
		"vendor_metrics": map[string]interface{}{"epson_status": "Paper jam", "epson_error": "Paper jam"},
	}
	upload := func(route, field string, records interface{}) {
		t.Helper()
		body, err := json.Marshal(map[string]interface{}{"agent_id": agent.AgentID, "timestamp": now, field: records})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+route, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+agent.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result struct{ Stored int }
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || result.Stored != 1 {
			t.Fatalf("upload %s: status %d, stored %d", route, resp.StatusCode, result.Stored)
		}
	}
	upload("/api/v1/devices/batch", "devices", []*storage.Device{device})
	assertDevice := func() {
		t.Helper()
		got, err := store.GetDevice(ctx, device.Serial)
		if err != nil {
			t.Fatal(err)
		}
		if got.AgentID != agent.AgentID || got.AssetNumber != device.AssetNumber || got.Location != device.Location ||
			got.Description != device.Description || got.WebUIURL != device.WebUIURL || got.SubnetMask != device.SubnetMask ||
			got.Gateway != device.Gateway || !got.LastSeen.Equal(device.LastSeen) || !got.FirstSeen.Equal(device.FirstSeen) ||
			!slices.Equal(got.Consumables, device.Consumables) || !slices.Equal(got.StatusMessages, device.StatusMessages) ||
			got.RawData["is_inkjet"] != true || got.RawData["raw_data"] != nil {
			t.Fatalf("inventory lost uploaded facts: %+v", got)
		}
		if !reflect.DeepEqual(got.RawData["vendor_metrics"], device.RawData["vendor_metrics"]) {
			t.Fatalf("vendor state mismatch: %v", got.RawData)
		}
	}
	assertDevice()
	device.LastSeen = now
	device.Location, device.AssetNumber = "New room", "05047"
	device.StatusMessages = []string{}
	device.RawData["vendor_metrics"] = map[string]interface{}{"epson_status": "Idle", "epson_ready": true}
	upload("/api/v1/devices/batch", "devices", []*storage.Device{device})
	assertDevice()
	at := now.Add(-30 * time.Second)
	metrics := &storage.MetricsSnapshot{AgentID: "untrusted-owner", Serial: device.Serial, Timestamp: at, PageCount: 91845,
		TonerLevels: map[string]interface{}{"ink_black": 33, "supply_maintenance_box": 75}}
	upload("/api/v1/metrics/batch", "metrics", []*storage.MetricsSnapshot{metrics})
	got, err := store.GetLatestMetrics(ctx, device.Serial)
	if err != nil || got == nil || got.AgentID != agent.AgentID || !got.Timestamp.Equal(at) ||
		got.TonerLevels["ink_black"] != float64(33) || got.TonerLevels["supply_maintenance_box"] != float64(75) {
		t.Fatalf("metrics lost sample time/supplies: %+v, %v", got, err)
	}
}
