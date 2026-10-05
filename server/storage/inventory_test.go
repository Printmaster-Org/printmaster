package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInventorySQLite(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "inventory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testInventoryScope(t, s.BaseStore)
	rows, err := s.DB().QueryContext(context.Background(), `EXPLAIN QUERY PLAN SELECT `+inventoryPageCount+` FROM devices d WHERE d.serial=?`, "device-a")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "COVERING INDEX idx_metrics_owner_latest") || strings.Contains(plan.String(), "TEMP B-TREE") {
		t.Fatalf("page count must be covering indexed point lookup: %s", plan.String())
	}
}

// Identical scope/legacy-owner fixtures run against both real SQL dialects.
func testInventoryScope(t *testing.T, s *BaseStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, tenant := range []string{"tenant-a", "tenant-b", "tenant%_"} {
		if err := s.CreateTenant(ctx, &Tenant{ID: tenant, Name: tenant}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "literal", "unassigned"} {
		tenant := "tenant-" + id
		if id == "unassigned" {
			tenant = ""
		}
		if id == "literal" {
			tenant = "tenant%_"
		}
		if err := s.RegisterAgent(ctx, &Agent{AgentID: "agent-" + id, Token: "token-" + id, TenantID: tenant, RegisteredAt: now, LastSeen: now}); err != nil {
			t.Fatal(err)
		}
		d := &Device{AgentID: "agent-" + id}
		d.Serial, d.IP, d.LastSeen = "device-"+id, "192.0.2.1", now
		d.RawData = map[string]interface{}{"large": "not-in-index"}
		if err := s.UpsertDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 50; i++ {
			if err := s.SaveMetrics(ctx, &MetricsSnapshot{Serial: d.Serial, AgentID: d.AgentID, Timestamp: now.Add(time.Duration(i) * time.Second), PageCount: i + 1, ColorPages: 9, TonerLevels: map[string]interface{}{"black": 25}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Restored legacy metrics may have the same serial but a foreign agent.
	if _, err := s.execContext(ctx, `INSERT INTO metrics_history(serial,agent_id,timestamp,page_count,toner_levels) VALUES(?,?,?,?,?)`, "device-a", "agent-b", now.Add(time.Hour), 9999, `{"black":1}`); err != nil {
		t.Fatal(err)
	}
	// Equal timestamps choose highest page count, then other persisted values.
	if err := s.SaveMetrics(ctx, &MetricsSnapshot{Serial: "device-a", AgentID: "agent-a", Timestamp: now.Add(49 * time.Second), PageCount: 51}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		scope InventoryScope
		want  int
	}{
		{"empty", InventoryScope{}, 0},
		{"no-agents", InventoryScope{TenantIDs: []string{"missing"}}, 0},
		{"a", InventoryScope{TenantIDs: []string{"tenant-a"}}, 1},
		{"multi", InventoryScope{TenantIDs: []string{"tenant-a", "tenant-b"}}, 2},
		{"literal", InventoryScope{TenantIDs: []string{"tenant%_"}}, 1},
		{"admin", InventoryScope{Unrestricted: true}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, err := s.InventoryIndex(ctx, tc.scope)
			if err != nil || index == nil || len(index) != tc.want {
				t.Fatalf("index: %+v %v", index, err)
			}
			count, err := s.InventoryCount(ctx, tc.scope)
			if err != nil || count != int64(tc.want) {
				t.Fatalf("count: %d %v", count, err)
			}
			rows, err := s.InventoryRows(ctx, tc.scope, nil, 0, 0)
			if err != nil || rows == nil || len(rows) != tc.want {
				t.Fatalf("rows: %+v %v", rows, err)
			}
			for _, d := range rows {
				if d.TonerLevels != nil || d.ColorPages != 0 || d.LastMetricsAt != nil || d.RawData == nil {
					t.Fatalf("row projection: %+v", d)
				}
				if d.Serial == "device-a" && d.PageCount != 51 {
					t.Fatalf("foreign/tied page count: %+v", d)
				}
			}
			keys := []string{"device-a", "device-b", "device-literal", "device-unassigned", "absent", "device-a"}
			metrics, err := s.InventoryMetrics(ctx, tc.scope, keys)
			if err != nil || metrics == nil || len(metrics) != tc.want {
				t.Fatalf("metrics: %+v %v", metrics, err)
			}
			for _, m := range metrics {
				if m.Serial == "device-a" && (m.AgentID != "agent-a" || m.PageCount != 51) {
					t.Fatalf("wrong owner: %+v", m)
				}
			}
			page, err := s.InventoryRows(ctx, tc.scope, nil, 1, 1)
			want := 0
			if tc.want > 1 {
				want = 1
			}
			if err != nil || len(page) != want {
				t.Fatalf("page: %+v %v", page, err)
			}
			if want == 1 && page[0].Serial != rows[1].Serial {
				t.Fatal("unstable ordering")
			}
		})
	}
	metrics, err := s.GetLatestMetricsBatch(ctx, []string{"device-a", "absent"})
	if err != nil || len(metrics) != 1 || metrics["device-a"].PageCount != 51 || metrics["device-a"].AgentID != "agent-a" {
		t.Fatalf("legacy batch: %+v %v", metrics, err)
	}
	rows, err := s.InventoryRows(ctx, InventoryScope{TenantIDs: []string{"tenant-a"}}, []string{"device-a", "device-b", "absent"}, 0, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("mixed keys: %+v %v", rows, err)
	}
	rows, err = s.InventoryRows(ctx, InventoryScope{Unrestricted: true}, []string{}, 0, 0)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty keys widened: %+v %v", rows, err)
	}
	d := &Device{AgentID: "agent-a"}
	d.Serial, d.IP = "no-metrics", "192.0.2.2"
	if err := s.UpsertDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	rows, err = s.InventoryRows(ctx, InventoryScope{TenantIDs: []string{"tenant-a"}}, []string{d.Serial}, 0, 0)
	if err != nil || len(rows) != 1 || rows[0].PageCount != 0 {
		t.Fatalf("missing snapshot rows: %+v %v", rows, err)
	}
	metricsSlice, err := s.InventoryMetrics(ctx, InventoryScope{Unrestricted: true}, []string{d.Serial})
	if err != nil || metricsSlice == nil || len(metricsSlice) != 0 {
		t.Fatalf("missing snapshot leaked fabricated metrics: %+v %v", metricsSlice, err)
	}
}
