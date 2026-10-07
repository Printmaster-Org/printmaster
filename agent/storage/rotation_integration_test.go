package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDatabaseRotationOnMigrationFailure simulates the scenario where
// a database migration fails and verifies the database is rotated
func TestDatabaseRotationOnMigrationFailure(t *testing.T) {
	t.Parallel()

	// Create a temporary directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create a database with an intentionally broken migration state
	// This simulates the exact error from the issue:
	// "there is already another table or index with this name: metrics_raw"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	// Create schema version table and set to version 7
	_, err = db.Exec(`
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version (version) VALUES (7);
	`)
	if err != nil {
		t.Fatalf("Failed to create schema version: %v", err)
	}

	// Create metrics_history table (old name)
	_, err = db.Exec(`
		CREATE TABLE metrics_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			serial TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			page_count INTEGER DEFAULT 0
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create metrics_history: %v", err)
	}

	// Also create metrics_raw table (this will cause the migration to fail)
	_, err = db.Exec(`
		CREATE TABLE metrics_raw (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			serial TEXT NOT NULL,
			timestamp DATETIME NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create metrics_raw: %v", err)
	}

	db.Close()

	// Verify the broken database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("Test database was not created")
	}

	// Now try to open with NewSQLiteStore - it should rotate and create a fresh DB
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed even after rotation: %v", err)
	}
	defer store.Close()

	// Verify a backup was created
	backupFiles, err := filepath.Glob(filepath.Join(tmpDir, "test.db.backup.*"))
	if err != nil {
		t.Fatalf("Failed to find backup files: %v", err)
	}
	if len(backupFiles) == 0 {
		t.Error("No backup file was created during rotation")
	}

	// Verify the new database is clean and has the correct schema version
	var currentVersion int
	err = store.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&currentVersion)
	if err != nil {
		t.Fatalf("Failed to check schema version: %v", err)
	}
	if currentVersion < 8 {
		t.Errorf("Expected schema version >= 8, got %d", currentVersion)
	}

	// Verify metrics_raw table exists in the new database
	var tableExists int
	err = store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='metrics_raw'").Scan(&tableExists)
	if err != nil {
		t.Fatalf("Failed to check for metrics_raw table: %v", err)
	}
	if tableExists == 0 {
		t.Error("metrics_raw table should exist in fresh database")
	}

	t.Logf("Successfully rotated broken database and created fresh one")
	t.Logf("Backup file: %s", backupFiles[0])
}

// TestDatabaseRotationLogging verifies that proper logging occurs during rotation
func TestDatabaseRotationLogging(t *testing.T) {
	// Not parallel: swaps the package-global logger used by concurrent tests.

	// Create a simple logger that captures messages
	var logMessages []string
	testLogger := &testLogger{
		errorFunc: func(msg string, args ...interface{}) {
			logMessages = append(logMessages, "ERROR: "+msg)
		},
		warnFunc: func(msg string, args ...interface{}) {
			logMessages = append(logMessages, "WARN: "+msg)
		},
		infoFunc: func(msg string, args ...interface{}) {
			logMessages = append(logMessages, "INFO: "+msg)
		},
	}

	// Set the test logger
	oldLogger := storageLogger
	SetLogger(testLogger)
	defer SetLogger(oldLogger)

	// Create a temporary directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create a broken database (same as above)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version (version) VALUES (7);
		CREATE TABLE metrics_history (id INTEGER PRIMARY KEY);
		CREATE TABLE metrics_raw (id INTEGER PRIMARY KEY);
	`)
	if err != nil {
		t.Fatalf("Failed to create broken schema: %v", err)
	}
	db.Close()

	// Try to open - should rotate
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer store.Close()

	// Verify logging occurred
	var foundError, foundWarn bool
	for _, msg := range logMessages {
		if strings.Contains(msg, "ERROR") && strings.Contains(msg, "schema initialization failed") {
			foundError = true
		}
		if strings.Contains(msg, "WARN") && strings.Contains(msg, "Database rotated") {
			foundWarn = true
		}
	}

	if !foundError {
		t.Error("Expected ERROR log message about schema initialization failure")
	}
	if !foundWarn {
		t.Error("Expected WARN log message about database rotation")
	}

	t.Logf("Captured %d log messages", len(logMessages))
	for _, msg := range logMessages {
		t.Logf("  %s", msg)
	}
}

// Simple test logger implementation
type testLogger struct {
	errorFunc func(string, ...interface{})
	warnFunc  func(string, ...interface{})
	infoFunc  func(string, ...interface{})
}

func (l *testLogger) Error(msg string, args ...interface{}) {
	if l.errorFunc != nil {
		l.errorFunc(msg, args...)
	}
}

func (l *testLogger) Warn(msg string, args ...interface{}) {
	if l.warnFunc != nil {
		l.warnFunc(msg, args...)
	}
}

func (l *testLogger) Info(msg string, args ...interface{}) {
	if l.infoFunc != nil {
		l.infoFunc(msg, args...)
	}
}

func (l *testLogger) Debug(msg string, args ...interface{}) {}

func (l *testLogger) WarnRateLimited(key string, interval time.Duration, msg string, args ...interface{}) {
	if l.warnFunc != nil {
		l.warnFunc(msg, args...)
	}
}
