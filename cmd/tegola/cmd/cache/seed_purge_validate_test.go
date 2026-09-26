package cache

import (
	"strings"
	"testing"
)

// withSeedPurgeFlags points the package-level seed/purge flag variables at the
// given values for the duration of a test.
func withSeedPurgeFlags(t *testing.T, concurrency int, bounds string, boundsSRID int, minZ, maxZ uint) {
	t.Helper()
	oldConcurrency := cacheConcurrency
	oldBounds := cacheBounds
	oldBoundsSRID := cacheBoundsSRID
	oldMinZoom := minZoom
	oldMaxZoom := maxZoom
	cacheConcurrency = concurrency
	cacheBounds = bounds
	cacheBoundsSRID = boundsSRID
	minZoom = minZ
	maxZoom = maxZ
	t.Cleanup(func() {
		cacheConcurrency = oldConcurrency
		cacheBounds = oldBounds
		cacheBoundsSRID = oldBoundsSRID
		minZoom = oldMinZoom
		maxZoom = oldMaxZoom
	})
}

// TestSeedPurgeCmdValidateConcurrency ensures --concurrency is rejected when
// below 1: 0 would leave the seeder with no workers (it hangs on wg.Wait) and
// negative values panic (part13 P6-24).
func TestSeedPurgeCmdValidateConcurrency(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		expectErr   bool
	}{
		{name: "zero hangs", concurrency: 0, expectErr: true},
		{name: "negative panics", concurrency: -1, expectErr: true},
		{name: "minimum valid", concurrency: 1, expectErr: false},
		{name: "many workers", concurrency: 16, expectErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withSeedPurgeFlags(t, tc.concurrency, "-180,-85.0511,180,85.0511", 4326, 0, 2)
			err := seedPurgeCmdValidate(nil, nil)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error for concurrency %d, got nil", tc.concurrency)
				}
				if !strings.Contains(err.Error(), "concurrency") {
					t.Errorf("expected error to mention concurrency, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error for concurrency %d, got: %v", tc.concurrency, err)
			}
		})
	}
}
