package hana

import (
	"math"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/slippy"
)

// extentStubTile is a provider.Tile with a configurable extent for the
// audit P6-20 error-propagation tests.
type extentStubTile struct {
	extent *geom.Extent
	srid   uint64
}

func (t extentStubTile) ZXY() (slippy.Zoom, uint, uint)         { return 14, 8, 5 }
func (t extentStubTile) Extent() (*geom.Extent, uint64)         { return t.extent, t.srid }
func (t extentStubTile) BufferedExtent() (*geom.Extent, uint64) { return t.extent, t.srid }

// TestReplaceTokensNilExtentError pins audit P6-20: a tile reporting a
// nil extent must surface as an error from replaceTokens, not as an
// unchecked nil flowing into the query builder. Pre-fix: this panicked
// with a nil-pointer dereference on extent.MaxX().
func TestReplaceTokensNilExtentError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("replaceTokens panicked on a nil-extent tile (audit P6-20): %v", r)
		}
	}()

	l := &Layer{name: "lyr", geomField: "geom", idField: "gid"}
	sql, err := replaceTokens(2, "SELECT 1", l, nil, 3857, extentStubTile{extent: nil, srid: 3857}, false)
	if err == nil {
		t.Fatalf("replaceTokens must propagate the nil-extent error (audit P6-20), got SQL %q", sql)
	}
}

// TestGetTileExtentRejectsBadExtents pins audit P6-20: getTileExtent
// reports an error for nil and non-finite extents instead of returning
// them unchecked to callers.
func TestGetTileExtentRejectsBadExtents(t *testing.T) {
	nilTile := extentStubTile{extent: nil, srid: 3857}
	if _, _, err := getTileExtent(nilTile, false); err == nil {
		t.Error("getTileExtent with a nil extent must return an error (audit P6-20)")
	}
	if _, _, err := getTileExtent(nilTile, true); err == nil {
		t.Error("buffered getTileExtent with a nil extent must return an error (audit P6-20)")
	}

	nanTile := extentStubTile{
		extent: geom.NewExtent([2]float64{math.NaN(), 0}, [2]float64{1, 1}),
		srid:   3857,
	}
	if _, _, err := getTileExtent(nanTile, false); err == nil {
		t.Error("getTileExtent with a non-finite extent must return an error (audit P6-20)")
	}
	infTile := extentStubTile{
		extent: geom.NewExtent([2]float64{0, 0}, [2]float64{math.Inf(1), 1}),
		srid:   3857,
	}
	if _, _, err := getTileExtent(infTile, true); err == nil {
		t.Error("buffered getTileExtent with a non-finite extent must return an error (audit P6-20)")
	}
}

// TestGetTileExtentPropagatesSrid: a valid extent passes through with its
// SRID alongside the error-free result.
func TestGetTileExtentPropagatesSrid(t *testing.T) {
	goodTile := extentStubTile{
		extent: geom.NewExtent([2]float64{-1, -2}, [2]float64{3, 4}),
		srid:   4326,
	}
	extent, srid, err := getTileExtent(goodTile, false)
	if err != nil {
		t.Fatalf("getTileExtent: unexpected error: %v", err)
	}
	if srid != 4326 {
		t.Errorf("srid = %v, expected 4326", srid)
	}
	if extent == nil || extent[0] != -1 || extent[3] != 4 {
		t.Errorf("extent = %v, expected [-1 -2 3 4]", extent)
	}
}
