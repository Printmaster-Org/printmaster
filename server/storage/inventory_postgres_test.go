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
		rows, err := s.queryContext(context.Background(), `EXPLAIN SELECT `+inventoryPageCount+` FROM devices d WHERE d.serial=?`, "device-a")
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
		if !strings.Contains(plan.String(), "idx_metrics_owner_latest") || !strings.Contains(plan.String(), "Limit") || strings.Contains(plan.String(), "Aggregate") {
			t.Fatalf("expected indexed latest point, not history aggregation: %s", plan.String())
		}
	})
}
