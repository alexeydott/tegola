// Package audit migration support (R09).
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CurrentSchemaVersion is the expected service schema version.
const CurrentSchemaVersion = 1

// Migrate ensures the service schema (audit, outbox, revisions, version)
// exists at CurrentSchemaVersion. It uses the dialect-specific DDL.
// This must be called at startup before write admission, not inside
// data transactions.
func Migrate(ctx context.Context, db *sql.DB, dialect string) error {
	var ddl string
	switch dialect {
	case "sqlite", "gpkg":
		ddl = SQLiteDDL
	case "mysql":
		ddl = MySQLDDL
	case "postgres", "pgx":
		ddl = PostgresDDL
	default:
		return fmt.Errorf("audit: unknown dialect %q for migration", dialect)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("audit: migration DDL failed: %w", err)
	}
	// Record version.
	now := time.Now().UTC().Format(time.RFC3339)
	switch dialect {
	case "mysql":
		_, err := db.ExecContext(ctx,
			`INSERT INTO tegola_schema_version (version, applied_at) VALUES (?, ?)
			 ON DUPLICATE KEY UPDATE applied_at = VALUES(applied_at)`,
			CurrentSchemaVersion, now)
		if err != nil {
			return fmt.Errorf("audit: version record failed: %w", err)
		}
	case "postgres", "pgx":
		_, err := db.ExecContext(ctx,
			`INSERT INTO tegola_schema_version (version, applied_at) VALUES ($1, $2)
			 ON CONFLICT (version) DO UPDATE SET applied_at = EXCLUDED.applied_at`,
			CurrentSchemaVersion, now)
		if err != nil {
			return fmt.Errorf("audit: version record failed: %w", err)
		}
	default:
		_, err := db.ExecContext(ctx,
			`INSERT OR REPLACE INTO tegola_schema_version (version, applied_at) VALUES (?, ?)`,
			CurrentSchemaVersion, now)
		if err != nil {
			return fmt.Errorf("audit: version record failed: %w", err)
		}
	}
	return nil
}

// CheckSchemaVersion verifies the service schema is at CurrentSchemaVersion.
// Returns an error if the schema is missing or outdated. Call before write
// admission; do not probe inside a PostgreSQL data transaction.
func CheckSchemaVersion(ctx context.Context, db *sql.DB, dialect string) error {
	var v int
	var q string
	switch dialect {
	case "postgres", "pgx":
		q = `SELECT version FROM tegola_schema_version WHERE version = $1`
	default:
		q = `SELECT version FROM tegola_schema_version WHERE version = ?`
	}
	err := db.QueryRowContext(ctx, q, CurrentSchemaVersion).Scan(&v)
	if err != nil {
		if IsMissingTable(err) || err == sql.ErrNoRows {
			return fmt.Errorf("audit: service schema not migrated (run Migrate): %w", err)
		}
		return fmt.Errorf("audit: schema version check failed: %w", err)
	}
	return nil
}

