package simplify

import (
	"math"
	"testing"

	"github.com/go-spatial/tegola/maths"
)

// TestDouglasPeuckerKeepsDistantIntermediatePoints guards against a
// regression where the point scan loop skipped the second-to-last point
// (i < len-2), so any 3-point slice was collapsed to its endpoints no
// matter how far the middle point sat from the chord.
func TestDouglasPeuckerKeepsDistantIntermediatePoints(t *testing.T) {
	testcases := []struct {
		name      string
		points    []maths.Pt
		tolerance float64
		// points that must survive simplification, besides first/last
		mustKeep []maths.Pt
	}{
		{
			name: "three points, far off middle kept",
			points: []maths.Pt{
				{0, 0}, {5, 100}, {10, 0},
			},
			tolerance: 2.0,
			mustKeep:  []maths.Pt{{5, 100}},
		},
		{
			name: "three points, far off second-to-last kept",
			points: []maths.Pt{
				{0, 0}, {100, 1}, {10, 1},
			},
			tolerance: 2.0,
			mustKeep:  []maths.Pt{{100, 1}},
		},
		{
			name: "ring without closing duplicate keeps all corners",
			// DP is always called on open point lists; the closing
			// duplicate is stripped upstream by normalizePoints.
			points: []maths.Pt{
				{0, 0}, {10, 0}, {10, 10}, {0, 10}, {5, 5},
			},
			tolerance: 2.0,
			mustKeep:  []maths.Pt{{10, 0}, {10, 10}, {0, 10}},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			got := DouglasPeucker(tc.points, tc.tolerance)
			first := tc.points[0]
			last := tc.points[len(tc.points)-1]
			if len(got) < 2 || got[0] != first || got[len(got)-1] != last {
				t.Fatalf("endpoints not preserved: got %v, want first %v last %v", got, first, last)
			}
			for _, want := range tc.mustKeep {
				found := false
				for _, p := range got {
					if math.Abs(p.X-want.X) < 1e-9 && math.Abs(p.Y-want.Y) < 1e-9 {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected point %v was simplified away: got %v", want, got)
				}
			}
		})
	}
}

// TestDouglasPeuckerCollapsesFlat verifies that points within tolerance of
// the chord are still removed.
func TestDouglasPeuckerCollapsesFlat(t *testing.T) {
	pts := []maths.Pt{
		{0, 0}, {5, 0.5}, {10, 0},
	}
	got := DouglasPeucker(pts, 2.0)
	if len(got) != 2 {
		t.Fatalf("expected flat middle point removed, got %v", got)
	}
}
