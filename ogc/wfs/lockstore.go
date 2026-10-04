package wfs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// A10/A11: persistent lock store.
//
// Locks were held in a process-global map, invisible to other server
// instances and lost on restart. This file defines the LockStore
// interface and two implementations:
//   - memoryLockStore: the previous in-memory behavior (default).
//   - sqlLockStore: DB-backed leases shared across processes.
//
// A lock lease carries: opaque token, normalized type name, feature IDs,
// owner, acquisition time, and expiry. The token is crypto-random, not
// time-based.

// LockStore persists feature locks.
type LockStore interface {
	// Acquire locks features; returns nil on conflict.
	Acquire(ctx context.Context, typeName string, ids []uint64, owner string, expiry time.Duration) (*FeatureLock, error)
	// Release removes a lock by ID.
	Release(ctx context.Context, id string) error
	// Check verifies that lockID covers (typeName, featureID).
	Check(ctx context.Context, lockID, typeName string, featureID uint64) *Exception
	// IsLocked reports whether any non-expired lock covers the feature.
	IsLocked(ctx context.Context, typeName string, featureID uint64) bool
}

// newLockID generates a crypto-random opaque token.
func newLockID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "lock-" + hex.EncodeToString(b[:])
}

// normalizeTypeName strips namespace prefixes for comparison.
// TODO(A09): replace with namespace catalog lookup.
func normalizeTypeName(tn string) string {
	return stripPrefix(tn)
}

// --- SQL-backed store ---

// SQLLockStoreDDL creates the locks table (migration, not in data tx).
const SQLLockStoreDDL = `
CREATE TABLE IF NOT EXISTS tegola_locks (
	lock_id TEXT PRIMARY KEY,
	type_name TEXT NOT NULL,
	feature_id INTEGER NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	acquired_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tegola_locks_lookup ON tegola_locks(type_name, feature_id);
`

// MySQLLockStoreDDL is the MySQL variant.
const MySQLLockStoreDDL = `
CREATE TABLE IF NOT EXISTS tegola_locks (
	lock_id VARCHAR(64) PRIMARY KEY,
	type_name VARCHAR(255) NOT NULL,
	feature_id BIGINT NOT NULL,
	owner VARCHAR(255) NOT NULL DEFAULT '',
	acquired_at VARCHAR(40) NOT NULL,
	expires_at VARCHAR(40) NOT NULL,
	INDEX idx_tegola_locks_lookup (type_name, feature_id)
);
`

// sqlLockStore is a DB-backed LockStore.
type sqlLockStore struct {
	db      *sql.DB
	dialect string // "sqlite" or "mysql"
}

// NewSQLLockStore creates a DB-backed lock store.
func NewSQLLockStore(db *sql.DB, dialect string) LockStore {
	return &sqlLockStore{db: db, dialect: dialect}
}

func (s *sqlLockStore) Acquire(ctx context.Context, typeName string, ids []uint64, owner string, expiry time.Duration) (*FeatureLock, error) {
	if expiry <= 0 {
		expiry = 5 * time.Minute
	}
	now := time.Now().UTC()
	exp := now.Add(expiry)
	norm := normalizeTypeName(typeName)

	// Clean expired locks and check conflicts in one transaction.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ph := "?"
	if s.dialect == "postgres" {
		ph = "$1" // simplified; full impl uses numbered params
	}
	_ = ph

	// Delete expired.
	_, _ = tx.ExecContext(ctx, `DELETE FROM tegola_locks WHERE expires_at < ?`, now.Format(time.RFC3339Nano))

	// Check conflicts.
	for _, id := range ids {
		var existing string
		err := tx.QueryRowContext(ctx,
			`SELECT lock_id FROM tegola_locks WHERE type_name = ? AND feature_id = ? AND expires_at >= ? LIMIT 1`,
			norm, id, now.Format(time.RFC3339Nano)).Scan(&existing)
		if err == nil {
			return nil, nil // conflict
		}
	}

	lockID := newLockID()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tegola_locks (lock_id, type_name, feature_id, owner, acquired_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			lockID, norm, id, owner, now.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &FeatureLock{
		ID:         lockID,
		TypeName:   typeName,
		FeatureIDs: ids,
		Owner:      owner,
		Acquired:   now,
		Expires:    exp,
	}, nil
}

func (s *sqlLockStore) Release(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tegola_locks WHERE lock_id = ?`, id)
	return err
}

func (s *sqlLockStore) Check(ctx context.Context, lockID, typeName string, featureID uint64) *Exception {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var dbType string
	var dbID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT type_name, feature_id FROM tegola_locks WHERE lock_id = ? AND expires_at >= ?`,
		lockID, now).Scan(&dbType, &dbID)
	if err != nil {
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: fmt.Sprintf("unknown or expired lock %q", lockID)}
	}
	if dbType != normalizeTypeName(typeName) {
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock does not cover this type"}
	}
	if uint64(dbID) != featureID {
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock does not cover this feature"}
	}
	return nil
}

func (s *sqlLockStore) IsLocked(ctx context.Context, typeName string, featureID uint64) bool {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT lock_id FROM tegola_locks WHERE type_name = ? AND feature_id = ? AND expires_at >= ? LIMIT 1`,
		normalizeTypeName(typeName), featureID, now).Scan(&id)
	return err == nil
}

// --- In-memory store (default, single process) ---

type memoryLockStore struct {
	mu    sync.Mutex
	locks map[string]*FeatureLock
}

// NewMemoryLockStore creates the default in-memory lock store.
func NewMemoryLockStore() LockStore {
	return &memoryLockStore{locks: map[string]*FeatureLock{}}
}

func (s *memoryLockStore) Acquire(_ context.Context, typeName string, ids []uint64, owner string, expiry time.Duration) (*FeatureLock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	// Expire old locks.
	for id, l := range s.locks {
		if now.After(l.Expires) {
			delete(s.locks, id)
		}
	}
	// Check conflicts (normalized type names).
	norm := normalizeTypeName(typeName)
	for _, l := range s.locks {
		if normalizeTypeName(l.TypeName) != norm {
			continue
		}
		for _, id := range ids {
			for _, locked := range l.FeatureIDs {
				if id == locked {
					return nil, nil // conflict
				}
			}
		}
	}
	if expiry <= 0 {
		expiry = 5 * time.Minute
	}
	lock := &FeatureLock{
		ID:         newLockID(),
		TypeName:   typeName,
		FeatureIDs: ids,
		Owner:      owner,
		Acquired:   now,
		Expires:    now.Add(expiry),
	}
	s.locks[lock.ID] = lock
	return lock, nil
}

func (s *memoryLockStore) Release(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.locks, id)
	return nil
}

func (s *memoryLockStore) Check(_ context.Context, lockID, typeName string, featureID uint64) *Exception {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[lockID]
	if !ok {
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "unknown lock " + lockID}
	}
	if time.Now().UTC().After(l.Expires) {
		delete(s.locks, lockID)
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock expired"}
	}
	if normalizeTypeName(l.TypeName) != normalizeTypeName(typeName) {
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock does not cover this type"}
	}
	for _, id := range l.FeatureIDs {
		if id == featureID {
			return nil
		}
	}
	return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock does not cover this feature"}
}

func (s *memoryLockStore) IsLocked(_ context.Context, typeName string, featureID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	norm := normalizeTypeName(typeName)
	for _, l := range s.locks {
		if now.After(l.Expires) {
			continue
		}
		if normalizeTypeName(l.TypeName) != norm {
			continue
		}
		for _, id := range l.FeatureIDs {
			if id == featureID {
				return true
			}
		}
	}
	return false
}
