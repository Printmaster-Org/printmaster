package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agentpkg "printmaster/agent/agent"
	"printmaster/agent/storage"
	"printmaster/common/protocol"
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
		_, _ = w.Write([]byte(`{"success":true,"received":1,"stored":1}`))
	}))
	defer server.Close()
	worker := NewUploadWorker(agentpkg.NewServerClient(server.URL, "agent-1", "test-token"), store, stubLogger{}, nil, UploadWorkerConfig{}, "")
	worker.doUpload()
	if len(routes) != 2 || routes[0] != "/api/v1/devices/batch" || routes[1] != "/api/v1/metrics/batch" {
		t.Fatalf("upload order = %v", routes)
	}
}

func TestUploadWorkerMetricsReceipt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		response   string
		transient  bool
		wantCalls  int32
		wantReject bool
	}{
		{"partial receipt", `{"success":false,"received":2,"stored":1,"failures":[{"index":1,"serial":"printer-2","code":"ownership_conflict"}]}`, false, 1, true},
		{"legacy partial receipt", `{"success":true,"received":2,"stored":1}`, false, 1, true},
		{"all rejected", `{"success":false,"received":2,"stored":0}`, false, 1, true},
		{"transport retry", `{"success":true,"received":2,"stored":2}`, true, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "metrics.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			for i := 1; i <= 2; i++ {
				device := &storage.Device{Serial: fmt.Sprintf("printer-%d", i), IP: fmt.Sprintf("192.0.2.%d", i), Visible: true}
				if err := store.Create(ctx, device); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveMetricsSnapshot(ctx, &storage.MetricsSnapshot{Serial: device.Serial, Timestamp: time.Now(), PageCount: 100}); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 && tc.transient {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			client := agentpkg.NewServerClient(server.URL, "agent-1", "test-token")
			worker := NewUploadWorker(client, store, stubLogger{}, nil, UploadWorkerConfig{}, "")
			worker.retryAttempts, worker.retryBackoff = 3, time.Millisecond
			err = worker.uploadMetrics()
			var rejected *agentpkg.MetricsBatchError
			if errors.As(err, &rejected) != tc.wantReject || (err != nil && !tc.wantReject) {
				t.Fatalf("upload error=%v, want rejection=%v", err, tc.wantReject)
			}
			if calls.Load() != tc.wantCalls {
				t.Fatalf("requests=%d, want %d", calls.Load(), tc.wantCalls)
			}
			for name, stats := range map[string]map[string]interface{}{"worker": worker.GetStats(), "client": client.GetStats()} {
				if stats["last_metrics_upload"].(time.Time).IsZero() != tc.wantReject {
					t.Fatalf("%s last success=%v, want rejection=%v", name, stats["last_metrics_upload"], tc.wantReject)
				}
			}
		})
	}
}

func TestUploadWorkerDoesNotReplayWrappedMetricsRejection(t *testing.T) {
	worker := &UploadWorker{logger: stubLogger{}, retryAttempts: 3, retryBackoff: time.Millisecond, stopCh: make(chan struct{})}
	rejection := &agentpkg.MetricsBatchError{Result: protocol.MetricsBatchResult{Received: 2, Stored: 1}}
	calls := 0
	err := worker.retryWithBackoff(func() error {
		calls++
		return fmt.Errorf("wrapped: %w", rejection)
	})
	if calls != 1 || !errors.Is(err, rejection) {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
