package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedTelemetrySQLite(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testSharedTelemetry(t, s.BaseStore, s)
}

func testSharedTelemetry(t *testing.T, s *BaseStore, store Store) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"telemetry-tenant", "telemetry-foreign"} {
		if err := s.CreateTenant(ctx, &Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "c", "d", "e", "foreign", "unassigned", "unassigned-2"} {
		tenant := "telemetry-tenant"
		if id == "foreign" {
			tenant = "telemetry-foreign"
		}
		if id == "unassigned" || id == "unassigned-2" {
			tenant = ""
		}
		if err := s.RegisterAgent(ctx, &Agent{AgentID: "telemetry-" + id, Token: "token-" + id, TenantID: tenant, Status: "offline"}); err != nil {
			t.Fatal(err)
		}
		d := &Device{AgentID: "telemetry-" + id}
		d.Serial, d.IP, d.Model = "extra-"+id, "192.0.2.1", "original"
		if err := s.UpsertDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	device := &Device{AgentID: "telemetry-a"}
	device.Serial, device.IP, device.Model = "shared-printer", "192.0.2.10", "original"
	if err := s.UpsertDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDeviceCredentials(ctx, &DeviceCredentials{Serial: device.Serial, TenantID: "telemetry-tenant", Username: "owner", EncryptedPassword: "encrypted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditDeviceMetadata(ctx, device.Serial, device.AgentID, map[string]interface{}{"location": "pinned"}, "", false); err != nil {
		t.Fatal(err)
	}
	// PostgreSQL TIMESTAMPTZ stores microseconds.
	observedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	for _, id := range []string{"b", "c", "d", "e"} {
		secondary := *device
		secondary.AgentID, secondary.IP, secondary.Model, secondary.Location = "telemetry-"+id, "192.0.2.99", "overwritten", "untrusted"
		secondary.LastSeen = observedAt
		if err := s.UpsertDevice(ctx, &secondary); err != nil {
			t.Fatalf("secondary discovery %s: %v", id, err)
		}
	}
	gotDevice, err := s.GetDevice(ctx, device.Serial)
	if err != nil || gotDevice.AgentID != device.AgentID || gotDevice.IP != device.IP || gotDevice.Model != device.Model ||
		gotDevice.Location != "pinned" || !gotDevice.LastSeen.Equal(observedAt) {
		t.Fatalf("secondary discovery changed management inventory: %+v, %v", gotDevice, err)
	}
	stale := *device
	stale.AgentID, stale.LastSeen = "telemetry-b", observedAt.Add(-time.Hour)
	if err := s.UpsertDevice(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	gotDevice, err = s.GetDevice(ctx, device.Serial)
	if err != nil || !gotDevice.LastSeen.Equal(observedAt) {
		t.Fatalf("stale secondary discovery regressed liveness: %+v, %v", gotDevice, err)
	}
	creds, err := store.GetDeviceCredentials(ctx, device.Serial)
	if err != nil || creds.Username != "owner" || creds.EncryptedPassword != "encrypted" {
		t.Fatalf("credentials changed: %+v, %v", creds, err)
	}
	if err := s.DeleteDeviceForAgent(ctx, device.Serial, "telemetry-b"); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("shared reporter gained deletion rights: %v", err)
	}
	if _, err := s.EditDeviceMetadata(ctx, device.Serial, "telemetry-b", map[string]interface{}{"location": "changed"}, "", true); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("shared reporter gained metadata rights: %v", err)
	}
	for _, tc := range []struct{ serial, agent string }{
		{device.Serial, "telemetry-foreign"},
		{device.Serial, "telemetry-unassigned"},
		{device.Serial, "missing-agent"},
		{"extra-unassigned", "telemetry-unassigned-2"},
		{"missing-device", "telemetry-a"},
	} {
		if err := s.SaveMetrics(ctx, &MetricsSnapshot{Serial: tc.serial, AgentID: tc.agent, Timestamp: time.Now(), PageCount: 999}); !errors.Is(err, ErrOwnershipConflict) {
			t.Fatalf("unauthorized metrics %+v: %v", tc, err)
		}
		d := &Device{AgentID: tc.agent}
		d.Serial = tc.serial
		if tc.serial != "missing-device" {
			if err := s.UpsertDevice(ctx, d); !errors.Is(err, ErrOwnershipConflict) {
				t.Fatalf("unauthorized inventory %+v: %v", tc, err)
			}
		}
	}
	if err := s.SaveMetrics(ctx, &MetricsSnapshot{Serial: "extra-unassigned", AgentID: "telemetry-unassigned", Timestamp: time.Now(), PageCount: 1}); err != nil {
		t.Fatalf("legacy exact owner without tenant: %v", err)
	}
	since := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	points := []struct {
		agent string
		min   int
		count int
	}{
		{"a", -2, 110}, {"a", -3, 100}, {"b", 0, 105},
		{"c", 1, 115}, {"c", 1, 115}, {"d", 2, 108}, {"e", 3, 120}, {"b", 3, 119},
	}
	for _, p := range points {
		if err := s.SaveMetrics(ctx, &MetricsSnapshot{
			Serial: device.Serial, AgentID: "telemetry-" + p.agent,
			Timestamp: since.Add(time.Duration(p.min) * time.Minute),
			PageCount: p.count, ColorPages: p.count, MonoPages: p.count, ScanCount: p.count,
			TonerLevels: map[string]interface{}{"black": 42},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy/restored foreign observations must be excluded from every reader,
	// including seed baselines and bounds, not only from new writes.
	for _, offset := range []time.Duration{-time.Hour, time.Hour} {
		if _, err := s.execContext(ctx, `INSERT INTO metrics_history(serial,agent_id,timestamp,page_count,color_pages,mono_pages,scan_count)
			VALUES(?,?,?,?,?,?,?)`, device.Serial, "telemetry-foreign", since.Add(offset), 9999, 9999, 9999, 9999); err != nil {
			t.Fatal(err)
		}
	}
	checkLatest := func(name string, m *MetricsSnapshot, err error) {
		t.Helper()
		if err != nil || m == nil || m.PageCount != 120 || m.AgentID != "telemetry-e" {
			t.Fatalf("%s: %+v, %v", name, m, err)
		}
	}
	latest, err := s.GetLatestMetrics(ctx, device.Serial)
	checkLatest("latest", latest, err)
	at, err := s.GetMetricsAtOrBefore(ctx, device.Serial, since.Add(2*time.Hour))
	checkLatest("at or before", at, err)
	batch, err := s.GetLatestMetricsBatch(ctx, []string{device.Serial})
	checkLatest("latest batch", batch[device.Serial], err)
	lazy, err := s.InventoryMetrics(ctx, InventoryScope{TenantIDs: []string{"telemetry-tenant"}}, []string{device.Serial})
	if err != nil || len(lazy) != 1 {
		t.Fatalf("lazy metrics: %+v, %v", lazy, err)
	}
	checkLatest("lazy", lazy[0], nil)
	rows, err := s.InventoryRows(ctx, InventoryScope{TenantIDs: []string{"telemetry-tenant"}}, []string{device.Serial}, 0, 0)
	if err != nil || len(rows) != 1 || rows[0].PageCount != 120 || rows[0].AgentID != device.AgentID {
		t.Fatalf("inventory rows: %+v, %v", rows, err)
	}
	index, err := s.InventoryIndex(ctx, InventoryScope{TenantIDs: []string{"telemetry-tenant"}})
	if err != nil || len(index) != 6 {
		t.Fatalf("five reporters and one shared printer should yield six devices: %+v, %v", index, err)
	}
	history, err := s.GetMetricsHistory(ctx, device.Serial, since.Add(-24*time.Hour))
	if err != nil || len(history) != len(points) {
		t.Fatalf("eligible history: %+v, %v", history, err)
	}
	minTS, maxTS, count, err := s.GetMetricsBounds(ctx, device.Serial)
	if err != nil || count != int64(len(points)) || !minTS.Equal(since.Add(-3*time.Minute)) || !maxTS.Equal(since.Add(3*time.Minute)) {
		t.Fatalf("bounds: %v %v %d %v", minTS, maxTS, count, err)
	}
	for _, filter := range []AggregatedMetricsFilter{
		{}, {TenantIDs: []string{"telemetry-tenant"}}, {AgentID: device.AgentID}, {DeviceSerial: device.Serial},
	} {
		agg, err := s.GetAggregatedMetrics(ctx, since, filter)
		if err != nil {
			t.Fatal(err)
		}
		for name, series := range map[string][]MetricSeriesPoint{
			"pages": agg.Fleet.History.TotalImpressions, "color": agg.Fleet.History.ColorImpressions,
			"mono": agg.Fleet.History.MonoImpressions, "scans": agg.Fleet.History.ScanVolume,
		} {
			var total int64
			for _, point := range series {
				total += point.Value
			}
			if total != 10 {
				t.Fatalf("%s filter=%+v usage=%d, want 10 (not sum/replayed deltas)", name, filter, total)
			}
		}
		wantPages := int64(120)
		if len(filter.TenantIDs) == 0 && filter.AgentID == "" && filter.DeviceSerial == "" {
			wantPages++
		}
		if agg.Fleet.Totals.PageCount != wantPages {
			t.Fatalf("current page count duplicated/foreign: got %d want %d", agg.Fleet.Totals.PageCount, wantPages)
		}
	}
	// A reporting Agent changing tenants must no longer contribute to reads.
	if _, err := s.execContext(ctx, `UPDATE agents SET tenant_id = ? WHERE agent_id = ?`, "telemetry-foreign", "telemetry-e"); err != nil {
		t.Fatal(err)
	}
	latest, err = s.GetLatestMetrics(ctx, device.Serial)
	if err != nil || latest == nil || latest.AgentID != "telemetry-b" || latest.PageCount != 119 {
		t.Fatalf("moved reporter still visible: %+v %v", latest, err)
	}
}

func TestSharedTelemetryCounterResets(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "single reporter", true: "shared conservative"}[shared], func(t *testing.T) {
			s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "reset.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			for _, id := range []string{"a", "b"} {
				if err := s.RegisterAgent(ctx, &Agent{AgentID: id, Token: id, TenantID: "same"}); err != nil {
					t.Fatal(err)
				}
			}
			d := &Device{AgentID: "a"}
			d.Serial = "reset"
			if err := s.UpsertDevice(ctx, d); err != nil {
				t.Fatal(err)
			}
			since := time.Now().UTC().Truncate(time.Hour)
			for i, value := range []int{100, 1, 11} {
				reporter := "a"
				if shared && i == 1 {
					reporter = "b"
				}
				if err := s.SaveMetrics(ctx, &MetricsSnapshot{Serial: d.Serial, AgentID: reporter, Timestamp: since.Add(time.Duration(i-1) * time.Minute), PageCount: value}); err != nil {
					t.Fatal(err)
				}
			}
			agg, err := s.GetAggregatedMetrics(ctx, since, AggregatedMetricsFilter{})
			if err != nil {
				t.Fatal(err)
			}
			want := int64(10)
			if shared {
				want = 0
			}
			var total int64
			for _, point := range agg.Fleet.History.TotalImpressions {
				total += point.Value
			}
			if total != want {
				t.Fatalf("reset usage=%d want=%d", total, want)
			}
		})
	}
}
