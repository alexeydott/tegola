package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/alexeydott/tegola/provider"
	"github.com/jackc/pgx/v5/pgconn"
)

// RecordTx writes an audit entry and outbox event using a *sql.Tx (MySQL/GPKG).
// Call in the data transaction before Commit.
func RecordTx(ctx context.Context, tx *sql.Tx, collection string, op provider.MutationOp, outcome provider.MutationOutcome, actor, requestID, txID string) error {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	opStr := op.String()
	var revBefore, revAfter string
	switch op {
	case provider.MutationInsert:
		revAfter = outcome.Revision
	case provider.MutationDelete:
		revBefore = outcome.Revision
	default:
		revBefore, revAfter = outcome.Revision, outcome.Revision
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tegola_audit (ts, actor, collection, operation, feature_id, revision_before, revision_after, transaction_id, request_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, actor, collection, opStr, outcome.FeatureID, revBefore, revAfter, txID, requestID,
	); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	eventType := "feature.changed"
	switch op {
	case provider.MutationInsert:
		eventType = "feature.created"
	case provider.MutationReplace:
		eventType = "feature.replaced"
	case provider.MutationUpdate:
		eventType = "feature.updated"
	case provider.MutationDelete:
		eventType = "feature.deleted"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tegola_outbox (ts, event_type, collection, feature_id, payload, dispatched)
		 VALUES (?, ?, ?, ?, ?, 0)`,
		ts, eventType, collection, outcome.FeatureID, "{}",
	); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}

// PgxTx is the minimal pgx.Tx interface for audit writes.
type PgxTx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RecordPgxTx writes audit/outbox using pgx (PostGIS).
func RecordPgxTx(ctx context.Context, tx PgxTx, collection string, op provider.MutationOp, outcome provider.MutationOutcome, actor, requestID, txID string) error {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	opStr := op.String()
	var revBefore, revAfter string
	switch op {
	case provider.MutationInsert:
		revAfter = outcome.Revision
	case provider.MutationDelete:
		revBefore = outcome.Revision
	default:
		revBefore, revAfter = outcome.Revision, outcome.Revision
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tegola_audit (ts, actor, collection, operation, feature_id, revision_before, revision_after, transaction_id, request_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ts, actor, collection, opStr, outcome.FeatureID, revBefore, revAfter, txID, requestID,
	); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	eventType := "feature.changed"
	switch op {
	case provider.MutationInsert:
		eventType = "feature.created"
	case provider.MutationReplace:
		eventType = "feature.replaced"
	case provider.MutationUpdate:
		eventType = "feature.updated"
	case provider.MutationDelete:
		eventType = "feature.deleted"
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tegola_outbox (ts, event_type, collection, feature_id, payload, dispatched)
		 VALUES ($1, $2, $3, $4, $5, FALSE)`,
		ts, eventType, collection, outcome.FeatureID, "{}",
	); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}
