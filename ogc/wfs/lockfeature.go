package wfs

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// LockFeature implements WFS 1.1 feature locking (WFS 2.0 removed it).
//
// Locks are held in memory with an expiry. A Transaction that mutates a
// locked feature must present the lock ID; otherwise the mutation is
// rejected. This is the reference profile; a production deployment with
// multiple server instances needs a shared lock store.

// FeatureLock is one held lock.
type FeatureLock struct {
	ID         string
	TypeName   string
	FeatureIDs []uint64
	Expires    time.Time
}

var (
	lockMu     sync.Mutex
	locks      = map[string]*FeatureLock{}
	lockExpiry = 5 * time.Minute // default WFS 1.1 expiry
)

// AcquireLock locks features and returns the lock ID.
func AcquireLock(typeName string, ids []uint64, expiry time.Duration) *FeatureLock {
	lockMu.Lock()
	defer lockMu.Unlock()
	// Expire old locks.
	now := time.Now()
	for id, l := range locks {
		if now.After(l.Expires) {
			delete(locks, id)
		}
	}
	// Check for conflicts.
	for _, l := range locks {
		if l.TypeName != typeName {
			continue
		}
		for _, id := range ids {
			for _, locked := range l.FeatureIDs {
				if id == locked {
					return nil // conflict
				}
			}
		}
	}
	if expiry <= 0 {
		expiry = lockExpiry
	}
	lock := &FeatureLock{
		ID:         fmt.Sprintf("lock-%d", now.UnixNano()),
		TypeName:   typeName,
		FeatureIDs: ids,
		Expires:    now.Add(expiry),
	}
	locks[lock.ID] = lock
	return lock
}

// ReleaseLock releases a lock by ID.
func ReleaseLock(id string) bool {
	lockMu.Lock()
	defer lockMu.Unlock()
	if _, ok := locks[id]; ok {
		delete(locks, id)
		return true
	}
	return false
}

// CheckLock verifies that a lock ID covers the given feature.
// Returns nil if the lock is valid for the feature.
func CheckLock(lockID, typeName string, featureID uint64) *Exception {
	lockMu.Lock()
	defer lockMu.Unlock()
	l, ok := locks[lockID]
	if !ok {
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: fmt.Sprintf("unknown lock %q", lockID)}
	}
	if time.Now().After(l.Expires) {
		delete(locks, lockID)
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock expired"}
	}
	if l.TypeName != typeName {
		return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock does not cover this type"}
	}
	for _, id := range l.FeatureIDs {
		if id == featureID {
			return nil
		}
	}
	return &Exception{Code: ExceptionInvalidParameterValue, Locator: "lockId", Text: "lock does not cover this feature"}
}

// IsLocked reports whether a feature is currently locked (by any lock).
func IsLocked(typeName string, featureID uint64) bool {
	lockMu.Lock()
	defer lockMu.Unlock()
	now := time.Now()
	for _, l := range locks {
		if now.After(l.Expires) {
			continue
		}
		if l.TypeName != typeName {
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

// LockFeatureResponse renders the WFS 1.1 LockFeature response.
func LockFeatureResponse(lock *FeatureLock) string {
	var sb strings.Builder
	sb.WriteString(`<wfs:LockFeatureResponse xmlns:wfs="http://www.opengis.net/wfs">`)
	sb.WriteString(fmt.Sprintf(`<wfs:LockId>%s</wfs:LockId>`, xmlEscape(lock.ID)))
	sb.WriteString(`</wfs:LockFeatureResponse>`)
	return sb.String()
}
