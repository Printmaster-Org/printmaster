package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AggregatedMetrics represents aggregated metrics with min/max/avg values
type AggregatedMetrics struct {
	ID           int64                  `json:"id"`
	Serial       string                 `json:"serial"`
	BucketStart  time.Time              `json:"bucket_start"`
	SampleCount  int                    `json:"sample_count"`
	PageCountMin int                    `json:"page_count_min"`
	PageCountMax int                    `json:"page_count_max"`
	PageCountAvg int                    `json:"page_count_avg"`
	ColorMin     int                    `json:"color_pages_min"`
	ColorMax     int                    `json:"color_pages_max"`
	ColorAvg     int                    `json:"color_pages_avg"`
	MonoMin      int                    `json:"mono_pages_min"`
	MonoMax      int                    `json:"mono_pages_max"`
	MonoAvg      int                    `json:"mono_pages_avg"`
	ScanMin      int                    `json:"scan_count_min"`
	ScanMax      int                    `json:"scan_count_max"`
	ScanAvg      int                    `json:"scan_count_avg"`
	TonerAvg     map[string]interface{} `json:"toner_levels_avg"`
}

// DownsampleRawToHourly aggregates raw 5-minute metrics into hourly buckets
// Returns the number of buckets created and any error
func (s *SQLiteStore) DownsampleRawToHourly(ctx context.Context, olderThan time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction for hourly aggregation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Query raw metrics grouped by serial and hour
	query := `
		SELECT 
			serial,
			strftime('%Y-%m-%d %H:00:00', timestamp) as hour_start,
			COUNT(*) as sample_count,
			MIN(page_count) as page_count_min,
			MAX(page_count) as page_count_max,
			CAST(AVG(page_count) AS INTEGER) as page_count_avg,
			MIN(color_pages) as color_min,
			MAX(color_pages) as color_max,
			CAST(AVG(color_pages) AS INTEGER) as color_avg,
			MIN(mono_pages) as mono_min,
			MAX(mono_pages) as mono_max,
			CAST(AVG(mono_pages) AS INTEGER) as mono_avg,
			MIN(scan_count) as scan_min,
			MAX(scan_count) as scan_max,
			CAST(AVG(scan_count) AS INTEGER) as scan_avg,
			GROUP_CONCAT(toner_levels) as toner_samples
		FROM metrics_raw
		WHERE timestamp < ?
		GROUP BY serial, hour_start
		ORDER BY serial, hour_start
	`

	// Use RFC3339Nano UTC string for time parameter to match stored timestamp format
	olderStr := olderThan.UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, query, olderStr)
	if err != nil {
		return 0, fmt.Errorf("failed to query raw metrics for hourly aggregation: %w", err)
	}
	defer rows.Close()

	insertQuery := `
		INSERT INTO metrics_hourly (
			serial, hour_start, sample_count,
			page_count_min, page_count_max, page_count_avg,
			color_pages_min, color_pages_max, color_pages_avg,
			mono_pages_min, mono_pages_max, mono_pages_avg,
			scan_count_min, scan_count_max, scan_count_avg,
			toner_levels_avg
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(serial, hour_start) DO UPDATE SET
			sample_count=excluded.sample_count,
			page_count_min=excluded.page_count_min,
			page_count_max=excluded.page_count_max,
			page_count_avg=excluded.page_count_avg,
			color_pages_min=excluded.color_pages_min,
			color_pages_max=excluded.color_pages_max,
			color_pages_avg=excluded.color_pages_avg,
			mono_pages_min=excluded.mono_pages_min,
			mono_pages_max=excluded.mono_pages_max,
			mono_pages_avg=excluded.mono_pages_avg,
			scan_count_min=excluded.scan_count_min,
			scan_count_max=excluded.scan_count_max,
			scan_count_avg=excluded.scan_count_avg,
			toner_levels_avg=excluded.toner_levels_avg
	`

	stmt, err := tx.PrepareContext(ctx, insertQuery)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare hourly upsert: %w", err)
	}
	defer stmt.Close()

	count := 0
	for rows.Next() {
		var serial, hourStartStr, tonerSamplesStr string
		var sampleCount, pageMin, pageMax, pageAvg, colorMin, colorMax, colorAvg int
		var monoMin, monoMax, monoAvg, scanMin, scanMax, scanAvg int

		err := rows.Scan(
			&serial, &hourStartStr, &sampleCount,
			&pageMin, &pageMax, &pageAvg,
			&colorMin, &colorMax, &colorAvg,
			&monoMin, &monoMax, &monoAvg,
			&scanMin, &scanMax, &scanAvg,
			&tonerSamplesStr,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to scan hourly aggregate row: %w", err)
		}

		// Parse hour_start timestamp
		hourStart, err := time.Parse("2006-01-02 15:04:05", hourStartStr)
		if err != nil {
			return 0, fmt.Errorf("failed to parse hour_start %q: %w", hourStartStr, err)
		}

		// Average toner levels across samples
		tonerAvg := averageTonerLevels(tonerSamplesStr)
		tonerJSON, _ := json.Marshal(tonerAvg)

		_, err = stmt.ExecContext(ctx,
			serial, hourStart.Format(time.RFC3339Nano), sampleCount,
			pageMin, pageMax, pageAvg,
			colorMin, colorMax, colorAvg,
			monoMin, monoMax, monoAvg,
			scanMin, scanMax, scanAvg,
			string(tonerJSON),
		)

		if err != nil {
			return 0, fmt.Errorf("failed to upsert hourly aggregate (serial=%s hour=%s): %w", serial, hourStartStr, err)
		}

		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("hourly aggregation row iteration failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit hourly aggregation: %w", err)
	}

	if storageLogger != nil && count > 0 {
		storageLogger.Info("Downsampled raw metrics to hourly", "buckets_created", count, "older_than", olderThan)
	}

	return count, nil
}

// DownsampleHourlyToDaily aggregates hourly metrics into daily buckets
func (s *SQLiteStore) DownsampleHourlyToDaily(ctx context.Context, olderThan time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction for daily aggregation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		SELECT 
			serial,
			strftime('%Y-%m-%d 00:00:00', hour_start) as day_start,
			SUM(sample_count) as sample_count,
			MIN(page_count_min) as page_count_min,
			MAX(page_count_max) as page_count_max,
			CAST(AVG(page_count_avg) AS INTEGER) as page_count_avg,
			MIN(color_pages_min) as color_min,
			MAX(color_pages_max) as color_max,
			CAST(AVG(color_pages_avg) AS INTEGER) as color_avg,
			MIN(mono_pages_min) as mono_min,
			MAX(mono_pages_max) as mono_max,
			CAST(AVG(mono_pages_avg) AS INTEGER) as mono_avg,
			MIN(scan_count_min) as scan_min,
			MAX(scan_count_max) as scan_max,
			CAST(AVG(scan_count_avg) AS INTEGER) as scan_avg,
			GROUP_CONCAT(toner_levels_avg) as toner_samples
		FROM metrics_hourly
		WHERE hour_start < ?
		GROUP BY serial, day_start
		ORDER BY serial, day_start
	`

	olderStr := olderThan.UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, query, olderStr)
	if err != nil {
		return 0, fmt.Errorf("failed to query hourly metrics for daily aggregation: %w", err)
	}
	defer rows.Close()

	insertQuery := `
		INSERT INTO metrics_daily (
			serial, day_start, sample_count,
			page_count_min, page_count_max, page_count_avg,
			color_pages_min, color_pages_max, color_pages_avg,
			mono_pages_min, mono_pages_max, mono_pages_avg,
			scan_count_min, scan_count_max, scan_count_avg,
			toner_levels_avg
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(serial, day_start) DO UPDATE SET
			sample_count=excluded.sample_count,
			page_count_min=excluded.page_count_min,
			page_count_max=excluded.page_count_max,
			page_count_avg=excluded.page_count_avg,
			color_pages_min=excluded.color_pages_min,
			color_pages_max=excluded.color_pages_max,
			color_pages_avg=excluded.color_pages_avg,
			mono_pages_min=excluded.mono_pages_min,
			mono_pages_max=excluded.mono_pages_max,
			mono_pages_avg=excluded.mono_pages_avg,
			scan_count_min=excluded.scan_count_min,
			scan_count_max=excluded.scan_count_max,
			scan_count_avg=excluded.scan_count_avg,
			toner_levels_avg=excluded.toner_levels_avg
	`

	stmt, err := tx.PrepareContext(ctx, insertQuery)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare daily upsert: %w", err)
	}
	defer stmt.Close()

	count := 0
	for rows.Next() {
		var serial, dayStartStr, tonerSamplesStr string
		var sampleCount, pageMin, pageMax, pageAvg, colorMin, colorMax, colorAvg int
		var monoMin, monoMax, monoAvg, scanMin, scanMax, scanAvg int

		err := rows.Scan(
			&serial, &dayStartStr, &sampleCount,
			&pageMin, &pageMax, &pageAvg,
			&colorMin, &colorMax, &colorAvg,
			&monoMin, &monoMax, &monoAvg,
			&scanMin, &scanMax, &scanAvg,
			&tonerSamplesStr,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to scan daily aggregate row: %w", err)
		}

		dayStart, err := time.Parse("2006-01-02 15:04:05", dayStartStr)
		if err != nil {
			return 0, fmt.Errorf("failed to parse day_start %q: %w", dayStartStr, err)
		}

		tonerAvg := averageTonerLevels(tonerSamplesStr)
		tonerJSON, _ := json.Marshal(tonerAvg)

		_, err = stmt.ExecContext(ctx,
			serial, dayStart.Format(time.RFC3339Nano), sampleCount,
			pageMin, pageMax, pageAvg,
			colorMin, colorMax, colorAvg,
			monoMin, monoMax, monoAvg,
			scanMin, scanMax, scanAvg,
			string(tonerJSON),
		)

		if err != nil {
			return 0, fmt.Errorf("failed to upsert daily aggregate (serial=%s day=%s): %w", serial, dayStartStr, err)
		}

		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("daily aggregation row iteration failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit daily aggregation: %w", err)
	}

	if storageLogger != nil && count > 0 {
		storageLogger.Info("Downsampled hourly metrics to daily", "buckets_created", count, "older_than", olderThan)
	}

	return count, nil
}

// DownsampleDailyToMonthly aggregates daily metrics into monthly buckets
func (s *SQLiteStore) DownsampleDailyToMonthly(ctx context.Context, olderThan time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction for monthly aggregation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		SELECT 
			serial,
			strftime('%Y-%m-01 00:00:00', day_start) as month_start,
			SUM(sample_count) as sample_count,
			MIN(page_count_min) as page_count_min,
			MAX(page_count_max) as page_count_max,
			CAST(AVG(page_count_avg) AS INTEGER) as page_count_avg,
			MIN(color_pages_min) as color_min,
			MAX(color_pages_max) as color_max,
			CAST(AVG(color_pages_avg) AS INTEGER) as color_avg,
			MIN(mono_pages_min) as mono_min,
			MAX(mono_pages_max) as mono_max,
			CAST(AVG(mono_pages_avg) AS INTEGER) as mono_avg,
			MIN(scan_count_min) as scan_min,
			MAX(scan_count_max) as scan_max,
			CAST(AVG(scan_count_avg) AS INTEGER) as scan_avg,
			GROUP_CONCAT(toner_levels_avg) as toner_samples
		FROM metrics_daily
		WHERE day_start < ?
		GROUP BY serial, month_start
		ORDER BY serial, month_start
	`

	olderStr := olderThan.UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, query, olderStr)
	if err != nil {
		return 0, fmt.Errorf("failed to query daily metrics for monthly aggregation: %w", err)
	}
	defer rows.Close()

	insertQuery := `
		INSERT INTO metrics_monthly (
			serial, month_start, sample_count,
			page_count_min, page_count_max, page_count_avg,
			color_pages_min, color_pages_max, color_pages_avg,
			mono_pages_min, mono_pages_max, mono_pages_avg,
			scan_count_min, scan_count_max, scan_count_avg,
			toner_levels_avg
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(serial, month_start) DO UPDATE SET
			sample_count=excluded.sample_count,
			page_count_min=excluded.page_count_min,
			page_count_max=excluded.page_count_max,
			page_count_avg=excluded.page_count_avg,
			color_pages_min=excluded.color_pages_min,
			color_pages_max=excluded.color_pages_max,
			color_pages_avg=excluded.color_pages_avg,
			mono_pages_min=excluded.mono_pages_min,
			mono_pages_max=excluded.mono_pages_max,
			mono_pages_avg=excluded.mono_pages_avg,
			scan_count_min=excluded.scan_count_min,
			scan_count_max=excluded.scan_count_max,
			scan_count_avg=excluded.scan_count_avg,
			toner_levels_avg=excluded.toner_levels_avg
	`

	stmt, err := tx.PrepareContext(ctx, insertQuery)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare monthly upsert: %w", err)
	}
	defer stmt.Close()

	count := 0
	for rows.Next() {
		var serial, monthStartStr, tonerSamplesStr string
		var sampleCount, pageMin, pageMax, pageAvg, colorMin, colorMax, colorAvg int
		var monoMin, monoMax, monoAvg, scanMin, scanMax, scanAvg int

		err := rows.Scan(
			&serial, &monthStartStr, &sampleCount,
			&pageMin, &pageMax, &pageAvg,
			&colorMin, &colorMax, &colorAvg,
			&monoMin, &monoMax, &monoAvg,
			&scanMin, &scanMax, &scanAvg,
			&tonerSamplesStr,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to scan monthly aggregate row: %w", err)
		}

		monthStart, err := time.Parse("2006-01-02 15:04:05", monthStartStr)
		if err != nil {
			return 0, fmt.Errorf("failed to parse month_start %q: %w", monthStartStr, err)
		}

		tonerAvg := averageTonerLevels(tonerSamplesStr)
		tonerJSON, _ := json.Marshal(tonerAvg)

		_, err = stmt.ExecContext(ctx,
			serial, monthStart.Format(time.RFC3339Nano), sampleCount,
			pageMin, pageMax, pageAvg,
			colorMin, colorMax, colorAvg,
			monoMin, monoMax, monoAvg,
			scanMin, scanMax, scanAvg,
			string(tonerJSON),
		)

		if err != nil {
			return 0, fmt.Errorf("failed to upsert monthly aggregate (serial=%s month=%s): %w", serial, monthStartStr, err)
		}

		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("monthly aggregation row iteration failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit monthly aggregation: %w", err)
	}

	if storageLogger != nil && count > 0 {
		storageLogger.Info("Downsampled daily metrics to monthly", "buckets_created", count, "older_than", olderThan)
	}

	return count, nil
}

// CleanupOldTieredMetrics removes metrics from all tiers based on retention policies
// Returns a map with counts of deleted records per tier
func (s *SQLiteStore) CleanupOldTieredMetrics(ctx context.Context, rawRetentionDays, hourlyRetentionDays, dailyRetentionDays int) (map[string]int, error) {
	now := time.Now()
	results := make(map[string]int)

	// Cleanup raw metrics (default 7 days)
	rawCutoff := now.AddDate(0, 0, -rawRetentionDays)
	rawCutoffStr := rawCutoff.UTC().Format(time.RFC3339Nano)
	rawResult, err := s.db.ExecContext(ctx, "DELETE FROM metrics_raw WHERE timestamp < ?", rawCutoffStr)
	if err != nil {
		return results, fmt.Errorf("failed to cleanup raw metrics: %w", err)
	}
	rawCount, _ := rawResult.RowsAffected()
	results["raw"] = int(rawCount)

	// Cleanup hourly metrics (default 30 days)
	hourlyCutoff := now.AddDate(0, 0, -hourlyRetentionDays)
	hourlyCutoffStr := hourlyCutoff.UTC().Format(time.RFC3339Nano)
	hourlyResult, err := s.db.ExecContext(ctx, "DELETE FROM metrics_hourly WHERE hour_start < ?", hourlyCutoffStr)
	if err != nil {
		return results, fmt.Errorf("failed to cleanup hourly metrics: %w", err)
	}
	hourlyCount, _ := hourlyResult.RowsAffected()
	results["hourly"] = int(hourlyCount)

	// Cleanup daily metrics (default 365 days)
	dailyCutoff := now.AddDate(0, 0, -dailyRetentionDays)
	dailyCutoffStr := dailyCutoff.UTC().Format(time.RFC3339Nano)
	dailyResult, err := s.db.ExecContext(ctx, "DELETE FROM metrics_daily WHERE day_start < ?", dailyCutoffStr)
	if err != nil {
		return results, fmt.Errorf("failed to cleanup daily metrics: %w", err)
	}
	dailyCount, _ := dailyResult.RowsAffected()
	results["daily"] = int(dailyCount)

	// Monthly metrics are kept forever (no cleanup)
	results["monthly"] = 0

	if storageLogger != nil {
		storageLogger.Info("Cleaned up old tiered metrics",
			"raw_deleted", rawCount,
			"hourly_deleted", hourlyCount,
			"daily_deleted", dailyCount,
			"raw_retention_days", rawRetentionDays,
			"hourly_retention_days", hourlyRetentionDays,
			"daily_retention_days", dailyRetentionDays,
		)
	}

	return results, nil
}

// PerformFullDownsampling runs all downsampling operations in sequence
// This should be called periodically (e.g., every 6-12 hours)
func (s *SQLiteStore) PerformFullDownsampling(ctx context.Context) error {
	now := time.Now()

	// Downsample raw → hourly (process data older than 1 hour)
	hourAgo := now.Add(-1 * time.Hour)
	hourlyCount, err := s.DownsampleRawToHourly(ctx, hourAgo)
	if err != nil {
		return fmt.Errorf("failed to downsample raw to hourly: %w", err)
	}

	// Downsample hourly → daily (process data older than 1 day)
	dayAgo := now.AddDate(0, 0, -1)
	dailyCount, err := s.DownsampleHourlyToDaily(ctx, dayAgo)
	if err != nil {
		return fmt.Errorf("failed to downsample hourly to daily: %w", err)
	}

	// Downsample daily → monthly (process data older than 1 month)
	monthAgo := now.AddDate(0, -1, 0)
	monthlyCount, err := s.DownsampleDailyToMonthly(ctx, monthAgo)
	if err != nil {
		return fmt.Errorf("failed to downsample daily to monthly: %w", err)
	}

	// Cleanup old data from each tier
	cleanupResults, err := s.CleanupOldTieredMetrics(ctx, 7, 30, 365)
	if err != nil {
		return fmt.Errorf("failed to cleanup old metrics: %w", err)
	}

	if storageLogger != nil {
		storageLogger.Info("Full downsampling complete",
			"hourly_buckets", hourlyCount,
			"daily_buckets", dailyCount,
			"monthly_buckets", monthlyCount,
			"raw_deleted", cleanupResults["raw"],
			"hourly_deleted", cleanupResults["hourly"],
			"daily_deleted", cleanupResults["daily"],
		)
	}

	return nil
}

// averageTonerLevels takes a comma-separated list of JSON toner level objects
// (from GROUP_CONCAT) and returns a map with averaged values.
//
// This is a streaming-friendly implementation that avoids building a giant
// slice of strings. It scans through the input once and accumulates values
// as it finds complete JSON objects.
func averageTonerLevels(samplesStr string) map[string]interface{} {
	if samplesStr == "" {
		return make(map[string]interface{})
	}

	totals := make(map[string]float64)
	counts := make(map[string]int)

	// Stream through the GROUP_CONCAT result, extracting one JSON object at a time.
	// JSON objects are delimited by {...} and may contain nested structures.
	start := -1
	braceCount := 0
	inString := false
	escape := false

	for i := 0; i < len(samplesStr); i++ {
		c := samplesStr[i]

		// Handle escape sequences inside strings
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}

		// Track string boundaries
		if c == '"' {
			inString = !inString
			continue
		}

		// Only count braces outside of strings
		if !inString {
			if c == '{' {
				if braceCount == 0 {
					start = i
				}
				braceCount++
			} else if c == '}' {
				braceCount--
				if braceCount == 0 && start >= 0 {
					// Found complete JSON object
					jsonStr := samplesStr[start : i+1]
					accumulateTonerSample(jsonStr, totals, counts)
					start = -1
				}
			}
		}
	}

	// Calculate averages
	result := make(map[string]interface{})
	for color, total := range totals {
		if count := counts[color]; count > 0 {
			result[color] = int(total / float64(count))
		}
	}

	return result
}

// accumulateTonerSample parses a single JSON toner object and accumulates values.
func accumulateTonerSample(jsonStr string, totals map[string]float64, counts map[string]int) {
	var levels map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &levels); err != nil {
		return
	}

	for color, value := range levels {
		switch v := value.(type) {
		case float64:
			totals[color] += v
			counts[color]++
		case int:
			totals[color] += float64(v)
			counts[color]++
		}
	}
}

// GetTieredMetricsBounds returns the overall min/max timestamps and total point count
// across all metric tiers for a device.
func (s *SQLiteStore) GetTieredMetricsBounds(ctx context.Context, serial string) (time.Time, time.Time, int, error) {
	if serial == "" {
		return time.Time{}, time.Time{}, 0, ErrInvalidSerial
	}

	query := `
		SELECT
			MIN(min_ts) AS min_ts,
			MAX(max_ts) AS max_ts,
			SUM(cnt)    AS total_cnt
		FROM (
			SELECT MIN(timestamp) AS min_ts, MAX(timestamp) AS max_ts, COUNT(*) AS cnt FROM metrics_raw WHERE serial = ?
			UNION ALL
			SELECT MIN(hour_start) AS min_ts, MAX(hour_start) AS max_ts, COUNT(*) AS cnt FROM metrics_hourly WHERE serial = ?
			UNION ALL
			SELECT MIN(day_start) AS min_ts, MAX(day_start) AS max_ts, COUNT(*) AS cnt FROM metrics_daily WHERE serial = ?
			UNION ALL
			SELECT MIN(month_start) AS min_ts, MAX(month_start) AS max_ts, COUNT(*) AS cnt FROM metrics_monthly WHERE serial = ?
		)
	`

	var minStr sql.NullString
	var maxStr sql.NullString
	var total sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query, serial, serial, serial, serial).Scan(&minStr, &maxStr, &total); err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("failed to query metrics bounds: %w", err)
	}

	if !total.Valid || total.Int64 == 0 || !minStr.Valid || !maxStr.Valid || minStr.String == "" || maxStr.String == "" {
		return time.Time{}, time.Time{}, 0, ErrNotFound
	}

	minTS, err := parseMetricsTimestamp(minStr.String)
	if err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("failed to parse metrics min timestamp %q: %w", minStr.String, err)
	}
	maxTS, err := parseMetricsTimestamp(maxStr.String)
	if err != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("failed to parse metrics max timestamp %q: %w", maxStr.String, err)
	}

	return minTS, maxTS, int(total.Int64), nil
}

func parseMetricsTimestamp(value string) (time.Time, error) {
	// SQLite aggregate expressions return strings, including CURRENT_TIMESTAMP
	// and driver-written timestamps rather than only our RFC3339 snapshots.
	var parseErr error
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		} else {
			parseErr = err
		}
	}
	return time.Time{}, parseErr
}

// GetTieredMetricsHistory retrieves metrics from appropriate tiers based on time range
// This is a smarter version of GetMetricsHistory that queries the right tier
func (s *SQLiteStore) GetTieredMetricsHistory(ctx context.Context, serial string, since time.Time, until time.Time) ([]*MetricsSnapshot, error) {
	if serial == "" {
		return nil, ErrInvalidSerial
	}

	now := time.Now()
	sevenDaysAgo := now.AddDate(0, 0, -7)
	thirtyDaysAgo := now.AddDate(0, 0, -30)
	oneYearAgo := now.AddDate(0, 0, -365)

	var snapshots []*MetricsSnapshot

	// Determine which tiers to query based on time range
	needRaw := since.After(sevenDaysAgo) || until.After(sevenDaysAgo)
	needHourly := (since.Before(sevenDaysAgo) && since.After(thirtyDaysAgo)) || (until.Before(sevenDaysAgo) && until.After(thirtyDaysAgo))
	needDaily := (since.Before(thirtyDaysAgo) && since.After(oneYearAgo)) || (until.Before(thirtyDaysAgo) && until.After(oneYearAgo))
	needMonthly := since.Before(oneYearAgo) || until.Before(oneYearAgo)

	// Query raw metrics (last 7 days)
	if needRaw {
		rawQuery := `
			SELECT id, serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels
			FROM metrics_raw
			WHERE serial = ? AND timestamp >= ? AND timestamp <= ?
			ORDER BY timestamp ASC
		`
		sinceStr := since.UTC().Format(time.RFC3339Nano)
		untilStr := until.UTC().Format(time.RFC3339Nano)
		rows, err := s.db.QueryContext(ctx, rawQuery, serial, sinceStr, untilStr)
		if err != nil {
			return nil, fmt.Errorf("failed to query raw metrics: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			snapshot := &MetricsSnapshot{}
			var tonerJSON sql.NullString

			err := rows.Scan(
				&snapshot.ID, &snapshot.Serial, &snapshot.Timestamp,
				&snapshot.PageCount, &snapshot.ColorPages, &snapshot.MonoPages,
				&snapshot.ScanCount, &tonerJSON,
			)
			if err != nil {
				continue
			}

			if tonerJSON.Valid && tonerJSON.String != "" {
				json.Unmarshal([]byte(tonerJSON.String), &snapshot.TonerLevels)
			}

			snapshot.Tier = "raw"
			snapshots = append(snapshots, snapshot)
		}
	}

	// Query hourly aggregates (8-30 days ago)
	if needHourly {
		hourlyQuery := `
			SELECT id, serial, hour_start, page_count_avg, color_pages_avg, mono_pages_avg, scan_count_avg, toner_levels_avg
			FROM metrics_hourly
			WHERE serial = ? AND hour_start >= ? AND hour_start <= ?
			ORDER BY hour_start ASC
		`
		sinceStr := since.UTC().Format(time.RFC3339Nano)
		untilStr := until.UTC().Format(time.RFC3339Nano)
		rows, err := s.db.QueryContext(ctx, hourlyQuery, serial, sinceStr, untilStr)
		if err != nil {
			return nil, fmt.Errorf("failed to query hourly metrics: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			snapshot := &MetricsSnapshot{}
			var tonerJSON sql.NullString

			err := rows.Scan(
				&snapshot.ID, &snapshot.Serial, &snapshot.Timestamp,
				&snapshot.PageCount, &snapshot.ColorPages, &snapshot.MonoPages,
				&snapshot.ScanCount, &tonerJSON,
			)
			if err != nil {
				continue
			}

			if tonerJSON.Valid && tonerJSON.String != "" {
				json.Unmarshal([]byte(tonerJSON.String), &snapshot.TonerLevels)
			}

			snapshot.Tier = "hourly"
			snapshots = append(snapshots, snapshot)
		}
	}

	// Query daily aggregates (31-365 days ago)
	if needDaily {
		dailyQuery := `
			SELECT id, serial, day_start, page_count_avg, color_pages_avg, mono_pages_avg, scan_count_avg, toner_levels_avg
			FROM metrics_daily
			WHERE serial = ? AND day_start >= ? AND day_start <= ?
			ORDER BY day_start ASC
		`
		sinceStr := since.UTC().Format(time.RFC3339Nano)
		untilStr := until.UTC().Format(time.RFC3339Nano)
		rows, err := s.db.QueryContext(ctx, dailyQuery, serial, sinceStr, untilStr)
		if err != nil {
			return nil, fmt.Errorf("failed to query daily metrics: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			snapshot := &MetricsSnapshot{}
			var tonerJSON sql.NullString

			err := rows.Scan(
				&snapshot.ID, &snapshot.Serial, &snapshot.Timestamp,
				&snapshot.PageCount, &snapshot.ColorPages, &snapshot.MonoPages,
				&snapshot.ScanCount, &tonerJSON,
			)
			if err != nil {
				continue
			}

			if tonerJSON.Valid && tonerJSON.String != "" {
				json.Unmarshal([]byte(tonerJSON.String), &snapshot.TonerLevels)
			}

			snapshot.Tier = "daily"
			snapshots = append(snapshots, snapshot)
		}
	}

	// Query monthly aggregates (>365 days ago)
	if needMonthly {
		monthlyQuery := `
			SELECT id, serial, month_start, page_count_avg, color_pages_avg, mono_pages_avg, scan_count_avg, toner_levels_avg
			FROM metrics_monthly
			WHERE serial = ? AND month_start >= ? AND month_start <= ?
			ORDER BY month_start ASC
		`
		sinceStr := since.UTC().Format(time.RFC3339Nano)
		untilStr := until.UTC().Format(time.RFC3339Nano)
		rows, err := s.db.QueryContext(ctx, monthlyQuery, serial, sinceStr, untilStr)
		if err != nil {
			return nil, fmt.Errorf("failed to query monthly metrics: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			snapshot := &MetricsSnapshot{}
			var tonerJSON sql.NullString

			err := rows.Scan(
				&snapshot.ID, &snapshot.Serial, &snapshot.Timestamp,
				&snapshot.PageCount, &snapshot.ColorPages, &snapshot.MonoPages,
				&snapshot.ScanCount, &tonerJSON,
			)
			if err != nil {
				continue
			}

			if tonerJSON.Valid && tonerJSON.String != "" {
				json.Unmarshal([]byte(tonerJSON.String), &snapshot.TonerLevels)
			}

			snapshot.Tier = "monthly"
			snapshots = append(snapshots, snapshot)
		}
	}

	return snapshots, nil
}
