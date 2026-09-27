package simplify

import (
	"math"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/maths"
)

// SimplifyGeometry applies the DouglasPeucker simplification routine to the supplied geometry
func SimplifyGeometry(g tegola.Geometry, tolerance float64) tegola.Geometry {
	switch gg := g.(type) {
	case tegola.Polygon:
		return simplifyPolygon(gg, tolerance)

	case tegola.MultiPolygon:
		var newMP basic.MultiPolygon

		for _, p := range gg.Polygons() {
			sp := simplifyPolygon(p, tolerance)
			if sp == nil {
				continue
			}
			newMP = append(newMP, sp)
		}

		if len(newMP) == 0 {
			return nil
		}

		return newMP

	case tegola.LineString:
		return simplifyLineString(gg, tolerance)

	case tegola.MultiLine:
		var newML basic.MultiLine

		for _, l := range gg.Lines() {
			sl := simplifyLineString(l, tolerance)
			if sl == nil {
				continue
			}
			newML = append(newML, sl)
		}

		if len(newML) == 0 {
			return nil
		}

		return newML
	}

	return g
}

func simplifyLineString(g tegola.LineString, tolerance float64) basic.Line {
	line := basic.CloneLine(g)
	if len(line) <= 4 || maths.DistOfLine(g) < tolerance {
		return line
	}

	pts := line.AsPts()
	pts = DouglasPeucker(pts, tolerance)
	if len(pts) == 0 {
		return nil
	}

	// No coordinate truncation here: the MVT encoder quantizes to its output
	// grid anyway, and pre-truncating shifted simplified vertices off the
	// input subsequence (debt 3.6 corpus cases line/*-truncation).
	return basic.NewLineFromPt(pts...)
}

func simplifyPolygon(g tegola.Polygon, tolerance float64) basic.Polygon {
	lines := g.Sublines()
	if len(lines) <= 0 {
		return nil
	}

	var poly basic.Polygon
	sqTolerance := tolerance * tolerance
	// First lets look the first line, then we will simplify the other lines.
	for i := range lines {
		area := maths.AreaOfPolygonLineString(lines[i])
		l := basic.CloneLine(lines[i])

		if area < sqTolerance {
			if i == 0 {
				return basic.ClonePolygon(g)
			}
			// don't simplify the internal line
			poly = append(poly, l)
			continue
		}

		pts := l.AsPts()
		if len(pts) <= 2 {
			if i == 0 {
				return nil
			}
			continue
		}

		normalized := normalizePoints(pts)
		pts = normalized
		if len(pts) <= 4 {
			if i == 0 {
				return basic.ClonePolygon(g)
			}
			poly = append(poly, l)
			continue
		}

		pts = DouglasPeucker(pts, tolerance)
		if len(pts) <= 2 {
			if i == 0 {
				return nil
			}
			//log.Println("\t Skipping polygon subline.")
			continue
		}

		// Ring topology backstop: DouglasPeucker validates the open chain, but
		// a ring also has the closing edge from last point back to first. When
		// the closed ring self-intersects and the pre-simplify ring did not
		// (or validation is inconclusive), fall back to the normalized ring,
		// which preserves the input geometry exactly.
		if intersects, complete := selfIntersects(pts, true); !complete || intersects {
			if inIntersects, inComplete := selfIntersects(normalized, true); !inComplete || !inIntersects {
				pts = normalized
			}
		}

		poly = append(poly, basic.NewLineFromPt(pts...))
	}

	if len(poly) == 0 {
		return nil
	}

	return poly
}

// normalizePoints removes redundant vertices from the ring chain pts without
// changing the geometry: a vertex is dropped only when it lies on the segment
// between the neighbours kept around it. (The previous rule dropped points
// collinear with pts[0] and their successor regardless of position, which
// removed spike apexes that are not between their neighbours and folded the
// ring edge across the spike.)
func normalizePoints(pts []maths.Pt) (pnts []maths.Pt) {
	if pts[0] == pts[len(pts)-1] {
		pts = pts[1:]
	}

	if len(pts) <= 4 {
		return pts
	}

	lpt := 0
	pnts = append(pnts, pts[0])

	for i := 1; i < len(pts); i++ {
		ni := i + 1
		if ni >= len(pts) {
			ni = 0
		}
		if collinearBetween(pts[lpt], pts[i], pts[ni]) {
			continue // drop: the path pts[lpt]-pts[i]-pts[ni] keeps its geometry
		}
		pnts = append(pnts, pts[i])
		lpt = i
	}

	return pnts
}

// collinearBetween reports whether b lies on the segment from a to c
// (endpoints included), so dropping b leaves the path a-c unchanged.
func collinearBetween(a, b, c maths.Pt) bool {
	ab := sub2(b, a)
	bc := sub2(c, b)
	if !nearZero(cross2(ab, bc), math.Sqrt(dot2(ab, ab)*dot2(bc, bc))) {
		return false
	}
	return dot2(ab, bc) >= 0
}
