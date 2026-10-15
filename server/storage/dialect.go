package storage

import (
	"fmt"
	"strings"
)

// currentSchemaVersion is the single source of truth for the schema_version
// bookkeeping row written by both the SQLite and PostgreSQL stores. It doesn't
// gate any migration logic (all migrations run unconditionally and are
// idempotent) - it's purely informational. Bump it here only; both dialects
// read from this constant so they can't drift apart from each other.
const currentSchemaVersion = 11

// Dialect abstracts database-specific SQL syntax differences.
// This allows the same business logic to work across SQLite and PostgreSQL.
type Dialect interface {
	// Name returns the dialect name (e.g., "sqlite", "postgres")
	Name() string

	// Placeholder returns a parameter placeholder for the given 1-based index.
	// SQLite uses ?, PostgreSQL uses $1, $2, etc.
	Placeholder(index int) string

	// AutoIncrement returns the column type for auto-incrementing primary keys.
	// SQLite: "INTEGER PRIMARY KEY AUTOINCREMENT"
	// PostgreSQL: "SERIAL PRIMARY KEY" or "BIGSERIAL PRIMARY KEY"
	AutoIncrement(big bool) string

	// TimestampType returns the column type for timestamps.
	// SQLite: "DATETIME"
	// PostgreSQL: "TIMESTAMPTZ" (with timezone)
	TimestampType() string

	// BoolType returns the column type for boolean values.
	// SQLite: "INTEGER" (0/1)
	// PostgreSQL: "BOOLEAN"
	BoolType() string

	// CurrentTimestamp returns the SQL expression for current timestamp.
	// SQLite: "CURRENT_TIMESTAMP"
	// PostgreSQL: "NOW()" or "CURRENT_TIMESTAMP"
	CurrentTimestamp() string

	// Upsert returns the upsert clause for the database.
	// SQLite: "ON CONFLICT (key) DO UPDATE SET ..."
	// PostgreSQL: "ON CONFLICT (key) DO UPDATE SET ..."
	UpsertConflict(conflictColumns []string) string

	// ReturningClause returns "RETURNING id" for databases that support it.
	// SQLite (3.35+) and PostgreSQL both support RETURNING.
	ReturningClause(columns ...string) string

	// LimitOffset returns the LIMIT/OFFSET clause.
	// Both SQLite and PostgreSQL use the same syntax.
	LimitOffset(limit, offset int) string

	// ILike returns case-insensitive LIKE syntax.
	// SQLite: "LOWER(col) LIKE LOWER(?)" (no native ILIKE)
	// PostgreSQL: "col ILIKE $1"
	ILike(column string, placeholderIndex int) string

	// NullSafeEqual returns null-safe equality check.
	// SQLite: "col IS ?" or "(col = ? OR (col IS NULL AND ? IS NULL))"
	// PostgreSQL: "col IS NOT DISTINCT FROM $1"
	NullSafeEqual(column string, placeholderIndex int) string

	// JSONExtract returns the syntax for extracting a JSON field.
	// SQLite: json_extract(col, '$.key')
	// PostgreSQL: col->>'key' or col->'key'
	JSONExtract(column, key string) string

	// ForUpdate returns the locking clause for SELECT ... FOR UPDATE.
	// SQLite: "" (not supported in the same way)
	// PostgreSQL: "FOR UPDATE"
	ForUpdate() string

	// TextType returns the TEXT column type (same for both).
	TextType() string

	// IntegerType returns the appropriate integer type.
	IntegerType(big bool) string

	// BoolValue returns the SQL literal for a boolean value.
	// SQLite: "0" or "1"
	// PostgreSQL: "FALSE" or "TRUE"
	BoolValue(b bool) string
}

// SQLiteDialect implements Dialect for SQLite.
type SQLiteDialect struct{}

var _ Dialect = (*SQLiteDialect)(nil)

func (d *SQLiteDialect) Name() string { return "sqlite" }

func (d *SQLiteDialect) Placeholder(index int) string {
	return "?"
}

func (d *SQLiteDialect) AutoIncrement(big bool) string {
	return "INTEGER PRIMARY KEY AUTOINCREMENT"
}

func (d *SQLiteDialect) TimestampType() string {
	return "DATETIME"
}

func (d *SQLiteDialect) BoolType() string {
	return "INTEGER"
}

func (d *SQLiteDialect) CurrentTimestamp() string {
	return "CURRENT_TIMESTAMP"
}

func (d *SQLiteDialect) UpsertConflict(conflictColumns []string) string {
	return fmt.Sprintf("ON CONFLICT(%s) DO UPDATE SET", strings.Join(conflictColumns, ", "))
}

func (d *SQLiteDialect) ReturningClause(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	return "RETURNING " + strings.Join(columns, ", ")
}

func (d *SQLiteDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 && offset <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

func (d *SQLiteDialect) ILike(column string, placeholderIndex int) string {
	// SQLite doesn't have ILIKE, use LOWER() for case-insensitive matching
	return fmt.Sprintf("LOWER(%s) LIKE LOWER(?)", column)
}

func (d *SQLiteDialect) NullSafeEqual(column string, placeholderIndex int) string {
	return fmt.Sprintf("%s IS ?", column)
}

func (d *SQLiteDialect) JSONExtract(column, key string) string {
	return fmt.Sprintf("json_extract(%s, '$.%s')", column, key)
}

func (d *SQLiteDialect) ForUpdate() string {
	return "" // SQLite doesn't support FOR UPDATE in the same way
}

func (d *SQLiteDialect) TextType() string {
	return "TEXT"
}

func (d *SQLiteDialect) IntegerType(big bool) string {
	return "INTEGER"
}

func (d *SQLiteDialect) BoolValue(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// PostgresDialect implements Dialect for PostgreSQL.
type PostgresDialect struct{}

var _ Dialect = (*PostgresDialect)(nil)

func (d *PostgresDialect) Name() string { return "postgres" }

func (d *PostgresDialect) Placeholder(index int) string {
	return fmt.Sprintf("$%d", index)
}

func (d *PostgresDialect) AutoIncrement(big bool) string {
	if big {
		return "BIGSERIAL PRIMARY KEY"
	}
	return "SERIAL PRIMARY KEY"
}

func (d *PostgresDialect) TimestampType() string {
	return "TIMESTAMPTZ"
}

func (d *PostgresDialect) BoolType() string {
	return "BOOLEAN"
}

func (d *PostgresDialect) CurrentTimestamp() string {
	return "NOW()"
}

func (d *PostgresDialect) UpsertConflict(conflictColumns []string) string {
	return fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET", strings.Join(conflictColumns, ", "))
}

func (d *PostgresDialect) ReturningClause(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	return "RETURNING " + strings.Join(columns, ", ")
}

func (d *PostgresDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 && offset <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

func (d *PostgresDialect) ILike(column string, placeholderIndex int) string {
	return fmt.Sprintf("%s ILIKE $%d", column, placeholderIndex)
}

func (d *PostgresDialect) NullSafeEqual(column string, placeholderIndex int) string {
	return fmt.Sprintf("%s IS NOT DISTINCT FROM $%d", column, placeholderIndex)
}

func (d *PostgresDialect) JSONExtract(column, key string) string {
	return fmt.Sprintf("%s->>'%s'", column, key)
}

func (d *PostgresDialect) ForUpdate() string {
	return "FOR UPDATE"
}

func (d *PostgresDialect) TextType() string {
	return "TEXT"
}

func (d *PostgresDialect) IntegerType(big bool) string {
	if big {
		return "BIGINT"
	}
	return "INTEGER"
}

func (d *PostgresDialect) BoolValue(b bool) string {
	if b {
		return "TRUE"
	}
	return "FALSE"
}

// ConvertPlaceholders converts SQLite-style ? placeholders to PostgreSQL-style $n placeholders.
// This is useful when reusing queries written for SQLite.
func ConvertPlaceholders(query string) string {
	var result strings.Builder
	result.Grow(len(query) + 10)
	n := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			result.WriteString(fmt.Sprintf("$%d", n))
			n++
		} else {
			result.WriteByte(query[i])
		}
	}
	return result.String()
}

// PlaceholderSet generates a comma-separated list of placeholders for IN clauses.
// For SQLite: "?, ?, ?"
// For PostgreSQL: "$1, $2, $3"
func PlaceholderSet(dialect Dialect, count int, startIndex int) string {
	if count <= 0 {
		return ""
	}
	placeholders := make([]string, count)
	for i := 0; i < count; i++ {
		placeholders[i] = dialect.Placeholder(startIndex + i)
	}
	return strings.Join(placeholders, ", ")
}
