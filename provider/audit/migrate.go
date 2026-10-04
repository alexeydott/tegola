// Package audit migration support (R09).
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CurrentSchemaVersion is the expected service schema version.
const CurrentSchemaVersion = 2

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
	// A01/A38: add incarnation column to existing tables (v1 -> v2).
	// CREATE TABLE IF NOT EXISTS does not alter existing tables.
	if err := addIncarnationColumn(ctx, db, dialect); err != nil {
		return fmt.Errorf("audit: incarnation migration failed: %w", err)
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

// addIncarnationColumn adds the incarnation column if missing (v1->v2 migration).
func addIncarnationColumn(ctx context.Context, db *sql.DB, dialect string) error {
	var alter string
	switch dialect {
	case "mysql":
		alter = `ALTER TABLE tegola_revisions ADD COLUMN IF NOT EXISTS incarnation BIGINT NOT NULL DEFAULT 0`
	case "postgres", "pgx":
		alter = `ALTER TABLE tegola_revisions ADD COLUMN IF NOT EXISTS incarnation BIGINT NOT NULL DEFAULT 0`
	default: // sqlite, gpkg
		// SQLite does not support IF NOT EXISTS for ADD COLUMN; check first.
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM pragma_table_info('tegola_revisions') WHERE name = 'incarnation'`).Scan(&name)
		if err == nil {
			return nil // already exists
		}
		if err != sql.ErrNoRows {
			return err
		}
		alter = `ALTER TABLE tegola_revisions ADD COLUMN incarnation INTEGER NOT NULL DEFAULT 0`
	}
	if _, err := db.ExecContext(ctx, alter); err != nil {
		// Ignore "duplicate column" errors (concurrent migration).
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "Duplicate") {
			return nil
		}
		return err
	}
	return nil
}

// CheckTableEngine verifies MySQL service tables use InnoDB (required for
// transactions). Returns an error if the engine is wrong.
func CheckTableEngine(ctx context.Context, db *sql.DB, dialect string) error {
	if dialect != "mysql" {
		return nil
	}
	for _, tbl := range []string{"tegola_revisions", "tegola_outbox", "tegola_audit_log"} {
		var engine string
		err := db.QueryRowContext(ctx,
			`SELECT ENGINE FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`,
			tbl).Scan(&engine)
		if err != nil {
			if err == sql.ErrNoRows {
				continue // table not created yet, Migrate will create it
			}
			return fmt.Errorf("audit: engine check failed for %s: %w", tbl, err)
		}
		if engine != "InnoDB" {
			return fmt.Errorf("audit: table %s uses engine %s, requires InnoDB for transactions", tbl, engine)
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

