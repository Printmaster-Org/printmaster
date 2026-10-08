package storage

import (
	"context"
	"testing"
	"time"
)

func TestSQLiteStore_GetTieredMetricsBounds_SQLiteTimestamps(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	serial := "SQLITE_TIMESTAMP_BOUNDS"
	if err := store.Create(ctx, newFullTestDevice(serial, "192.168.1.201", "HP", "LaserJet", true, true)); err != nil {
		t.Fatal(err)
	}
	for _, timestamp := range []string{"2026-10-01 12:30:00", "2026-10-08T12:30:00.123456789Z"} {
		if _, err := store.db.ExecContext(ctx, "INSERT INTO metrics_raw (serial, timestamp) VALUES (?, ?)", serial, timestamp); err != nil {
			t.Fatal(err)
		}
	}
	minTS, maxTS, count, err := store.GetTieredMetricsBounds(ctx, serial)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || minTS.Format(time.RFC3339) != "2026-10-01T12:30:00Z" || maxTS.Format(time.RFC3339Nano) != "2026-10-08T12:30:00.123456789Z" {
		t.Fatalf("unexpected bounds: min=%s max=%s count=%d", minTS, maxTS, count)
	}
}

func TestParseMetricsTimestamp(t *testing.T) {
	for _, value := range []string{"2026-10-01T12:30:00Z", "2026-10-01 12:30:00",
		"2026-10-01 12:30:00.123456789+00:00", "2026-10-01 14:30:00+02:00"} {
		parsed, err := parseMetricsTimestamp(value)
		if err != nil || parsed.UTC().Format(time.RFC3339) != "2026-10-01T12:30:00Z" {
			t.Fatalf("parseMetricsTimestamp(%q) = %s, %v", value, parsed, err)
		}
	}
	if _, err := parseMetricsTimestamp("not a timestamp"); err == nil {
		t.Fatal("invalid timestamps must surface an error")
	}
}

func TestSQLiteStore_GetTieredMetricsHistory_RangeBounded(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	serial := "TEST_METRICS_RANGE"

	// Seed a minimal device to satisfy foreign key expectations in some schemas.
	dev := newFullTestDevice(serial, "192.168.1.200", "HP", "LaserJet", true, true)
	if err := store.Create(ctx, dev); err != nil {
		t.Fatalf("Failed to create device: %v", err)
	}

	// Use a base time relative to now so the tier selection logic works correctly.
	// GetTieredMetricsHistory determines tiers based on time.Now(), so we need
	// test data within the "raw" tier window (last 7 days).
	base := time.Now().UTC().Truncate(time.Hour)

	// Helper inserts directly into tier tables using the store's DB.
	insertRaw := func(ts time.Time, pages int) {
		t.Helper()
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO metrics_raw (serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			serial, ts.Format(time.RFC3339Nano), pages, 0, 0, 0, `{}`,
		)
		if err != nil {
			t.Fatalf("Failed to insert raw: %v", err)
		}
	}
	insertHourly := func(ts time.Time, pages int) {
		t.Helper()
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO metrics_hourly (serial, hour_start, sample_count, page_count_min, page_count_max, page_count_avg, color_pages_min, color_pages_max, color_pages_avg, mono_pages_min, mono_pages_max, mono_pages_avg, scan_count_min, scan_count_max, scan_count_avg, toner_levels_avg) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			serial, ts.Format(time.RFC3339Nano), 1,
			pages, pages, pages,
			0, 0, 0,
			0, 0, 0,
			0, 0, 0,
			`{}`,
		)
		if err != nil {
			t.Fatalf("Failed to insert hourly: %v", err)
		}
	}
	insertDaily := func(ts time.Time, pages int) {
		t.Helper()
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO metrics_daily (serial, day_start, sample_count, page_count_min, page_count_max, page_count_avg, color_pages_min, color_pages_max, color_pages_avg, mono_pages_min, mono_pages_max, mono_pages_avg, scan_count_min, scan_count_max, scan_count_avg, toner_levels_avg) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			serial, ts.Format(time.RFC3339Nano), 1,
			pages, pages, pages,
			0, 0, 0,
			0, 0, 0,
			0, 0, 0,
			`{}`,
		)
		if err != nil {
			t.Fatalf("Failed to insert daily: %v", err)
		}
	}
	insertMonthly := func(ts time.Time, pages int) {
		t.Helper()
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO metrics_monthly (serial, month_start, sample_count, page_count_min, page_count_max, page_count_avg, color_pages_min, color_pages_max, color_pages_avg, mono_pages_min, mono_pages_max, mono_pages_avg, scan_count_min, scan_count_max, scan_count_avg, toner_levels_avg) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			serial, ts.Format(time.RFC3339Nano), 1,
			pages, pages, pages,
			0, 0, 0,
			0, 0, 0,
			0, 0, 0,
			`{}`,
		)
		if err != nil {
			t.Fatalf("Failed to insert monthly: %v", err)
		}
	}

	// Insert points inside and outside the window for each tier.
	insertRaw(base.Add(-2*time.Hour), 10)          // outside
	insertRaw(base.Add(1*time.Hour), 20)           // inside
	insertHourly(base.Add(-48*time.Hour), 100)     // outside
	insertHourly(base.Add(24*time.Hour), 200)      // inside
	insertDaily(base.Add(-48*time.Hour), 1000)     // outside
	insertDaily(base.Add(72*time.Hour), 2000)      // inside
	insertMonthly(base.Add(-720*time.Hour), 10000) // outside
	insertMonthly(base.Add(720*time.Hour), 20000)  // inside

	since := base
	until := base.Add(10 * 24 * time.Hour)

	got, err := store.GetTieredMetricsHistory(ctx, serial, since, until)
	if err != nil {
		t.Fatalf("GetTieredMetricsHistory returned error: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("Expected non-empty history")
	}

	for _, snap := range got {
		if snap.Timestamp.Before(since) || snap.Timestamp.After(until) {
			t.Fatalf("Returned snapshot out of range: ts=%s since=%s until=%s tier=%s", snap.Timestamp, since, until, snap.Tier)
		}
		if snap.Tier == "" {
			t.Fatalf("Expected Tier to be set")
		}
	}
}

func TestAverageTonerLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected map[string]interface{}
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]interface{}{},
		},
		{
			name:     "single sample",
			input:    `{"black":80,"cyan":60,"magenta":40,"yellow":20}`,
			expected: map[string]interface{}{"black": 80, "cyan": 60, "magenta": 40, "yellow": 20},
		},
		{
			name:     "two samples average",
			input:    `{"black":100,"cyan":80},{"black":50,"cyan":40}`,
			expected: map[string]interface{}{"black": 75, "cyan": 60},
		},
		{
			name:     "three samples average",
			input:    `{"black":90},{"black":60},{"black":30}`,
			expected: map[string]interface{}{"black": 60},
		},
		{
			name:     "mixed colors across samples",
			input:    `{"black":100},{"black":80,"cyan":60},{"cyan":40}`,
			expected: map[string]interface{}{"black": 90, "cyan": 50},
		},
		{
			name:     "escaped quotes in JSON",
			input:    `{"black":100},{"black":50}`,
			expected: map[string]interface{}{"black": 75},
		},
		{
			name:     "whitespace in GROUP_CONCAT output",
			input:    ` {"black":100} , {"black":50} `,
			expected: map[string]interface{}{"black": 75},
		},
		{
			name:     "nested JSON in toner (unusual but should handle)",
			input:    `{"black":100},{"black":{"level":50}}`,
			expected: map[string]interface{}{"black": 100}, // only numeric values counted
		},
		{
			name:     "malformed JSON skipped",
			input:    `{"black":100},invalid,{"black":50}`,
			expected: map[string]interface{}{"black": 75},
		},
		{
			name:     "empty objects",
			input:    `{},{"black":50},{}`,
			expected: map[string]interface{}{"black": 50},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := averageTonerLevels(tc.input)
			if len(result) != len(tc.expected) {
				t.Errorf("expected %d keys, got %d: %v", len(tc.expected), len(result), result)
				return
			}
			for k, want := range tc.expected {
				got, ok := result[k]
				if !ok {
					t.Errorf("missing key %q", k)
					continue
				}
				if got != want {
					t.Errorf("key %q: expected %v, got %v", k, want, got)
				}
			}
		})
	}
}

func TestSQLiteStore_DownsampleRawToHourly_UpsertPreservesRowID(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	serial := "TEST_DOWNSAMPLE_UPSERT"

	dev := newFullTestDevice(serial, "192.168.1.210", "HP", "LaserJet", true, true)
	if err := store.Create(ctx, dev); err != nil {
		t.Fatalf("Failed to create device: %v", err)
	}

	base := time.Date(2025, 1, 1, 0, 5, 0, 0, time.UTC)
	insertRaw := func(ts time.Time, pages int) {
		t.Helper()
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO metrics_raw (serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			serial, ts.Format(time.RFC3339Nano), pages, 0, 0, 0, `{"black":50}`,
		)
		if err != nil {
			t.Fatalf("Failed to insert raw: %v", err)
		}
	}
	insertRaw(base, 10)
	insertRaw(base.Add(5*time.Minute), 20)

	olderThan := base.Add(2 * time.Hour)
	if _, err := store.DownsampleRawToHourly(ctx, olderThan); err != nil {
		t.Fatalf("DownsampleRawToHourly returned error: %v", err)
	}

	var id1 int64
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM metrics_hourly WHERE serial = ?`, serial).Scan(&id1); err != nil {
		t.Fatalf("Failed to query hourly row id: %v", err)
	}

	if _, err := store.DownsampleRawToHourly(ctx, olderThan); err != nil {
		t.Fatalf("DownsampleRawToHourly (rerun) returned error: %v", err)
	}

	var id2 int64
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM metrics_hourly WHERE serial = ?`, serial).Scan(&id2); err != nil {
		t.Fatalf("Failed to query hourly row id after rerun: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected hourly row id to be stable across reruns (UPSERT), got %d then %d", id1, id2)
	}

	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics_hourly WHERE serial = ?`, serial).Scan(&rows); err != nil {
		t.Fatalf("Failed to query hourly row count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected exactly 1 hourly row, got %d", rows)
	}
}

func TestSQLiteStore_DownsampleRawToHourly_RollsBackOnInsertFailure(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	serialA := "TEST_DOWNSAMPLE_TX_A"
	serialB := "TEST_DOWNSAMPLE_TX_B"

	if err := store.Create(ctx, newFullTestDevice(serialA, "192.168.1.211", "HP", "LaserJet", true, true)); err != nil {
		t.Fatalf("Failed to create device A: %v", err)
	}
	if err := store.Create(ctx, newFullTestDevice(serialB, "192.168.1.212", "HP", "LaserJet", true, true)); err != nil {
		t.Fatalf("Failed to create device B: %v", err)
	}

	base := time.Date(2025, 1, 1, 0, 5, 0, 0, time.UTC)
	insertRaw := func(serial string, ts time.Time, pages int) {
		t.Helper()
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO metrics_raw (serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			serial, ts.Format(time.RFC3339Nano), pages, 0, 0, 0, `{}`,
		)
		if err != nil {
			t.Fatalf("Failed to insert raw for %s: %v", serial, err)
		}
	}
	insertRaw(serialA, base, 10)
	insertRaw(serialB, base, 20)

	_, err = store.db.ExecContext(ctx, `
		CREATE TRIGGER abort_on_b
		BEFORE INSERT ON metrics_hourly
		WHEN NEW.serial = 'TEST_DOWNSAMPLE_TX_B'
		BEGIN
			SELECT RAISE(ABORT, 'boom');
		END;
	`)
	if err != nil {
		t.Fatalf("Failed to create trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS abort_on_b`)
	})

	olderThan := base.Add(2 * time.Hour)
	if _, err := store.DownsampleRawToHourly(ctx, olderThan); err == nil {
		t.Fatalf("expected DownsampleRawToHourly to fail due to trigger, but got nil")
	}

	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metrics_hourly`).Scan(&rows); err != nil {
		t.Fatalf("Failed to query hourly row count: %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected transaction rollback (0 hourly rows), got %d", rows)
	}
}
