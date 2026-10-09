package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"printmaster/common/updatepolicy"
)

// Release artifact format migration.
//
// Before format identity existed, release_artifacts and release_manifests were
// unique on (component, version, platform, arch). A release's raw binary, MSI,
// .deb, and .rpm for one platform/arch therefore overwrote each other, and the
// surviving row could be any of them. The migration has two parts:
//
//  1. Schema: give both tables a format column and a five-column identity.
//     PostgreSQL does this in its idempotent schema block; SQLite cannot drop an
//     inline UNIQUE constraint, so migrateSQLiteReleaseFormatIdentity rebuilds
//     the tables once.
//  2. Data: reclassifyLegacyReleaseFormats labels each legacy row by the file it
//     actually cached, using the same classifier as release intake. Manifests
//     for reclassified rows are removed so intake re-signs them with the correct
//     format instead of serving a binary manifest for an installer payload.

// sqliteReleaseTableSpec describes one release table for the SQLite rebuild.
type sqliteReleaseTableSpec struct {
	name        string
	createSQL   string // CREATE TABLE for the "<name>_format_new" staging table
	copyColumns string // columns copied verbatim from the legacy table
	indexSQL    string // indexes dropped with the legacy table and recreated
}

var sqliteReleaseTableSpecs = []sqliteReleaseTableSpec{
	{
		name: "release_artifacts",
		createSQL: `CREATE TABLE release_artifacts_format_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			component TEXT NOT NULL,
			version TEXT NOT NULL,
			platform TEXT NOT NULL,
			arch TEXT NOT NULL,
			format TEXT NOT NULL DEFAULT 'binary',
			channel TEXT NOT NULL DEFAULT 'stable',
			source_url TEXT NOT NULL,
			cache_path TEXT,
			sha256 TEXT,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			release_notes TEXT,
			published_at DATETIME,
			downloaded_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(component, version, platform, arch, format)
		)`,
		copyColumns: `id, component, version, platform, arch, channel, source_url, cache_path, sha256,
			size_bytes, release_notes, published_at, downloaded_at, created_at, updated_at`,
		indexSQL: `CREATE INDEX IF NOT EXISTS idx_release_artifacts_component ON release_artifacts(component)`,
	},
	{
		name: "release_manifests",
		createSQL: `CREATE TABLE release_manifests_format_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			component TEXT NOT NULL,
			version TEXT NOT NULL,
			platform TEXT NOT NULL,
			arch TEXT NOT NULL,
			format TEXT NOT NULL DEFAULT 'binary',
			channel TEXT NOT NULL DEFAULT 'stable',
			manifest_version TEXT NOT NULL,
			manifest_json TEXT NOT NULL,
			signature TEXT NOT NULL,
			signing_key_id TEXT NOT NULL,
			generated_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(component, version, platform, arch, format)
		)`,
		copyColumns: `id, component, version, platform, arch, channel, manifest_version, manifest_json,
			signature, signing_key_id, generated_at, created_at, updated_at`,
		indexSQL: `CREATE INDEX IF NOT EXISTS idx_release_manifests_component ON release_manifests(component)`,
	},
}

// migrateSQLiteReleaseFormatIdentity rebuilds legacy SQLite release tables
// that lack the format column. New databases already have the final shape and
// are skipped. Each table is rebuilt in one transaction with foreign keys
// disabled (SQLite's documented procedure) so self_update_runs references
// survive the rename.
func (s *SQLiteStore) migrateSQLiteReleaseFormatIdentity() error {
	pending := make([]sqliteReleaseTableSpec, 0, len(sqliteReleaseTableSpecs))
	for _, spec := range sqliteReleaseTableSpecs {
		var hasFormat int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info(?) WHERE name = 'format'`, spec.name).Scan(&hasFormat); err != nil {
			return fmt.Errorf("inspect %s columns: %w", spec.name, err)
		}
		if hasFormat == 0 {
			pending = append(pending, spec)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	if _, err := s.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys for release format migration: %w", err)
	}
	defer s.db.Exec("PRAGMA foreign_keys=ON")

	for _, spec := range pending {
		if err := s.rebuildSQLiteReleaseTable(spec); err != nil {
			return err
		}
		logInfo("Release table migrated to format identity", "table", spec.name)
	}
	return nil
}

func (s *SQLiteStore) rebuildSQLiteReleaseTable(spec sqliteReleaseTableSpec) error {
	staging := spec.name + "_format_new"
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin %s format migration: %w", spec.name, err)
	}
	defer tx.Rollback()

	statements := []string{
		"DROP TABLE IF EXISTS " + staging,
		spec.createSQL,
		// Every legacy row starts as binary; reclassifyLegacyReleaseFormats then
		// corrects rows whose cached file is an installer or OS package.
		fmt.Sprintf("INSERT INTO %s (%s, format) SELECT %s, 'binary' FROM %s", staging, spec.copyColumns, spec.copyColumns, spec.name),
		"DROP TABLE " + spec.name,
		fmt.Sprintf("ALTER TABLE %s RENAME TO %s", staging, spec.name),
		spec.indexSQL,
	}
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrate %s to format identity: %w", spec.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s format migration: %w", spec.name, err)
	}
	return nil
}

// reclassifyLegacyReleaseFormats corrects the format of artifacts cached before
// format identity existed, when every row was implicitly binary. It is
// idempotent: rows written by format-aware intake already carry the format
// their file name implies, so later runs change nothing.
func (s *BaseStore) reclassifyLegacyReleaseFormats(ctx context.Context) error {
	rows, err := s.queryContext(ctx, `
		SELECT id, component, version, platform, arch, source_url, cache_path
		FROM release_artifacts
		WHERE format = 'binary'
	`)
	if err != nil {
		return fmt.Errorf("list release artifacts for format reclassification: %w", err)
	}

	type legacyArtifact struct {
		id                                 int64
		component, version, platform, arch string
		format                             updatepolicy.ArtifactFormat
	}
	var corrections []legacyArtifact
	for rows.Next() {
		var (
			item      legacyArtifact
			sourceURL string
			cachePath sql.NullString
		)
		if err := rows.Scan(&item.id, &item.component, &item.version, &item.platform, &item.arch, &sourceURL, &cachePath); err != nil {
			rows.Close()
			return fmt.Errorf("scan release artifact for format reclassification: %w", err)
		}
		// The cached file is what Agents would actually receive, so prefer it
		// over the source URL when both exist.
		name := sourceURL
		if cachePath.Valid && cachePath.String != "" {
			name = filepath.Base(cachePath.String)
		}
		item.format = updatepolicy.ArtifactFormatFromFilename(name)
		if item.format != updatepolicy.ArtifactFormatBinary {
			corrections = append(corrections, item)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range corrections {
		// The binary-format manifest for this tuple was signed over the installer
		// payload. Drop it; intake re-signs a correct manifest for each format.
		if _, err := s.execContext(ctx, `
			DELETE FROM release_manifests
			WHERE component = ? AND version = ? AND platform = ? AND arch = ? AND format = 'binary'
		`, item.component, item.version, item.platform, item.arch); err != nil {
			return fmt.Errorf("remove misclassified release manifest: %w", err)
		}
		if _, err := s.execContext(ctx, `UPDATE release_artifacts SET format = ? WHERE id = ?`, item.format.String(), item.id); err != nil {
			// A correctly classified row for this format already exists, so the
			// legacy row is a stale duplicate and can be discarded.
			logWarn("Release artifact format reclassification conflicted; removing legacy duplicate",
				"component", item.component, "version", item.version, "platform", item.platform,
				"arch", item.arch, "format", item.format, "error", err)
			if _, derr := s.execContext(ctx, `DELETE FROM release_artifacts WHERE id = ?`, item.id); derr != nil {
				return fmt.Errorf("remove duplicate legacy release artifact: %w", derr)
			}
			continue
		}
		logInfo("Release artifact reclassified by cached file type",
			"component", item.component, "version", item.version, "platform", item.platform,
			"arch", item.arch, "format", item.format)
	}
	return nil
}
