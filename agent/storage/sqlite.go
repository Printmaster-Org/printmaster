package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	commonstorage "printmaster/common/storage"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// Logger interface for storage operations
type Logger interface {
	Error(msg string, context ...interface{})
	Warn(msg string, context ...interface{})
	Info(msg string, context ...interface{})
	Debug(msg string, context ...interface{})
	WarnRateLimited(key string, interval time.Duration, msg string, context ...interface{})
}

// Global logger for storage package
var storageLogger Logger

// SetLogger sets the logger for the storage package
func SetLogger(logger Logger) {
	storageLogger = logger
}

// SQLiteStore implements DeviceStore using SQLite
type SQLiteStore struct {
	db     *sql.DB
	dbPath string // Store path for backup operations
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// NewSQLiteStore creates a new SQLite-based device store
// If dbPath is empty, uses in-memory database (:memory:)
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	return NewSQLiteStoreWithConfig(dbPath, nil)
}

// openSQLiteDB opens a pooled *sql.DB with pragmas applied via the DSN.
//
// Pragmas must be passed in the DSN (not via db.Exec after opening) because
// *sql.DB pools multiple physical connections: a PRAGMA applied with db.Exec
// only affects whichever single connection happened to be borrowed for that
// call. Any additional connections the pool opens later (MaxOpenConns > 1)
// would fall back to SQLite's defaults - journal_mode=DELETE and
// busy_timeout=0 - which fail instantly on lock contention instead of
// waiting, causing intermittent "database is locked" errors under
// concurrent access. Encoding the pragmas in the DSN applies them to every
// connection the pool opens.
func openSQLiteDB(dbPath string) (*sql.DB, error) {
	const busyTimeoutMS = 30000
	connStr := dbPath
	if dbPath != ":memory:" {
		connStr += fmt.Sprintf("?_busy_timeout=%d&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000&_foreign_keys=ON&_txlock=immediate", busyTimeoutMS)
	} else {
		connStr += fmt.Sprintf("?_busy_timeout=%d&_foreign_keys=ON", busyTimeoutMS)
	}

	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Connection pool settings for SQLite:
	// - MaxOpenConns: Allow multiple connections for reads (WAL mode supports this)
	// - MaxIdleConns: Keep some connections ready to reduce open/close overhead
	// - ConnMaxLifetime: Prevent stale connections
	// Note: SQLite handles write serialization internally with busy_timeout
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

// NewSQLiteStoreWithConfig creates a new SQLite-based device store with optional config store
// for tracking rotation events. If configStore is provided, rotation events will set a flag
// that the UI can use to warn users.
func NewSQLiteStoreWithConfig(dbPath string, configStore AgentConfigStore) (*SQLiteStore, error) {
	if dbPath == "" {
		dbPath = ":memory:"
	}

	db, err := openSQLiteDB(dbPath)
	if err != nil {
		return nil, err
	}

	store := &SQLiteStore{
		db:     db,
		dbPath: dbPath,
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		db.Close()

		// If schema initialization fails and we have a real database file (not in-memory),
		// rotate the corrupted database and try again with a fresh one
		if dbPath != ":memory:" {
			if storageLogger != nil {
				storageLogger.Error("Database schema initialization failed, attempting to rotate database",
					"error", err, "path", dbPath)
			}

			backupPath, rotateErr := RotateDatabase(dbPath, configStore)
			if rotateErr != nil {
				return nil, fmt.Errorf("failed to initialize schema and unable to rotate database: %w (rotation error: %v)", err, rotateErr)
			}

			if storageLogger != nil {
				storageLogger.Warn("Database rotated due to migration failure - starting with fresh database",
					"backupPath", backupPath,
					"newPath", dbPath,
					"originalError", err.Error())
			}

			// Try to open the new database (pass config store through)
			return NewSQLiteStoreWithConfig(dbPath, configStore)
		}

		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Run auto-migration
	if err := store.autoMigrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}

	// Seed default metrics for devices without any
	if err := store.seedDefaultMetrics(); err != nil {
		if storageLogger != nil {
			storageLogger.Warn("Failed to seed default metrics", "error", err)
		}
		// Non-fatal, continue
	}

	return store, nil
}

// initSchema creates the database schema
func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS devices (
		serial TEXT PRIMARY KEY,
		ip TEXT NOT NULL,
		manufacturer TEXT,
		model TEXT,
		hostname TEXT,
		firmware TEXT,
		mac_address TEXT,
		subnet_mask TEXT,
		gateway TEXT,
		dns_servers TEXT,
		dhcp_server TEXT,
		page_count INTEGER DEFAULT 0,
		toner_levels TEXT,
		consumables TEXT,
		status_messages TEXT,
		last_seen DATETIME NOT NULL,
		created_at DATETIME NOT NULL,
		first_seen DATETIME NOT NULL,
		is_saved BOOLEAN DEFAULT 0,
		visible BOOLEAN DEFAULT 1,
		discovery_method TEXT,
		walk_filename TEXT,
		last_scan_id INTEGER,
		raw_data TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_devices_is_saved ON devices(is_saved);
	CREATE INDEX IF NOT EXISTS idx_devices_visible ON devices(visible);
	CREATE INDEX IF NOT EXISTS idx_devices_ip ON devices(ip);
	CREATE INDEX IF NOT EXISTS idx_devices_last_seen ON devices(last_seen);
	CREATE INDEX IF NOT EXISTS idx_devices_manufacturer ON devices(manufacturer);

	CREATE TABLE IF NOT EXISTS scan_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		serial TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		ip TEXT NOT NULL,
		hostname TEXT,
		firmware TEXT,
		consumables TEXT,
		status_messages TEXT,
		discovery_method TEXT,
		walk_filename TEXT,
		raw_data TEXT,
		FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_scan_history_serial ON scan_history(serial);
	CREATE INDEX IF NOT EXISTS idx_scan_history_created ON scan_history(created_at);

	CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL
	);

	-- Raw metrics: high-resolution 5-minute data, retained for 7 days
	CREATE TABLE IF NOT EXISTS metrics_raw (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		serial TEXT NOT NULL,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		page_count INTEGER DEFAULT 0,
		color_pages INTEGER DEFAULT 0,
		mono_pages INTEGER DEFAULT 0,
		scan_count INTEGER DEFAULT 0,
		toner_levels TEXT,
		FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE
	);

	-- Hourly aggregates: 1-hour buckets, retained for 30 days
	CREATE TABLE IF NOT EXISTS metrics_hourly (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		serial TEXT NOT NULL,
		hour_start DATETIME NOT NULL,  -- Start of the hour bucket
		sample_count INTEGER DEFAULT 0,  -- Number of raw samples aggregated
		page_count_min INTEGER DEFAULT 0,
		page_count_max INTEGER DEFAULT 0,
		page_count_avg INTEGER DEFAULT 0,
		color_pages_min INTEGER DEFAULT 0,
		color_pages_max INTEGER DEFAULT 0,
		color_pages_avg INTEGER DEFAULT 0,
		mono_pages_min INTEGER DEFAULT 0,
		mono_pages_max INTEGER DEFAULT 0,
		mono_pages_avg INTEGER DEFAULT 0,
		scan_count_min INTEGER DEFAULT 0,
		scan_count_max INTEGER DEFAULT 0,
		scan_count_avg INTEGER DEFAULT 0,
		toner_levels_avg TEXT,  -- JSON with averaged toner levels
		FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE,
		UNIQUE(serial, hour_start)  -- One aggregate per device per hour
	);

	-- Daily aggregates: 1-day buckets, retained for 365 days
	CREATE TABLE IF NOT EXISTS metrics_daily (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		serial TEXT NOT NULL,
		day_start DATETIME NOT NULL,  -- Start of the day (midnight UTC)
		sample_count INTEGER DEFAULT 0,  -- Number of hourly samples aggregated
		page_count_min INTEGER DEFAULT 0,
		page_count_max INTEGER DEFAULT 0,
		page_count_avg INTEGER DEFAULT 0,
		color_pages_min INTEGER DEFAULT 0,
		color_pages_max INTEGER DEFAULT 0,
		color_pages_avg INTEGER DEFAULT 0,
		mono_pages_min INTEGER DEFAULT 0,
		mono_pages_max INTEGER DEFAULT 0,
		mono_pages_avg INTEGER DEFAULT 0,
		scan_count_min INTEGER DEFAULT 0,
		scan_count_max INTEGER DEFAULT 0,
		scan_count_avg INTEGER DEFAULT 0,
		toner_levels_avg TEXT,
		FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE,
		UNIQUE(serial, day_start)
	);

	-- Monthly aggregates: 1-month buckets, retained forever
	CREATE TABLE IF NOT EXISTS metrics_monthly (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		serial TEXT NOT NULL,
		month_start DATETIME NOT NULL,  -- Start of the month (first day, midnight UTC)
		sample_count INTEGER DEFAULT 0,  -- Number of daily samples aggregated
		page_count_min INTEGER DEFAULT 0,
		page_count_max INTEGER DEFAULT 0,
		page_count_avg INTEGER DEFAULT 0,
		color_pages_min INTEGER DEFAULT 0,
		color_pages_max INTEGER DEFAULT 0,
		color_pages_avg INTEGER DEFAULT 0,
		mono_pages_min INTEGER DEFAULT 0,
		mono_pages_max INTEGER DEFAULT 0,
		mono_pages_avg INTEGER DEFAULT 0,
		scan_count_min INTEGER DEFAULT 0,
		scan_count_max INTEGER DEFAULT 0,
		scan_count_avg INTEGER DEFAULT 0,
		toner_levels_avg TEXT,
		FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE,
		UNIQUE(serial, month_start)
	);

	CREATE INDEX IF NOT EXISTS idx_metrics_raw_serial ON metrics_raw(serial);
	CREATE INDEX IF NOT EXISTS idx_metrics_raw_timestamp ON metrics_raw(timestamp);
	CREATE INDEX IF NOT EXISTS idx_metrics_raw_serial_timestamp ON metrics_raw(serial, timestamp);
	
	CREATE INDEX IF NOT EXISTS idx_metrics_hourly_serial ON metrics_hourly(serial);
	CREATE INDEX IF NOT EXISTS idx_metrics_hourly_hour_start ON metrics_hourly(hour_start);
	CREATE INDEX IF NOT EXISTS idx_metrics_hourly_serial_hour ON metrics_hourly(serial, hour_start);
	
	CREATE INDEX IF NOT EXISTS idx_metrics_daily_serial ON metrics_daily(serial);
	CREATE INDEX IF NOT EXISTS idx_metrics_daily_day_start ON metrics_daily(day_start);
	CREATE INDEX IF NOT EXISTS idx_metrics_daily_serial_day ON metrics_daily(serial, day_start);
	
	CREATE INDEX IF NOT EXISTS idx_metrics_monthly_serial ON metrics_monthly(serial);
	CREATE INDEX IF NOT EXISTS idx_metrics_monthly_month_start ON metrics_monthly(month_start);
	CREATE INDEX IF NOT EXISTS idx_metrics_monthly_serial_month ON metrics_monthly(serial, month_start);

	-- Local printers discovered via Windows print spooler
	CREATE TABLE IF NOT EXISTS local_printers (
		name TEXT PRIMARY KEY,
		port_name TEXT NOT NULL,
		driver_name TEXT,
		printer_type TEXT NOT NULL,
		is_default BOOLEAN DEFAULT 0,
		is_shared BOOLEAN DEFAULT 0,
		manufacturer TEXT,
		model TEXT,
		serial_number TEXT,
		status TEXT DEFAULT 'unknown',
		first_seen DATETIME NOT NULL,
		last_seen DATETIME NOT NULL,
		total_pages INTEGER DEFAULT 0,
		total_color_pages INTEGER DEFAULT 0,
		total_mono_pages INTEGER DEFAULT 0,
		baseline_pages INTEGER DEFAULT 0,
		last_page_update DATETIME,
		tracking_enabled BOOLEAN DEFAULT 0,
		asset_number TEXT,
		location TEXT,
		description TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_local_printers_type ON local_printers(printer_type);
	CREATE INDEX IF NOT EXISTS idx_local_printers_tracking ON local_printers(tracking_enabled);

	-- Print job history for local printers
	CREATE TABLE IF NOT EXISTS local_print_jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		printer_name TEXT NOT NULL,
		job_id INTEGER NOT NULL,
		document_name TEXT,
		user_name TEXT,
		machine_name TEXT,
		total_pages INTEGER DEFAULT 0,
		pages_printed INTEGER DEFAULT 0,
		is_color BOOLEAN DEFAULT 0,
		size_bytes INTEGER DEFAULT 0,
		submitted_at DATETIME NOT NULL,
		completed_at DATETIME,
		status TEXT DEFAULT 'completed',
		FOREIGN KEY (printer_name) REFERENCES local_printers(name) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_local_print_jobs_printer ON local_print_jobs(printer_name);
	CREATE INDEX IF NOT EXISTS idx_local_print_jobs_submitted ON local_print_jobs(submitted_at);

	-- Page count audit trail for billing/accountability
	CREATE TABLE IF NOT EXISTS page_count_audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		serial TEXT NOT NULL,
		old_count INTEGER DEFAULT 0,
		new_count INTEGER DEFAULT 0,
		change_type TEXT NOT NULL,
		changed_by TEXT,
		reason TEXT,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		source_metric TEXT DEFAULT 'page_count',
		FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_page_count_audit_serial ON page_count_audit(serial);
	CREATE INDEX IF NOT EXISTS idx_page_count_audit_timestamp ON page_count_audit(timestamp);
	CREATE INDEX IF NOT EXISTS idx_page_count_audit_serial_timestamp ON page_count_audit(serial, timestamp);
	`

	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	// Run migrations if needed
	if err := s.runMigrations(); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

// schemaMigration is a single, ordered schema change. The list below is the
// single source of truth for the schema version: adding a migration to this
// slice automatically advances latestSchemaVersion() everywhere it's used
// (runMigrations here, and the BackupAndReset staleness check in
// migrations.go), so there's no separate constant to remember to bump.
type schemaMigration struct {
	version int
	apply   func(s *SQLiteStore) error
}

var schemaMigrations = []schemaMigration{
	{2, migrateSchemaV2},
	{3, migrateSchemaV3},
	{4, migrateSchemaV4},
	{5, migrateSchemaV5},
	{6, migrateSchemaV6},
	{7, migrateSchemaV7},
	{8, migrateSchemaV8},
	{9, migrateSchemaV9},
	{10, migrateSchemaV10},
}

// latestSchemaVersion returns the highest version defined in schemaMigrations.
func latestSchemaVersion() int {
	if len(schemaMigrations) == 0 {
		return 0
	}
	return schemaMigrations[len(schemaMigrations)-1].version
}

// runMigrations handles schema migrations for existing databases
func (s *SQLiteStore) runMigrations() error {
	// Check current version
	var currentVersion int
	err := s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&currentVersion)
	if err != nil {
		// Table might not exist yet, treat as version 0
		currentVersion = 0
	}

	for _, m := range schemaMigrations {
		if currentVersion >= m.version {
			continue
		}
		if err := m.apply(s); err != nil {
			return err
		}
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO schema_version (version, applied_at) VALUES (?, ?)`, m.version, time.Now()); err != nil {
			return fmt.Errorf("failed to record schema version: %w", err)
		}
	}

	// Schema repair: ensure critical columns exist regardless of recorded version
	// This handles cases where migration tracking got out of sync with actual schema
	if err := s.repairSchema(); err != nil {
		return fmt.Errorf("schema repair failed: %w", err)
	}

	return nil
}

// migrateSchemaV2: Add visible and first_seen columns
func migrateSchemaV2(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		// Add visible column if it doesn't exist
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN visible BOOLEAN DEFAULT 1`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add visible column: %w", err)
		}

		// Add first_seen column if it doesn't exist
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN first_seen DATETIME`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add first_seen column: %w", err)
		}

		// Populate first_seen with created_at for existing records
		_, err = s.db.Exec(`UPDATE devices SET first_seen = created_at WHERE first_seen IS NULL`)
		if err != nil {
			return fmt.Errorf("failed to populate first_seen: %w", err)
		}
	}
	return nil
}

// migrateSchemaV3: Add asset_number, location, and web_ui_url columns
func migrateSchemaV3(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		// Add asset_number column if it doesn't exist
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN asset_number TEXT`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add asset_number column: %w", err)
		}

		// Add location column if it doesn't exist
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN location TEXT`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add location column: %w", err)
		}

		// Add web_ui_url column if it doesn't exist
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN web_ui_url TEXT`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add web_ui_url column: %w", err)
		}
	}
	return nil
}

// migrateSchemaV4: Add locked_fields column for field locking
func migrateSchemaV4(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		// Add locked_fields column (JSON) if it doesn't exist
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN locked_fields TEXT`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add locked_fields column: %w", err)
		}
	}
	return nil
}

// migrateSchemaV5: Add detailed impression counter fields to metrics_history
func migrateSchemaV5(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='metrics_history'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		// Add new counter columns
		columns := []string{
			"fax_pages INTEGER DEFAULT 0",
			"copy_pages INTEGER DEFAULT 0",
			"other_pages INTEGER DEFAULT 0",
			"copy_mono_pages INTEGER DEFAULT 0",
			"copy_flatbed_scans INTEGER DEFAULT 0",
			"copy_adf_scans INTEGER DEFAULT 0",
			"fax_flatbed_scans INTEGER DEFAULT 0",
			"fax_adf_scans INTEGER DEFAULT 0",
			"scan_to_host_flatbed INTEGER DEFAULT 0",
			"scan_to_host_adf INTEGER DEFAULT 0",
			"duplex_sheets INTEGER DEFAULT 0",
			"jam_events INTEGER DEFAULT 0",
			"scanner_jam_events INTEGER DEFAULT 0",
		}

		for _, col := range columns {
			_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE metrics_history ADD COLUMN %s`, col))
			if err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("failed to add column %s: %w", col, err)
			}
		}
	}
	return nil
}

// migrateSchemaV6: Add description column to devices table
func migrateSchemaV6(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		// Add description column if it doesn't exist
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN description TEXT`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add description column: %w", err)
		}
	}
	return nil
}

// migrateSchemaV7: Remove page_count and toner_levels from devices table
// These fields now live exclusively in metrics_history table
func migrateSchemaV7(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		// SQLite doesn't support DROP COLUMN directly, so we need to recreate the table
		// First, check if columns exist
		var hasPageCount, hasTonerLevels bool
		rows, err := s.db.Query("PRAGMA table_info(devices)")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var cid int
				var name string
				var ctype string
				var notnull int
				var dfltValue interface{}
				var pk int
				if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err == nil {
					if name == "page_count" {
						hasPageCount = true
					}
					if name == "toner_levels" {
						hasTonerLevels = true
					}
				}
			}
		}

		// Only migrate if columns exist
		if hasPageCount || hasTonerLevels {
			// Create new table without page_count and toner_levels
			_, err = s.db.Exec(`
				CREATE TABLE devices_new (
					serial TEXT PRIMARY KEY,
					ip TEXT NOT NULL,
					manufacturer TEXT,
					model TEXT,
					hostname TEXT,
					firmware TEXT,
					mac_address TEXT,
					subnet_mask TEXT,
					gateway TEXT,
					dns_servers TEXT,
					dhcp_server TEXT,
					consumables TEXT,
					status_messages TEXT,
					last_seen DATETIME NOT NULL,
					created_at DATETIME NOT NULL,
					first_seen DATETIME NOT NULL,
					is_saved BOOLEAN DEFAULT 0,
					visible BOOLEAN DEFAULT 1,
					discovery_method TEXT,
					walk_filename TEXT,
					last_scan_id INTEGER,
					asset_number TEXT,
					location TEXT,
					description TEXT,
					web_ui_url TEXT,
					locked_fields TEXT,
					raw_data TEXT
				)
			`)
			if err != nil {
				return fmt.Errorf("failed to create devices_new table: %w", err)
			}

			// Copy data (excluding page_count and toner_levels)
			_, err = s.db.Exec(`
				INSERT INTO devices_new 
				SELECT serial, ip, manufacturer, model, hostname, firmware, mac_address, subnet_mask, gateway, dns_servers, dhcp_server,
				       consumables, status_messages, last_seen, created_at, first_seen, is_saved, visible, discovery_method, walk_filename,
				       last_scan_id, asset_number, location, description, web_ui_url, locked_fields, raw_data
				FROM devices
			`)
			if err != nil {
				return fmt.Errorf("failed to copy data to devices_new: %w", err)
			}

			// Drop old table and rename new one
			_, err = s.db.Exec(`DROP TABLE devices`)
			if err != nil {
				return fmt.Errorf("failed to drop old devices table: %w", err)
			}

			_, err = s.db.Exec(`ALTER TABLE devices_new RENAME TO devices`)
			if err != nil {
				return fmt.Errorf("failed to rename devices_new: %w", err)
			}

			// Recreate indexes
			_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_is_saved ON devices(is_saved)`)
			_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_visible ON devices(visible)`)
			_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_ip ON devices(ip)`)
			_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_last_seen ON devices(last_seen)`)
			_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_manufacturer ON devices(manufacturer)`)
		}
	}
	return nil
}

// migrateSchemaV8: Rename metrics_history to metrics_raw, add tiered aggregation tables
// This implements Netdata-style tiered storage: raw (7d), hourly (30d), daily (365d), monthly (forever)
func migrateSchemaV8(s *SQLiteStore) error {
	var historyExists int
	var rawExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='metrics_history'").Scan(&historyExists)
	if err != nil {
		return fmt.Errorf("failed to check for metrics_history table: %w", err)
	}

	err = s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='metrics_raw'").Scan(&rawExists)
	if err != nil {
		return fmt.Errorf("failed to check for metrics_raw table: %w", err)
	}

	// Only rename if metrics_history exists AND metrics_raw doesn't
	if historyExists > 0 && rawExists == 0 {
		// Rename existing metrics_history to metrics_raw
		_, err = s.db.Exec(`ALTER TABLE metrics_history RENAME TO metrics_raw`)
		if err != nil {
			return fmt.Errorf("failed to rename metrics_history to metrics_raw: %w", err)
		}

		// Drop old indexes
		_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_metrics_serial`)
		_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_metrics_timestamp`)
		_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_metrics_serial_timestamp`)

		// Create new indexes for metrics_raw
		_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_raw_serial ON metrics_raw(serial)`)
		_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_raw_timestamp ON metrics_raw(timestamp)`)
		_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_raw_serial_timestamp ON metrics_raw(serial, timestamp)`)
	} else if historyExists > 0 && rawExists > 0 {
		// Both tables exist - this is a conflict, drop the old one
		_, _ = s.db.Exec(`DROP TABLE IF EXISTS metrics_history`)
	}

	// Create hourly aggregation table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS metrics_hourly (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			serial TEXT NOT NULL,
			hour_start DATETIME NOT NULL,
			sample_count INTEGER DEFAULT 0,
			page_count_min INTEGER DEFAULT 0,
			page_count_max INTEGER DEFAULT 0,
			page_count_avg INTEGER DEFAULT 0,
			color_pages_min INTEGER DEFAULT 0,
			color_pages_max INTEGER DEFAULT 0,
			color_pages_avg INTEGER DEFAULT 0,
			mono_pages_min INTEGER DEFAULT 0,
			mono_pages_max INTEGER DEFAULT 0,
			mono_pages_avg INTEGER DEFAULT 0,
			scan_count_min INTEGER DEFAULT 0,
			scan_count_max INTEGER DEFAULT 0,
			scan_count_avg INTEGER DEFAULT 0,
			toner_levels_avg TEXT,
			FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE,
			UNIQUE(serial, hour_start)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create metrics_hourly table: %w", err)
	}

	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_hourly_serial ON metrics_hourly(serial)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_hourly_hour_start ON metrics_hourly(hour_start)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_hourly_serial_hour ON metrics_hourly(serial, hour_start)`)

	// Create daily aggregation table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS metrics_daily (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			serial TEXT NOT NULL,
			day_start DATETIME NOT NULL,
			sample_count INTEGER DEFAULT 0,
			page_count_min INTEGER DEFAULT 0,
			page_count_max INTEGER DEFAULT 0,
			page_count_avg INTEGER DEFAULT 0,
			color_pages_min INTEGER DEFAULT 0,
			color_pages_max INTEGER DEFAULT 0,
			color_pages_avg INTEGER DEFAULT 0,
			mono_pages_min INTEGER DEFAULT 0,
			mono_pages_max INTEGER DEFAULT 0,
			mono_pages_avg INTEGER DEFAULT 0,
			scan_count_min INTEGER DEFAULT 0,
			scan_count_max INTEGER DEFAULT 0,
			scan_count_avg INTEGER DEFAULT 0,
			toner_levels_avg TEXT,
			FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE,
			UNIQUE(serial, day_start)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create metrics_daily table: %w", err)
	}

	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_daily_serial ON metrics_daily(serial)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_daily_day_start ON metrics_daily(day_start)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_daily_serial_day ON metrics_daily(serial, day_start)`)

	// Create monthly aggregation table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS metrics_monthly (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			serial TEXT NOT NULL,
			month_start DATETIME NOT NULL,
			sample_count INTEGER DEFAULT 0,
			page_count_min INTEGER DEFAULT 0,
			page_count_max INTEGER DEFAULT 0,
			page_count_avg INTEGER DEFAULT 0,
			color_pages_min INTEGER DEFAULT 0,
			color_pages_max INTEGER DEFAULT 0,
			color_pages_avg INTEGER DEFAULT 0,
			mono_pages_min INTEGER DEFAULT 0,
			mono_pages_max INTEGER DEFAULT 0,
			mono_pages_avg INTEGER DEFAULT 0,
			scan_count_min INTEGER DEFAULT 0,
			scan_count_max INTEGER DEFAULT 0,
			scan_count_avg INTEGER DEFAULT 0,
			toner_levels_avg TEXT,
			FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE,
			UNIQUE(serial, month_start)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create metrics_monthly table: %w", err)
	}

	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_monthly_serial ON metrics_monthly(serial)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_monthly_month_start ON metrics_monthly(month_start)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metrics_monthly_serial_month ON metrics_monthly(serial, month_start)`)

	if storageLogger != nil {
		storageLogger.Info("Applied schema migration 7->8: Tiered metrics storage (raw/hourly/daily/monthly)")
	}
	return nil
}

// migrateSchemaV9: Add device classification columns and page_count_audit table
// This supports unified device view (network + USB/local printers)
func migrateSchemaV9(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		// Add new device classification columns
		columns := []struct {
			name string
			def  string
		}{
			{"device_type", "TEXT DEFAULT 'network'"},
			{"source_type", "TEXT DEFAULT 'snmp'"},
			{"is_usb", "BOOLEAN DEFAULT 0"},
			{"initial_page_count", "INTEGER DEFAULT 0"},
			{"port_name", "TEXT"},
			{"driver_name", "TEXT"},
			{"is_default", "BOOLEAN DEFAULT 0"},
			{"is_shared", "BOOLEAN DEFAULT 0"},
			{"spooler_status", "TEXT"},
		}

		for _, col := range columns {
			_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE devices ADD COLUMN %s %s`, col.name, col.def))
			if err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("failed to add %s column: %w", col.name, err)
			}
		}

		// Set device_type and source_type for existing devices
		_, _ = s.db.Exec(`UPDATE devices SET device_type = 'network', source_type = 'snmp' WHERE device_type IS NULL OR device_type = ''`)
	}

	// Create page_count_audit table if it doesn't exist
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS page_count_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			serial TEXT NOT NULL,
			old_count INTEGER DEFAULT 0,
			new_count INTEGER DEFAULT 0,
			change_type TEXT NOT NULL,
			changed_by TEXT,
			reason TEXT,
			timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			source_metric TEXT DEFAULT 'page_count',
			FOREIGN KEY (serial) REFERENCES devices(serial) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create page_count_audit table: %w", err)
	}

	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_page_count_audit_serial ON page_count_audit(serial)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_page_count_audit_timestamp ON page_count_audit(timestamp)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_page_count_audit_serial_timestamp ON page_count_audit(serial, timestamp)`)

	// Add index for device_type filtering
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_device_type ON devices(device_type)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_source_type ON devices(source_type)`)

	if storageLogger != nil {
		storageLogger.Info("Applied schema migration 8->9: Device classification and page count audit trail")
	}
	return nil
}

// migrateSchemaV10: Add usb_webui_available column for USB printer web UI detection
func migrateSchemaV10(s *SQLiteStore) error {
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err == nil && tableExists > 0 {
		_, err = s.db.Exec(`ALTER TABLE devices ADD COLUMN usb_webui_available BOOLEAN DEFAULT 0`)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("failed to add usb_webui_available column: %w", err)
		}
	}

	if storageLogger != nil {
		storageLogger.Info("Applied schema migration 9->10: USB web UI availability tracking")
	}
	return nil
}

// repairSchema ensures critical columns exist in the devices table
// This is a safety net for cases where migrations were partially applied
// or the schema_version got out of sync with the actual schema
func (s *SQLiteStore) repairSchema() error {
	// Check if devices table exists first
	var tableExists int
	err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='devices'").Scan(&tableExists)
	if err != nil || tableExists == 0 {
		return nil // No devices table, nothing to repair
	}

	// Get existing columns
	existingCols := make(map[string]bool)
	rows, err := s.db.Query("PRAGMA table_info(devices)")
	if err != nil {
		return fmt.Errorf("failed to get table info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			continue
		}
		existingCols[name] = true
	}

	// Critical columns that must exist (added in migrations 9 and 10)
	criticalColumns := []struct {
		name string
		def  string
	}{
		{"device_type", "TEXT DEFAULT 'network'"},
		{"source_type", "TEXT DEFAULT 'snmp'"},
		{"is_usb", "BOOLEAN DEFAULT 0"},
		{"initial_page_count", "INTEGER DEFAULT 0"},
		{"port_name", "TEXT"},
		{"driver_name", "TEXT"},
		{"is_default", "BOOLEAN DEFAULT 0"},
		{"is_shared", "BOOLEAN DEFAULT 0"},
		{"spooler_status", "TEXT"},
		{"usb_webui_available", "BOOLEAN DEFAULT 0"},
	}

	repaired := false
	for _, col := range criticalColumns {
		if !existingCols[col.name] {
			_, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE devices ADD COLUMN %s %s`, col.name, col.def))
			if err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("failed to repair column %s: %w", col.name, err)
			}
			repaired = true
			if storageLogger != nil {
				storageLogger.Warn("Repaired missing column in devices table", "column", col.name)
			}
		}
	}

	if repaired {
		// Set defaults for newly added columns
		_, _ = s.db.Exec(`UPDATE devices SET device_type = 'network', source_type = 'snmp' WHERE device_type IS NULL OR device_type = ''`)
	}

	return nil
}

// Create adds a new device
func (s *SQLiteStore) Create(ctx context.Context, device *Device) error {
	if device.Serial == "" {
		return ErrInvalidSerial
	}

	now := time.Now()
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	if device.FirstSeen.IsZero() {
		device.FirstSeen = now
	}
	if device.LastSeen.IsZero() {
		device.LastSeen = now
	}

	consumablesJSON, _ := json.Marshal(device.Consumables)
	statusJSON, _ := json.Marshal(device.StatusMessages)
	dnsJSON, _ := json.Marshal(device.DNSServers)
	rawJSON, _ := json.Marshal(device.RawData)
	lockedFieldsJSON, _ := json.Marshal(device.LockedFields)

	query := `
		INSERT INTO devices (
			serial, ip, manufacturer, model, hostname, firmware,
			mac_address, subnet_mask, gateway, dns_servers, dhcp_server,
			consumables, status_messages,
			last_seen, created_at, first_seen, is_saved, visible,
			discovery_method, walk_filename, last_scan_id, raw_data,
			asset_number, location, description, web_ui_url, locked_fields,
			device_type, source_type, is_usb, initial_page_count,
			port_name, driver_name, is_default, is_shared, spooler_status, usb_webui_available
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		device.Serial, device.IP, device.Manufacturer, device.Model,
		device.Hostname, device.Firmware, device.MACAddress, device.SubnetMask,
		device.Gateway, string(dnsJSON), device.DHCPServer,
		string(consumablesJSON), string(statusJSON),
		device.LastSeen, device.CreatedAt, device.FirstSeen, device.IsSaved, device.Visible,
		device.DiscoveryMethod, device.WalkFilename, device.LastScanID, string(rawJSON),
		device.AssetNumber, device.Location, device.Description, device.WebUIURL, string(lockedFieldsJSON),
		device.DeviceType, device.SourceType, device.IsUSB, device.InitialPageCount,
		device.PortName, device.DriverName, device.IsDefault, device.IsShared, device.SpoolerStatus,
		device.UsbWebUIAvailable,
	)

	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			if storageLogger != nil {
				storageLogger.Debug("Device already exists", "serial", device.Serial, "ip", device.IP)
			}
			return ErrDuplicate
		}
		if storageLogger != nil {
			storageLogger.Error("Failed to create device", "serial", device.Serial, "ip", device.IP, "error", err)
		}
		return fmt.Errorf("failed to create device: %w", err)
	}

	if storageLogger != nil {
		storageLogger.Info("Device created", "serial", device.Serial, "ip", device.IP, "model", device.Model)
	}

	return nil
}

// Get retrieves a device by serial
func (s *SQLiteStore) Get(ctx context.Context, serial string) (*Device, error) {
	if serial == "" {
		return nil, ErrInvalidSerial
	}

	query := `
		SELECT serial, ip, manufacturer, model, hostname, firmware,
			   mac_address, subnet_mask, gateway, dns_servers, dhcp_server,
			   consumables, status_messages,
			   last_seen, created_at, first_seen, is_saved, visible,
			   discovery_method, walk_filename, last_scan_id, raw_data,
			   asset_number, location, description, web_ui_url, locked_fields,
			   device_type, source_type, is_usb, initial_page_count,
			   port_name, driver_name, is_default, is_shared, spooler_status, usb_webui_available
		FROM devices WHERE serial = ?
	`

	device := &Device{}
	var consumablesJSON, statusJSON, dnsJSON, rawJSON sql.NullString
	var assetNumber, location, description, webUIURL, lockedFieldsJSON sql.NullString
	var deviceType, sourceType, portName, driverName, spoolerStatus sql.NullString
	var manufacturer, model, hostname, firmware, macAddress, subnetMask sql.NullString
	var gateway, dhcpServer, discoveryMethod, walkFilename sql.NullString
	var isUSB, isDefault, isShared, usbWebUIAvailable sql.NullBool
	var initialPageCount sql.NullInt64
	var lastScanID sql.NullInt64

	err := s.db.QueryRowContext(ctx, query, serial).Scan(
		&device.Serial, &device.IP, &manufacturer, &model,
		&hostname, &firmware, &macAddress, &subnetMask,
		&gateway, &dnsJSON, &dhcpServer,
		&consumablesJSON, &statusJSON,
		&device.LastSeen, &device.CreatedAt, &device.FirstSeen, &device.IsSaved, &device.Visible,
		&discoveryMethod, &walkFilename, &lastScanID, &rawJSON,
		&assetNumber, &location, &description, &webUIURL, &lockedFieldsJSON,
		&deviceType, &sourceType, &isUSB, &initialPageCount,
		&portName, &driverName, &isDefault, &isShared, &spoolerStatus, &usbWebUIAvailable,
	)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get device: %w", err)
	}

	// Nullable TEXT columns (may be NULL for devices that never reported these fields)
	device.Manufacturer = manufacturer.String
	device.Model = model.String
	device.Hostname = hostname.String
	device.Firmware = firmware.String
	device.MACAddress = macAddress.String
	device.SubnetMask = subnetMask.String
	device.Gateway = gateway.String
	device.DHCPServer = dhcpServer.String
	device.DiscoveryMethod = discoveryMethod.String
	device.WalkFilename = walkFilename.String
	device.LastScanID = lastScanID.Int64

	// Unmarshal JSON fields
	if consumablesJSON.Valid && consumablesJSON.String != "" {
		json.Unmarshal([]byte(consumablesJSON.String), &device.Consumables)
	}
	if statusJSON.Valid && statusJSON.String != "" {
		json.Unmarshal([]byte(statusJSON.String), &device.StatusMessages)
	}
	if dnsJSON.Valid && dnsJSON.String != "" {
		json.Unmarshal([]byte(dnsJSON.String), &device.DNSServers)
	}
	if rawJSON.Valid && rawJSON.String != "" {
		json.Unmarshal([]byte(rawJSON.String), &device.RawData)
	}
	if assetNumber.Valid {
		device.AssetNumber = assetNumber.String
	}
	if location.Valid {
		device.Location = location.String
	}
	if description.Valid {
		device.Description = description.String
	}
	if webUIURL.Valid {
		device.WebUIURL = webUIURL.String
	}
	if lockedFieldsJSON.Valid && lockedFieldsJSON.String != "" {
		json.Unmarshal([]byte(lockedFieldsJSON.String), &device.LockedFields)
	}
	// New device classification fields
	if deviceType.Valid {
		device.DeviceType = deviceType.String
	}
	if sourceType.Valid {
		device.SourceType = sourceType.String
	}
	if isUSB.Valid {
		device.IsUSB = isUSB.Bool
	}
	if initialPageCount.Valid {
		device.InitialPageCount = int(initialPageCount.Int64)
	}
	if portName.Valid {
		device.PortName = portName.String
	}
	if driverName.Valid {
		device.DriverName = driverName.String
	}
	if isDefault.Valid {
		device.IsDefault = isDefault.Bool
	}
	if isShared.Valid {
		device.IsShared = isShared.Bool
	}
	if spoolerStatus.Valid {
		device.SpoolerStatus = spoolerStatus.String
	}
	if usbWebUIAvailable.Valid {
		device.UsbWebUIAvailable = usbWebUIAvailable.Bool
	}

	return device, nil
}

// Update modifies an existing device
func (s *SQLiteStore) Update(ctx context.Context, device *Device) error {
	if device.Serial == "" {
		return ErrInvalidSerial
	}

	device.LastSeen = time.Now()

	consumablesJSON, _ := json.Marshal(device.Consumables)
	statusJSON, _ := json.Marshal(device.StatusMessages)
	dnsJSON, _ := json.Marshal(device.DNSServers)
	rawJSON, _ := json.Marshal(device.RawData)
	lockedFieldsJSON, _ := json.Marshal(device.LockedFields)

	query := `
		UPDATE devices SET
			ip = ?, manufacturer = ?, model = ?, hostname = ?, firmware = ?,
			mac_address = ?, subnet_mask = ?, gateway = ?, dns_servers = ?, dhcp_server = ?,
			consumables = ?, status_messages = ?,
			last_seen = ?, is_saved = ?, visible = ?,
			discovery_method = ?, walk_filename = ?, last_scan_id = ?, raw_data = ?,
			asset_number = ?, location = ?, description = ?, web_ui_url = ?, locked_fields = ?,
			device_type = ?, source_type = ?, is_usb = ?, initial_page_count = ?,
			port_name = ?, driver_name = ?, is_default = ?, is_shared = ?, spooler_status = ?,
			usb_webui_available = ?
		WHERE serial = ?
	`

	result, err := s.db.ExecContext(ctx, query,
		device.IP, device.Manufacturer, device.Model, device.Hostname, device.Firmware,
		device.MACAddress, device.SubnetMask, device.Gateway, string(dnsJSON), device.DHCPServer,
		string(consumablesJSON), string(statusJSON),
		device.LastSeen, device.IsSaved, device.Visible,
		device.DiscoveryMethod, device.WalkFilename, device.LastScanID, string(rawJSON),
		device.AssetNumber, device.Location, device.Description, device.WebUIURL, string(lockedFieldsJSON),
		device.DeviceType, device.SourceType, device.IsUSB, device.InitialPageCount,
		device.PortName, device.DriverName, device.IsDefault, device.IsShared, device.SpoolerStatus,
		device.UsbWebUIAvailable,
		device.Serial,
	)

	if err != nil {
		return fmt.Errorf("failed to update device: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

// Upsert creates or updates a device using a single INSERT ... ON CONFLICT statement.
// Preserves created_at, first_seen, is_saved, and locked_fields from the existing row.
func (s *SQLiteStore) Upsert(ctx context.Context, device *Device) error {
	if device.Serial == "" {
		return ErrInvalidSerial
	}

	now := time.Now()
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	if device.FirstSeen.IsZero() {
		device.FirstSeen = now
	}
	if device.LastSeen.IsZero() {
		device.LastSeen = now
	}

	consumablesJSON, _ := json.Marshal(device.Consumables)
	statusJSON, _ := json.Marshal(device.StatusMessages)
	dnsJSON, _ := json.Marshal(device.DNSServers)
	rawJSON, _ := json.Marshal(device.RawData)
	lockedFieldsJSON, _ := json.Marshal(device.LockedFields)

	// Single-statement UPSERT: insert or update while preserving important fields from the existing row.
	// On conflict (serial exists), we:
	// - Keep created_at, first_seen, is_saved, locked_fields from the existing row (devices.*)
	// - Update all other fields from incoming data (excluded.*)
	query := `
		INSERT INTO devices (
			serial, ip, manufacturer, model, hostname, firmware,
			mac_address, subnet_mask, gateway, dns_servers, dhcp_server,
			consumables, status_messages,
			last_seen, created_at, first_seen, is_saved, visible,
			discovery_method, walk_filename, last_scan_id, raw_data,
			asset_number, location, description, web_ui_url, locked_fields,
			device_type, source_type, is_usb, initial_page_count,
			port_name, driver_name, is_default, is_shared, spooler_status, usb_webui_available
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(serial) DO UPDATE SET
			ip = excluded.ip,
			manufacturer = excluded.manufacturer,
			model = excluded.model,
			hostname = excluded.hostname,
			firmware = excluded.firmware,
			mac_address = excluded.mac_address,
			subnet_mask = excluded.subnet_mask,
			gateway = excluded.gateway,
			dns_servers = excluded.dns_servers,
			dhcp_server = excluded.dhcp_server,
			consumables = excluded.consumables,
			status_messages = excluded.status_messages,
			last_seen = excluded.last_seen,
			visible = excluded.visible,
			discovery_method = excluded.discovery_method,
			walk_filename = excluded.walk_filename,
			last_scan_id = excluded.last_scan_id,
			raw_data = excluded.raw_data,
			asset_number = excluded.asset_number,
			location = excluded.location,
			description = excluded.description,
			web_ui_url = excluded.web_ui_url,
			device_type = excluded.device_type,
			source_type = excluded.source_type,
			is_usb = excluded.is_usb,
			port_name = excluded.port_name,
			driver_name = excluded.driver_name,
			is_default = excluded.is_default,
			is_shared = excluded.is_shared,
			spooler_status = excluded.spooler_status,
			usb_webui_available = excluded.usb_webui_available
			-- IMPORTANT: created_at, first_seen, is_saved, locked_fields, initial_page_count are NOT updated (preserved from existing row)
	`

	_, err := s.db.ExecContext(ctx, query,
		device.Serial, device.IP, device.Manufacturer, device.Model,
		device.Hostname, device.Firmware, device.MACAddress, device.SubnetMask,
		device.Gateway, string(dnsJSON), device.DHCPServer,
		string(consumablesJSON), string(statusJSON),
		device.LastSeen, device.CreatedAt, device.FirstSeen, device.IsSaved, device.Visible,
		device.DiscoveryMethod, device.WalkFilename, device.LastScanID, string(rawJSON),
		device.AssetNumber, device.Location, device.Description, device.WebUIURL, string(lockedFieldsJSON),
		device.DeviceType, device.SourceType, device.IsUSB, device.InitialPageCount,
		device.PortName, device.DriverName, device.IsDefault, device.IsShared, device.SpoolerStatus,
		device.UsbWebUIAvailable,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert device: %w", err)
	}
	return nil
}

// StoreDiscoveryAtomic persists a discovered device update as a single atomic unit:
// - upsert device row
// - append scan history snapshot
// - save metrics snapshot
// If any step fails, no partial writes are committed.
func (s *SQLiteStore) StoreDiscoveryAtomic(ctx context.Context, device *Device, scan *ScanSnapshot, metrics *MetricsSnapshot) error {
	if device == nil {
		return fmt.Errorf("device is nil")
	}
	if scan == nil {
		return fmt.Errorf("scan is nil")
	}
	if metrics == nil {
		return fmt.Errorf("metrics is nil")
	}
	if device.Serial == "" || scan.Serial == "" || metrics.Serial == "" {
		return ErrInvalidSerial
	}
	if device.Serial != scan.Serial || device.Serial != metrics.Serial {
		return fmt.Errorf("serial mismatch between device/scan/metrics")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := s.upsertWithExecer(ctx, tx, device); err != nil {
		return err
	}
	if err := s.addScanHistoryWithExecer(ctx, tx, scan); err != nil {
		return err
	}
	if err := s.saveMetricsSnapshotWithExecer(ctx, tx, metrics); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	committed = true
	return nil
}

// Delete removes a device by serial
func (s *SQLiteStore) Delete(ctx context.Context, serial string) error {
	if serial == "" {
		return ErrInvalidSerial
	}

	result, err := s.db.ExecContext(ctx, "DELETE FROM devices WHERE serial = ?", serial)
	if err != nil {
		return fmt.Errorf("failed to delete device: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

// List returns devices matching the filter
func (s *SQLiteStore) List(ctx context.Context, filter DeviceFilter) ([]*Device, error) {
	query := `
		SELECT serial, ip, manufacturer, model, hostname, firmware,
			   mac_address, subnet_mask, gateway, dns_servers, dhcp_server,
			   consumables, status_messages,
			   last_seen, created_at, first_seen, is_saved, visible,
			   discovery_method, walk_filename, last_scan_id, raw_data,
			   asset_number, location, description, web_ui_url, locked_fields,
			   device_type, source_type, is_usb, initial_page_count,
			   port_name, driver_name, is_default, is_shared, spooler_status, usb_webui_available
		FROM devices WHERE 1=1
	`
	args := []interface{}{}

	if filter.IsSaved != nil {
		query += " AND is_saved = ?"
		args = append(args, *filter.IsSaved)
	}
	if filter.Visible != nil {
		query += " AND visible = ?"
		args = append(args, *filter.Visible)
	}
	if filter.IP != "" {
		query += " AND ip = ?"
		args = append(args, filter.IP)
	}
	if filter.Serial != "" {
		query += " AND serial = ?"
		args = append(args, filter.Serial)
	}
	if filter.Manufacturer != "" {
		query += " AND manufacturer LIKE ?"
		args = append(args, "%"+filter.Manufacturer+"%")
	}
	if filter.LastSeenAfter != nil {
		query += " AND last_seen > ?"
		args = append(args, *filter.LastSeenAfter)
	}
	if filter.DeviceType != "" {
		query += " AND device_type = ?"
		args = append(args, filter.DeviceType)
	}
	if filter.SourceType != "" {
		query += " AND source_type = ?"
		args = append(args, filter.SourceType)
	}
	if filter.IsUSB != nil {
		query += " AND is_usb = ?"
		args = append(args, *filter.IsUSB)
	}

	query += " ORDER BY last_seen DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %w", err)
	}
	defer rows.Close()

	devices := []*Device{}
	for rows.Next() {
		device := &Device{}
		var consumablesJSON, statusJSON, dnsJSON, rawJSON sql.NullString
		var assetNumber, location, description, webUIURL, lockedFieldsJSON sql.NullString
		var deviceType, sourceType, portName, driverName, spoolerStatus sql.NullString
		var manufacturer, model, hostname, firmware, macAddress, subnetMask sql.NullString
		var gateway, dhcpServer, discoveryMethod, walkFilename sql.NullString
		var isUSB, isDefault, isShared, usbWebUIAvailable sql.NullBool
		var initialPageCount sql.NullInt64
		var lastScanID sql.NullInt64

		err := rows.Scan(
			&device.Serial, &device.IP, &manufacturer, &model,
			&hostname, &firmware, &macAddress, &subnetMask,
			&gateway, &dnsJSON, &dhcpServer,
			&consumablesJSON, &statusJSON,
			&device.LastSeen, &device.CreatedAt, &device.FirstSeen, &device.IsSaved, &device.Visible,
			&discoveryMethod, &walkFilename, &lastScanID, &rawJSON,
			&assetNumber, &location, &description, &webUIURL, &lockedFieldsJSON,
			&deviceType, &sourceType, &isUSB, &initialPageCount,
			&portName, &driverName, &isDefault, &isShared, &spoolerStatus, &usbWebUIAvailable,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan device: %w", err)
		}

		// Nullable TEXT columns (may be NULL for devices that never reported these fields)
		device.Manufacturer = manufacturer.String
		device.Model = model.String
		device.Hostname = hostname.String
		device.Firmware = firmware.String
		device.MACAddress = macAddress.String
		device.SubnetMask = subnetMask.String
		device.Gateway = gateway.String
		device.DHCPServer = dhcpServer.String
		device.DiscoveryMethod = discoveryMethod.String
		device.WalkFilename = walkFilename.String
		device.LastScanID = lastScanID.Int64

		// Unmarshal JSON fields
		if consumablesJSON.Valid && consumablesJSON.String != "" {
			json.Unmarshal([]byte(consumablesJSON.String), &device.Consumables)
		}
		if statusJSON.Valid && statusJSON.String != "" {
			json.Unmarshal([]byte(statusJSON.String), &device.StatusMessages)
		}
		if dnsJSON.Valid && dnsJSON.String != "" {
			json.Unmarshal([]byte(dnsJSON.String), &device.DNSServers)
		}
		if rawJSON.Valid && rawJSON.String != "" {
			json.Unmarshal([]byte(rawJSON.String), &device.RawData)
		}
		if assetNumber.Valid {
			device.AssetNumber = assetNumber.String
		}
		if location.Valid {
			device.Location = location.String
		}
		if description.Valid {
			device.Description = description.String
		}
		if webUIURL.Valid {
			device.WebUIURL = webUIURL.String
		}
		if lockedFieldsJSON.Valid && lockedFieldsJSON.String != "" {
			json.Unmarshal([]byte(lockedFieldsJSON.String), &device.LockedFields)
		}
		// New device classification fields
		if deviceType.Valid {
			device.DeviceType = deviceType.String
		}
		if sourceType.Valid {
			device.SourceType = sourceType.String
		}
		if isUSB.Valid {
			device.IsUSB = isUSB.Bool
		}
		if initialPageCount.Valid {
			device.InitialPageCount = int(initialPageCount.Int64)
		}
		if portName.Valid {
			device.PortName = portName.String
		}
		if driverName.Valid {
			device.DriverName = driverName.String
		}
		if isDefault.Valid {
			device.IsDefault = isDefault.Bool
		}
		if isShared.Valid {
			device.IsShared = isShared.Bool
		}
		if spoolerStatus.Valid {
			device.SpoolerStatus = spoolerStatus.String
		}
		if usbWebUIAvailable.Valid {
			device.UsbWebUIAvailable = usbWebUIAvailable.Bool
		}

		devices = append(devices, device)
	}

	return devices, rows.Err()
}

// TouchDeviceSeen records confirmed identity without rewriting scan data or
// resurrecting a deleted device. An IP change or newer observation wins.
func (s *SQLiteStore) TouchDeviceSeen(ctx context.Context, serial, ip string, observedAt time.Time) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen = ? WHERE serial = ? AND ip = ? AND last_seen <= ?`,
		observedAt, serial, ip, observedAt)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkSaved sets is_saved=true for a device; saving is not a liveness observation.
func (s *SQLiteStore) MarkSaved(ctx context.Context, serial string) error {
	if serial == "" {
		return ErrInvalidSerial
	}

	result, err := s.db.ExecContext(ctx, "UPDATE devices SET is_saved = 1 WHERE serial = ?", serial)
	if err != nil {
		return fmt.Errorf("failed to mark device as saved: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

// MarkAllSaved sets is_saved=true for all visible, unsaved devices
func (s *SQLiteStore) MarkAllSaved(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx,
		"UPDATE devices SET is_saved = 1 WHERE is_saved = 0 AND visible = 1")
	if err != nil {
		return 0, fmt.Errorf("failed to mark all devices as saved: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// MarkDiscovered sets is_saved=false for a device
func (s *SQLiteStore) MarkDiscovered(ctx context.Context, serial string) error {
	if serial == "" {
		return ErrInvalidSerial
	}

	result, err := s.db.ExecContext(ctx, "UPDATE devices SET is_saved = 0 WHERE serial = ?", serial)
	if err != nil {
		return fmt.Errorf("failed to mark device as discovered: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

// DeleteAll removes all devices matching the filter
func (s *SQLiteStore) DeleteAll(ctx context.Context, filter DeviceFilter) (int, error) {
	query := "DELETE FROM devices WHERE 1=1"
	args := []interface{}{}

	if filter.IsSaved != nil {
		query += " AND is_saved = ?"
		args = append(args, *filter.IsSaved)
	}
	if filter.IP != "" {
		query += " AND ip = ?"
		args = append(args, filter.IP)
	}
	if filter.Manufacturer != "" {
		query += " AND manufacturer LIKE ?"
		args = append(args, "%"+filter.Manufacturer+"%")
	}

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to delete devices: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// Stats returns storage statistics
func (s *SQLiteStore) Stats(ctx context.Context) (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	var total, saved, discovered, visible, hidden, totalScans int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices").Scan(&total)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE is_saved = 1").Scan(&saved)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE is_saved = 0").Scan(&discovered)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE visible = 1").Scan(&visible)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE visible = 0").Scan(&hidden)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM scan_history").Scan(&totalScans)

	stats["total_devices"] = total
	stats["saved_devices"] = saved
	stats["discovered_devices"] = discovered
	stats["visible_devices"] = visible
	stats["hidden_devices"] = hidden
	stats["total_scans"] = totalScans

	return stats, nil
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// AddScanHistory records a new scan snapshot for a device
// Note: This tracks device state changes (IP, hostname, firmware) for audit purposes.
// Metrics data (page counts, toner levels) should be stored using SaveMetricsSnapshot instead.
func (s *SQLiteStore) AddScanHistory(ctx context.Context, scan *ScanSnapshot) error {
	return s.addScanHistoryWithExecer(ctx, s.db, scan)
}

func (s *SQLiteStore) addScanHistoryWithExecer(ctx context.Context, ex execer, scan *ScanSnapshot) error {
	if scan.Serial == "" {
		return ErrInvalidSerial
	}

	// Marshal complex fields to JSON
	consumablesJSON, _ := json.Marshal(scan.Consumables)
	statusJSON, _ := json.Marshal(scan.StatusMessages)

	query := `
		INSERT INTO scan_history (
			serial, created_at, ip, hostname, firmware, 
			consumables, status_messages,
			discovery_method, walk_filename, raw_data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	result, err := ex.ExecContext(ctx, query,
		scan.Serial,
		scan.CreatedAt,
		scan.IP,
		scan.Hostname,
		scan.Firmware,
		consumablesJSON,
		statusJSON,
		scan.DiscoveryMethod,
		scan.WalkFilename,
		scan.RawData,
	)
	if err != nil {
		return fmt.Errorf("failed to add scan history: %w", err)
	}

	// Update device's last_scan_id
	lastInsertID, _ := result.LastInsertId()
	_, err = ex.ExecContext(ctx,
		"UPDATE devices SET last_scan_id = ? WHERE serial = ?",
		lastInsertID, scan.Serial)

	return err
}

// GetScanHistory returns the last N scan snapshots for a device, newest first
func (s *SQLiteStore) GetScanHistory(ctx context.Context, serial string, limit int) ([]*ScanSnapshot, error) {
	if serial == "" {
		return nil, ErrInvalidSerial
	}
	if limit <= 0 {
		limit = 10 // Default limit
	}

	query := `
		SELECT id, serial, created_at, ip, hostname, firmware,
		       consumables, status_messages,
		       discovery_method, walk_filename, raw_data
		FROM scan_history
		WHERE serial = ?
		ORDER BY created_at DESC
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, serial, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query scan history: %w", err)
	}
	defer rows.Close()

	var scans []*ScanSnapshot
	for rows.Next() {
		scan := &ScanSnapshot{}
		var consumablesJSON, statusJSON, rawDataJSON sql.NullString

		err := rows.Scan(
			&scan.ID,
			&scan.Serial,
			&scan.CreatedAt,
			&scan.IP,
			&scan.Hostname,
			&scan.Firmware,
			&consumablesJSON,
			&statusJSON,
			&scan.DiscoveryMethod,
			&scan.WalkFilename,
			&rawDataJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Unmarshal JSON fields
		if consumablesJSON.Valid {
			json.Unmarshal([]byte(consumablesJSON.String), &scan.Consumables)
		}
		if statusJSON.Valid {
			json.Unmarshal([]byte(statusJSON.String), &scan.StatusMessages)
		}
		if rawDataJSON.Valid {
			scan.RawData = json.RawMessage(rawDataJSON.String)
		}

		scans = append(scans, scan)
	}

	return scans, rows.Err()
}

// DeleteOldScans removes scan history older than the given timestamp
func (s *SQLiteStore) DeleteOldScans(ctx context.Context, olderThan int64) (int, error) {
	cutoffTime := time.Unix(olderThan, 0)

	result, err := s.db.ExecContext(ctx,
		"DELETE FROM scan_history WHERE created_at < ?",
		cutoffTime)
	if err != nil {
		return 0, fmt.Errorf("failed to delete old scans: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// HideDiscovered sets visible=false for all devices where is_saved=false
func (s *SQLiteStore) HideDiscovered(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx,
		"UPDATE devices SET visible = 0 WHERE is_saved = 0")
	if err != nil {
		return 0, fmt.Errorf("failed to hide discovered devices: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// ShowAll sets visible=true for all devices
func (s *SQLiteStore) ShowAll(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx,
		"UPDATE devices SET visible = 1")
	if err != nil {
		return 0, fmt.Errorf("failed to show all devices: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// MetricsSnapshot represents a point-in-time snapshot of device metrics with agent-specific fields
type MetricsSnapshot struct {
	commonstorage.MetricsSnapshot // Embed common fields

	// HP-specific detailed impression counters (agent-specific)
	FaxPages          int `json:"fax_pages,omitempty"`
	CopyPages         int `json:"copy_pages,omitempty"`
	OtherPages        int `json:"other_pages,omitempty"`
	CopyMonoPages     int `json:"copy_mono_pages,omitempty"`
	CopyFlatbedScans  int `json:"copy_flatbed_scans,omitempty"`
	CopyADFScans      int `json:"copy_adf_scans,omitempty"`
	FaxFlatbedScans   int `json:"fax_flatbed_scans,omitempty"`
	FaxADFScans       int `json:"fax_adf_scans,omitempty"`
	ScanToHostFlatbed int `json:"scan_to_host_flatbed,omitempty"`
	ScanToHostADF     int `json:"scan_to_host_adf,omitempty"`
	DuplexSheets      int `json:"duplex_sheets,omitempty"`
	JamEvents         int `json:"jam_events,omitempty"`
	ScannerJamEvents  int `json:"scanner_jam_events,omitempty"`
	// Tier indicates which storage tier this snapshot came from (raw/hourly/daily/monthly)
	Tier string `json:"tier,omitempty"`
}

// SaveMetricsSnapshot stores a metrics snapshot for a device
func (s *SQLiteStore) SaveMetricsSnapshot(ctx context.Context, snapshot *MetricsSnapshot) error {
	return s.saveMetricsSnapshotWithExecer(ctx, s.db, snapshot)
}

func (s *SQLiteStore) saveMetricsSnapshotWithExecer(ctx context.Context, ex execer, snapshot *MetricsSnapshot) error {
	if snapshot.Serial == "" {
		return ErrInvalidSerial
	}

	if snapshot.Timestamp.IsZero() {
		snapshot.Timestamp = time.Now()
	}
	// Normalize to UTC for consistent storage/comparison
	snapshot.Timestamp = snapshot.Timestamp.UTC()

	tonerJSON, _ := json.Marshal(snapshot.TonerLevels)

	// Ignore snapshots where all primary counters are zero — likely an error/reset from device
	if snapshot.PageCount == 0 && snapshot.ColorPages == 0 && snapshot.MonoPages == 0 && snapshot.ScanCount == 0 {
		if storageLogger != nil {
			storageLogger.WarnRateLimited("metrics_zero_"+snapshot.Serial, 5*time.Minute,
				"Ignoring metrics snapshot where all counters are zero",
				"serial", snapshot.Serial)
		}
		return nil
	}

	// Validation: Check if color + mono equals total (when all three are present)
	// This helps identify inconsistent vendor parsing
	if snapshot.PageCount > 0 && snapshot.ColorPages > 0 && snapshot.MonoPages > 0 {
		partsSum := snapshot.ColorPages + snapshot.MonoPages
		diff := snapshot.PageCount - partsSum
		// Allow small discrepancy (e.g., from copy/fax pages not included in parts)
		if diff < 0 || diff > snapshot.PageCount/10 { // >10% discrepancy
			if storageLogger != nil {
				storageLogger.WarnRateLimited("metrics_mismatch_"+snapshot.Serial, 10*time.Minute,
					"Metrics mismatch: total != color + mono",
					"serial", snapshot.Serial,
					"total", snapshot.PageCount,
					"color", snapshot.ColorPages,
					"mono", snapshot.MonoPages,
					"diff", diff)
			}
		}
	}

	// =============================================================================
	// METRICS VALIDATION - Two Simple Rules
	// =============================================================================
	// Rule 1: Counts only go up (cumulative counters never decrease)
	// Rule 2: Parts must equal whole (color + mono ≈ total)
	// =============================================================================

	// Debug: log incoming metrics for troubleshooting
	if storageLogger != nil {
		storageLogger.Debug("Metrics snapshot received",
			"serial", snapshot.Serial,
			"total", snapshot.PageCount,
			"color", snapshot.ColorPages,
			"mono", snapshot.MonoPages,
			"scan", snapshot.ScanCount)
	}

	if latest, err := s.GetLatestMetrics(ctx, snapshot.Serial); err == nil && latest != nil {

		// Debug: log comparison with previous values
		if storageLogger != nil {
			storageLogger.Debug("Comparing with previous metrics",
				"serial", snapshot.Serial,
				"prev_total", latest.PageCount,
				"prev_color", latest.ColorPages,
				"prev_mono", latest.MonoPages,
				"prev_scan", latest.ScanCount)
		}

		// --- RULE 1: Counts only go up ---
		// Cumulative counters should never decrease (except for noise/rounding)
		isSignificantDecrease := func(incoming, previous int, name string) bool {
			if previous <= 0 || incoming >= previous {
				return false
			}
			decrease := previous - incoming
			threshold := previous / 20 // 5% tolerance
			if threshold < 10 {
				threshold = 10
			}
			if decrease > threshold {
				if storageLogger != nil {
					storageLogger.WarnRateLimited("metrics_decrease_"+name+"_"+snapshot.Serial, 1*time.Minute,
						"Dropping metrics: "+name+" decreased", "serial", snapshot.Serial,
						"previous", previous, "incoming", incoming, "decrease", decrease)
				}
				return true
			}
			return false
		}

		if isSignificantDecrease(snapshot.PageCount, latest.PageCount, "page_count") {
			return nil
		}
		if isSignificantDecrease(snapshot.ColorPages, latest.ColorPages, "color_pages") {
			return nil
		}
		if isSignificantDecrease(snapshot.MonoPages, latest.MonoPages, "mono_pages") {
			return nil
		}
		if isSignificantDecrease(snapshot.ScanCount, latest.ScanCount, "scan_count") {
			return nil
		}
	}

	// --- RULE 2: Parts must equal whole ---
	// If we have both breakdown AND total, they should roughly match
	// color + mono should be close to total (within 10%)
	if snapshot.PageCount > 0 && (snapshot.ColorPages > 0 || snapshot.MonoPages > 0) {
		partsSum := snapshot.ColorPages + snapshot.MonoPages
		if partsSum > 0 {
			diff := snapshot.PageCount - partsSum
			if diff < 0 {
				diff = -diff
			}
			// Allow 10% mismatch (some devices count differently)
			tolerance := snapshot.PageCount / 10
			if tolerance < 100 {
				tolerance = 100
			}
			if diff > tolerance {
				if storageLogger != nil {
					storageLogger.WarnRateLimited("metrics_mismatch_"+snapshot.Serial, 1*time.Minute,
						"Dropping metrics: color+mono doesn't match total", "serial", snapshot.Serial,
						"total", snapshot.PageCount, "color", snapshot.ColorPages, "mono", snapshot.MonoPages,
						"parts_sum", partsSum, "diff", diff)
				}
				return nil
			}
		}
	}

	query := `
		INSERT INTO metrics_raw (
			serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	// Store timestamp as RFC3339Nano UTC string to ensure consistent lexicographic
	// ordering and comparability inside SQLite.
	tsStr := snapshot.Timestamp.Format(time.RFC3339Nano)
	result, err := ex.ExecContext(ctx, query,
		snapshot.Serial, tsStr, snapshot.PageCount,
		snapshot.ColorPages, snapshot.MonoPages, snapshot.ScanCount,
		string(tonerJSON),
	)

	if err != nil {
		if storageLogger != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				storageLogger.WarnRateLimited("metrics_fk_"+snapshot.Serial, 1*time.Minute,
					"Metrics save failed: device not found", "serial", snapshot.Serial)
			} else {
				storageLogger.Error("Failed to save metrics snapshot", "serial", snapshot.Serial, "error", err)
			}
		}
		return fmt.Errorf("failed to save metrics snapshot: %w", err)
	}

	id, _ := result.LastInsertId()
	snapshot.ID = id

	if storageLogger != nil {
		storageLogger.Debug("Metrics snapshot saved",
			"serial", snapshot.Serial,
			"id", id,
			"total", snapshot.PageCount,
			"color", snapshot.ColorPages,
			"mono", snapshot.MonoPages)
	}

	return nil
}

func (s *SQLiteStore) upsertWithExecer(ctx context.Context, ex execer, device *Device) error {
	if device.Serial == "" {
		return ErrInvalidSerial
	}

	now := time.Now()
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	if device.FirstSeen.IsZero() {
		device.FirstSeen = now
	}
	if device.LastSeen.IsZero() {
		device.LastSeen = now
	}

	consumablesJSON, _ := json.Marshal(device.Consumables)
	statusJSON, _ := json.Marshal(device.StatusMessages)
	dnsJSON, _ := json.Marshal(device.DNSServers)
	rawJSON, _ := json.Marshal(device.RawData)
	lockedFieldsJSON, _ := json.Marshal(device.LockedFields)

	// Single-statement UPSERT: insert or update while preserving important fields from the existing row.
	// On conflict (serial exists), we:
	// - Keep created_at, first_seen, is_saved, locked_fields, initial_page_count from the existing row (devices.*)
	// - Update all other fields from incoming data (excluded.*)
	query := `
		INSERT INTO devices (
			serial, ip, manufacturer, model, hostname, firmware,
			mac_address, subnet_mask, gateway, dns_servers, dhcp_server,
			consumables, status_messages,
			last_seen, created_at, first_seen, is_saved, visible,
			discovery_method, walk_filename, last_scan_id, raw_data,
			asset_number, location, description, web_ui_url, locked_fields,
			device_type, source_type, is_usb, initial_page_count,
			port_name, driver_name, is_default, is_shared, spooler_status, usb_webui_available
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(serial) DO UPDATE SET
			ip = excluded.ip,
			manufacturer = excluded.manufacturer,
			model = excluded.model,
			hostname = excluded.hostname,
			firmware = excluded.firmware,
			mac_address = excluded.mac_address,
			subnet_mask = excluded.subnet_mask,
			gateway = excluded.gateway,
			dns_servers = excluded.dns_servers,
			dhcp_server = excluded.dhcp_server,
			consumables = excluded.consumables,
			status_messages = excluded.status_messages,
			last_seen = excluded.last_seen,
			visible = excluded.visible,
			discovery_method = excluded.discovery_method,
			walk_filename = excluded.walk_filename,
			last_scan_id = excluded.last_scan_id,
			raw_data = excluded.raw_data,
			asset_number = excluded.asset_number,
			location = excluded.location,
			description = excluded.description,
			web_ui_url = excluded.web_ui_url,
			device_type = excluded.device_type,
			source_type = excluded.source_type,
			is_usb = excluded.is_usb,
			port_name = excluded.port_name,
			driver_name = excluded.driver_name,
			is_default = excluded.is_default,
			is_shared = excluded.is_shared,
			spooler_status = excluded.spooler_status,
			usb_webui_available = excluded.usb_webui_available
			-- IMPORTANT: created_at, first_seen, is_saved, locked_fields, initial_page_count are NOT updated (preserved from existing row)
	`

	_, err := ex.ExecContext(ctx, query,
		device.Serial, device.IP, device.Manufacturer, device.Model,
		device.Hostname, device.Firmware, device.MACAddress, device.SubnetMask,
		device.Gateway, string(dnsJSON), device.DHCPServer,
		string(consumablesJSON), string(statusJSON),
		device.LastSeen, device.CreatedAt, device.FirstSeen, device.IsSaved, device.Visible,
		device.DiscoveryMethod, device.WalkFilename, device.LastScanID, string(rawJSON),
		device.AssetNumber, device.Location, device.Description, device.WebUIURL, string(lockedFieldsJSON),
		device.DeviceType, device.SourceType, device.IsUSB, device.InitialPageCount,
		device.PortName, device.DriverName, device.IsDefault, device.IsShared, device.SpoolerStatus,
		device.UsbWebUIAvailable,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert device: %w", err)
	}
	return nil
}

// GetMetricsHistory retrieves metrics history for a device within a time range
func (s *SQLiteStore) GetMetricsHistory(ctx context.Context, serial string, since time.Time, until time.Time) ([]*MetricsSnapshot, error) {
	if serial == "" {
		return nil, ErrInvalidSerial
	}

	// Now that timestamps are stored as UTC RFC3339Nano strings we can use
	// indexed range queries in SQL. This is faster and avoids scanning all rows.
	query := `
		SELECT id, serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels,
			   fax_pages, copy_pages, other_pages, copy_mono_pages, copy_flatbed_scans, copy_adf_scans,
			   fax_flatbed_scans, fax_adf_scans, scan_to_host_flatbed, scan_to_host_adf,
			   duplex_sheets, jam_events, scanner_jam_events
		FROM metrics_raw
		WHERE serial = ? AND timestamp >= ? AND timestamp <= ?
		ORDER BY timestamp ASC
	`

	sinceStr := since.UTC().Format(time.RFC3339Nano)
	untilStr := until.UTC().Format(time.RFC3339Nano)

	rows, err := s.db.QueryContext(ctx, query, serial, sinceStr, untilStr)
	if err != nil {
		return nil, fmt.Errorf("failed to query metrics history: %w", err)
	}
	defer rows.Close()

	var snapshots []*MetricsSnapshot
	for rows.Next() {
		snapshot := &MetricsSnapshot{}
		var tonerJSON sql.NullString

		err := rows.Scan(
			&snapshot.ID, &snapshot.Serial, &snapshot.Timestamp,
			&snapshot.PageCount, &snapshot.ColorPages, &snapshot.MonoPages,
			&snapshot.ScanCount, &tonerJSON,
			&snapshot.FaxPages, &snapshot.CopyPages, &snapshot.OtherPages, &snapshot.CopyMonoPages,
			&snapshot.CopyFlatbedScans, &snapshot.CopyADFScans, &snapshot.FaxFlatbedScans, &snapshot.FaxADFScans,
			&snapshot.ScanToHostFlatbed, &snapshot.ScanToHostADF, &snapshot.DuplexSheets,
			&snapshot.JamEvents, &snapshot.ScannerJamEvents,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan metrics row: %w", err)
		}

		if tonerJSON.Valid {
			json.Unmarshal([]byte(tonerJSON.String), &snapshot.TonerLevels)
		}

		snapshots = append(snapshots, snapshot)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating metrics rows: %w", err)
	}

	return snapshots, nil
}

// GetLatestMetrics retrieves the most recent metrics snapshot for a device
func (s *SQLiteStore) GetLatestMetrics(ctx context.Context, serial string) (*MetricsSnapshot, error) {
	if serial == "" {
		return nil, ErrInvalidSerial
	}

	query := `
		SELECT id, serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels
		FROM metrics_raw
		WHERE serial = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`

	snapshot := &MetricsSnapshot{}
	var tonerJSON sql.NullString

	err := s.db.QueryRowContext(ctx, query, serial).Scan(
		&snapshot.ID, &snapshot.Serial, &snapshot.Timestamp,
		&snapshot.PageCount, &snapshot.ColorPages, &snapshot.MonoPages,
		&snapshot.ScanCount, &tonerJSON,
	)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get latest metrics: %w", err)
	}

	if tonerJSON.Valid {
		json.Unmarshal([]byte(tonerJSON.String), &snapshot.TonerLevels)
	}

	return snapshot, nil
}

// DeleteOldMetrics removes metrics history older than the given timestamp
// TODO: Update to handle tiered cleanup (raw>7d, hourly>30d, daily>365d)
func (s *SQLiteStore) DeleteOldMetrics(ctx context.Context, olderThan time.Time) (int, error) {
	olderStr := olderThan.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM metrics_raw WHERE timestamp < ?",
		olderStr)
	if err != nil {
		return 0, fmt.Errorf("failed to delete old metrics: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// DeleteMetricByID removes a single metrics row by id from the specified tier/table.
// If tier is empty, attempt to delete from known metric tables until one succeeds.
func (s *SQLiteStore) DeleteMetricByID(ctx context.Context, tier string, id int64) error {
	tables := []string{"metrics_raw", "metrics_hourly", "metrics_daily", "metrics_monthly"}
	if tier != "" {
		// Normalize tier input to table name - only allow known values to prevent SQL injection
		switch tier {
		case "raw":
			tables = []string{"metrics_raw"}
		case "hourly":
			tables = []string{"metrics_hourly"}
		case "daily":
			tables = []string{"metrics_daily"}
		case "monthly":
			tables = []string{"metrics_monthly"}
		default:
			// Reject unknown tier values to prevent SQL injection
			return fmt.Errorf("unknown metrics tier: %s", tier)
		}
	}

	for _, tbl := range tables {
		res, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", tbl), id)
		if err != nil {
			// If table doesn't exist or other error, continue to next
			continue
		}
		rows, _ := res.RowsAffected()
		if rows > 0 {
			if storageLogger != nil {
				storageLogger.Info("Deleted metrics row", "table", tbl, "id", id)
			}
			return nil
		}
	}

	return fmt.Errorf("metrics row id %d not found", id)
}

// DeleteOldHiddenDevices removes devices that are hidden and older than timestamp
func (s *SQLiteStore) DeleteOldHiddenDevices(ctx context.Context, olderThan int64) (int, error) {
	cutoffTime := time.Unix(olderThan, 0)

	result, err := s.db.ExecContext(ctx,
		"DELETE FROM devices WHERE visible = 0 AND last_seen < ?",
		cutoffTime)
	if err != nil {
		return 0, fmt.Errorf("failed to delete old hidden devices: %w", err)
	}

	rows, _ := result.RowsAffected()
	return int(rows), nil
}
