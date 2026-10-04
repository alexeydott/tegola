// Package audit: revision tracking for A03 optimistic concurrency.
//
// A03 requires the If-Match revision check to happen INSIDE the native
// data transaction, not as a separate HTTP-layer read before it. Without
// this, two clients can both pass the precondition check and overwrite
// each other (TOCTOU race).
//
// Design: a tegola_revisions sidecar table maps (collection, feature_id)
// to a monotonic revision counter. It is updated in the SAME transaction
// as the data change:
//
//   1. checkRevision: SELECT the current revision (locked by the tx).
//      If IfRevision != "" and != current -> PreconditionFailed (412).
//   2. After the data mutation succeeds: bump the revision.
//
// The table must be created by migration before write traffic (A01: no
// DDL inside the data transaction). If the table is missing, revision
// checks fail with a clear error instead of silently passing.
//
// Revision 0 means "no revision recorded yet" (row never written through
// this path). The first guarded write initializes it.
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/alexeydott/tegola/provider"
)

// RevisionDDL statements for migration (NOT executed in data transactions).
const (
	RevisionSQLiteDDL = `CREATE TABLE IF NOT EXISTS tegola_revisions (
	collection TEXT NOT NULL,
	feature_id INTEGER NOT NULL,
	revision INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (collection, feature_id)
);`
	RevisionMySQLDDL = `CREATE TABLE IF NOT EXISTS tegola_revisions (
	collection VARCHAR(255) NOT NULL,
	feature_id BIGINT NOT NULL,
	revision BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (collection, feature_id)
);`
	RevisionPostgresDDL = `CREATE TABLE IF NOT EXISTS tegola_revisions (
	collection TEXT NOT NULL,
	feature_id BIGINT NOT NULL,
	revision BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (collection, feature_id)
);`
)

// CheckAndBumpRevisionSQL implements the A03 check inside a *sql.Tx
// (SQLite/MySQL). It SELECTs the current revision, compares against
// want (the IfRevision precondition; "" skips the check), and bumps.
//
// Returns the new revision, or a *provider.MutationError.
func CheckAndBumpRevisionSQL(ctx context.Context, tx *sql.Tx, collection string, featureID uint64, want string, dialect string) (int64, error) {
	var cur int64
	var found bool
	// Lock the revision row within this transaction.
	// MySQL: SELECT ... FOR UPDATE. SQLite: the tx itself serializes.
	q := `SELECT revision FROM tegola_revisions WHERE collection = ? AND feature_id = ?`
	if dialect == "mysql" {
		q += ` FOR UPDATE`
	}
	err := tx.QueryRowContext(ctx, q, collection, featureID).Scan(&cur)
	switch {
	case err == nil:
		found = true
	case err == sql.ErrNoRows:
		found = false
	default:
		// Table missing: revision tracking not migrated. If no precondition
		// was requested, skip silently (backward compat). If IfRevision was
		// given, we cannot enforce it -> fail explicitly (A03).
		if IsMissingTable(err) {
			if want != "" {
				return 0, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: "revision precondition requires tegola_revisions table (run migration)"}
			}
			// R01: table not migrated -> no revision tracking. Return -1
			// so callers emit "" (hash ETag fallback), not "0".
			return -1, nil
		}
		return 0, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("revision read: %v", err)}
	}
	if want != "" {
		// want is the If-Match value; "0" means "no revision yet".
		var wantNum int64
		if _, err := fmt.Sscanf(want, "%d", &wantNum); err != nil {
			return 0, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("invalid If-Revision %q", want)}
		}
		var curNum int64
		if found {
			curNum = cur
		}
		if wantNum != curNum {
			return 0, &provider.MutationError{Kind: provider.MutationErrPreconditionFailed, Reason: fmt.Sprintf("revision mismatch: have %d, want %d", curNum, wantNum)}
		}
	}
	newRev := cur + 1
	if !found {
		newRev = 1
	}
	if dialect == "mysql" {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO tegola_revisions (collection, feature_id, revision) VALUES (?, ?, ?)
			 ON DUPLICATE KEY UPDATE revision = ?`, collection, featureID, newRev, newRev)
	} else {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO tegola_revisions (collection, feature_id, revision) VALUES (?, ?, ?)
			 ON CONFLICT(collection, feature_id) DO UPDATE SET revision = excluded.revision`,
			collection, featureID, newRev)
	}
	if err != nil {
		if IsMissingTable(err) {
			// No revision table: skip bump, return 0 (no revision).
			return 0, nil
		}
		return 0, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("revision bump: %v", err)}
	}
	return newRev, nil
}

// isMissingTable reports whether err is a "no such table" error.
// IsMissingTable reports whether err indicates a missing revisions table.
func IsMissingTable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist")
}
