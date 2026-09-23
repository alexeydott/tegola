package simplify

import (
	"github.com/go-spatial/tegola/maths"
)

// DouglasPeucker is a geometry simplifcation routine
// https://en.wikipedia.org/wiki/Ramer%E2%80%93Douglas%E2%80%93Peucker_algorithm
//
// tolerance is a distance in the same units as the point coordinates.
// Callers that hold a squared tolerance must take its square root before
// calling; this function uses the tolerance as-is and passes it unchanged
// through the recursion (previously it squared the tolerance on entry and
// passed the squared value back into recursive calls, so the effective
// threshold collapsed to tolerance⁴/⁸/… and near-degenerate geometries were
// "simplified" into self-intersecting rings).
func DouglasPeucker(points []maths.Pt, tolerance float64) []maths.Pt {
	if tolerance <= 0 || len(points) <= 2 {
		return points
	}

	// find the maximum distance from the end points.
	// NOTE: the loop must cover every intermediate point (i < len-1); using
	// len-2 silently skipped the second-to-last point, so any 3-point slice
	// was collapsed to its endpoints no matter how far the middle point sat
	// from the chord (this produced self-intersecting "bowtie" rings).
	l := maths.Line{points[0], points[len(points)-1]}
	dmax := 0.0
	idx := 0
	for i := 1; i < len(points)-1; i++ {
		d := l.DistanceFromPoint(points[i])
		if d > dmax {
			dmax = d
			idx = i
		}
	}

	if dmax > tolerance {
		rec1 := DouglasPeucker(points[:idx+1], tolerance)
		rec2 := DouglasPeucker(points[idx:], tolerance)

		// The split point belongs to both recursive ranges; retain it only
		// once when joining the simplified segments.
		newpts := make([]maths.Pt, 0, len(rec1)+len(rec2)-1)
		newpts = append(newpts, rec1[:len(rec1)-1]...)
		newpts = append(newpts, rec2...)

		return newpts
	}

	return []maths.Pt{points[0], points[len(points)-1]}
}
