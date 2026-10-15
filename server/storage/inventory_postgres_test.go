//go:build integration

package storage

import (
	"context"
	"strings"
	"testing"
)

func TestInventoryPostgres(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, s *PostgresStore) {
		testInventoryScope(t, &s.BaseStore)
		ctx := context.Background()
		// Cost-based plans on near-empty tables prefer sorting a handful of
		// rows; seed realistic history so the plan reflects production volume.
		if _, err := s.execContext(ctx, `INSERT INTO metrics_history (serial, agent_id, timestamp, page_count)
			SELECT d.serial, d.agent_id, NOW() - g * INTERVAL '1 minute', g
			FROM devices d CROSS JOIN generate_series(1, 5000) g WHERE d.serial = ?`, "device-a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.execContext(ctx, `ANALYZE metrics_history`); err != nil {
			t.Fatal(err)
		}
		rows, err := s.queryContext(ctx, `EXPLAIN SELECT `+inventoryPageCount+` FROM devices d WHERE d.serial=?`, "device-a")
		if err != nil {
			t.Fatal(err)
		}

		defer rows.Close()
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), "idx_metrics_shared_latest") || !strings.Contains(plan.String(), "Limit") || strings.Contains(plan.String(), "Aggregate") {
			t.Fatalf("expected indexed latest point, not history aggregation: %s", plan.String())
		}
	})
}

func TestSharedTelemetryPostgres(t *testing.T) {
	WithPostgresStore(t, func(t *testing.T, s *PostgresStore) {
		testSharedTelemetry(t, &s.BaseStore, s)
	})
}
