package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	agentpkg "printmaster/agent/agent"
	"printmaster/agent/storage"
	pmsettings "printmaster/common/settings"
)

type stubLogger struct{}

func (stubLogger) Error(string, ...interface{}) {}
func (stubLogger) Warn(string, ...interface{})  {}
func (stubLogger) Info(string, ...interface{})  {}
func (stubLogger) Debug(string, ...interface{}) {}

func TestUploadWorkerHandleHeartbeatSettingsPersistsSnapshot(t *testing.T) {
	store := newFakeConfigStore()
	mgr := NewSettingsManager(store)
	prev := settingsManager
	settingsManager = mgr
	t.Cleanup(func() { settingsManager = prev })

	worker := &UploadWorker{settings: mgr, logger: stubLogger{}}
	snap := &agentpkg.SettingsSnapshot{
		Version:       "v1",
		SchemaVersion: "schema-1",
		UpdatedAt:     time.Unix(500, 0),
		Settings:      pmsettings.DefaultSettings(),
	}

	worker.handleHeartbeatSettings(&agentpkg.HeartbeatResult{Snapshot: snap})
	if mgr.CurrentVersion() != "v1" {
		t.Fatalf("expected manager version to update")
	}
	if store.setCount(serverManagedSettingsKey) != 1 {
		t.Fatalf("expected snapshot persisted once, got %d", store.setCount(serverManagedSettingsKey))
	}

	worker.handleHeartbeatSettings(&agentpkg.HeartbeatResult{Snapshot: snap, SettingsVersion: snap.Version})
	if store.setCount(serverManagedSettingsKey) != 1 {
		t.Fatalf("expected no-op when version matches")
	}
}

func TestUploadWorkerHandleHeartbeatSettingsIgnoresNilSnapshot(t *testing.T) {
	worker := &UploadWorker{settings: nil, logger: stubLogger{}}
	worker.handleHeartbeatSettings(&agentpkg.HeartbeatResult{})
}

func TestUploadWorkerSendsRichInventoryBeforeMetrics(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "upload.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	device := &storage.Device{Serial: "EPSON-1", IP: "192.0.2.91", Visible: true, AssetNumber: "05046", Location: "Office",
		WebUIURL: "http://192.0.2.91", LastSeen: now, FirstSeen: now.Add(-24 * time.Hour), CreatedAt: now.Add(-24 * time.Hour),
		StatusMessages: []string{"Ready: Idle"}, RawData: map[string]interface{}{"is_inkjet": true,
			"vendor_metrics": map[string]interface{}{"epson_ready": true, "scan_adf_pages": 42}}}
	if err := store.Create(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	metrics := &storage.MetricsSnapshot{Serial: device.Serial, Timestamp: now, PageCount: 91845,
		TonerLevels: map[string]interface{}{"Photo Black Ink T44H1": 33, "supply_maintenance_box_2": 62}}
	if err := store.SaveMetricsSnapshot(context.Background(), metrics); err != nil {
		t.Fatal(err)
	}
	var routes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes = append(routes, r.URL.Path)
		var payload struct {
			Devices []storage.Device          `json:"devices"`
			Metrics []storage.MetricsSnapshot `json:"metrics"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/api/v1/devices/batch" {
			if len(payload.Devices) != 1 {
				t.Errorf("device count = %d", len(payload.Devices))
			} else if got := payload.Devices[0]; got.Location != device.Location || got.AssetNumber != device.AssetNumber ||
				got.WebUIURL != device.WebUIURL || !got.CreatedAt.Equal(device.CreatedAt) ||
				len(got.StatusMessages) != 1 || got.RawData["is_inkjet"] != true || got.RawData["vendor_metrics"] == nil {
				t.Errorf("incomplete upload: %+v", got)
			}
		} else if len(payload.Metrics) != 1 {
			t.Errorf("metrics count = %d", len(payload.Metrics))
		} else if got := payload.Metrics[0]; !got.Timestamp.Equal(now) || got.TonerLevels["Photo Black Ink T44H1"] != float64(33) ||
			got.TonerLevels["supply_maintenance_box_2"] != float64(62) {
			t.Errorf("incomplete metrics: %+v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"stored":1}`))
	}))
	defer server.Close()
	worker := NewUploadWorker(agentpkg.NewServerClient(server.URL, "agent-1", "test-token"), store, stubLogger{}, nil, UploadWorkerConfig{}, "")
	worker.doUpload()
	if len(routes) != 2 || routes[0] != "/api/v1/devices/batch" || routes[1] != "/api/v1/metrics/batch" {
		t.Fatalf("upload order = %v", routes)
	}
}
