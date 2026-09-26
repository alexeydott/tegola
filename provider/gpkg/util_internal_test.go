package gpkg

import (
	"strings"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/provider"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
)

func TestReplaceTokens(t *testing.T) {
	type tcase struct {
		qtext    string
		layer    Layer
		tile     provider.Tile
		extent   *geom.Extent
		expected string
	}

	fn := func(tc tcase) func(*testing.T) {
		return func(t *testing.T) {
			if tc.tile == nil {
				tc.tile = provider.NewTile(0, 0, 0, 0, tegola.WebMercator)
			}
			if tc.extent == nil {
				tc.extent, _ = tc.tile.BufferedExtent()
			}
			output, err := replaceTokens(tc.qtext, &tc.layer, tc.tile, tc.extent)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.expected != output {
				t.Errorf("expected %v\n got\n %v", tc.expected, output)
				return
			}
		}
	}

	tests := map[string]tcase{
		"zoom": {
			qtext: `
				SELECT
					fid, geom, featurecla, min_zoom, 22 as max_zoom, minx, miny, maxx, maxy
				FROM
					ne_110m_land t JOIN rtree_ne_110m_land_geom si ON t.fid = si.id
				WHERE
					min_zoom <= !ZOOM! AND max_zoom >= !ZOOM!`,
			tile: provider.NewTile(9, 0, 0, 0, tegola.WebMercator),
			expected: `
				SELECT
					fid, geom, featurecla, min_zoom, 22 as max_zoom, minx, miny, maxx, maxy
				FROM
					ne_110m_land t JOIN rtree_ne_110m_land_geom si ON t.fid = si.id
				WHERE
					min_zoom <= 9 AND max_zoom >= 9`,
		},
		"bbox": {
			qtext: `
				SELECT
					fid, geom, featurecla, min_zoom, 22 as max_zoom, minx, miny, maxx, maxy
				FROM
					ne_110m_land t JOIN rtree_ne_110m_land_geom si ON t.fid = si.id
				WHERE
					!BBOX!`,
			extent: &geom.Extent{
				-180, -85.0511,
				180, 85.0511,
			},
			expected: `
				SELECT
					fid, geom, featurecla, min_zoom, 22 as max_zoom, minx, miny, maxx, maxy
				FROM
					ne_110m_land t JOIN rtree_ne_110m_land_geom si ON t.fid = si.id
				WHERE
					` + "`MAXX` >= -180 AND `MINX` <= 180 AND `MAXY` >= -85.0511 AND `MINY` <= 85.0511",
		},
		"bbox zoom": {
			qtext: `
				SELECT
					fid, geom, featurecla, min_zoom, 22 as max_zoom, minx, miny, maxx, maxy
				FROM
					ne_110m_land t JOIN rtree_ne_110m_land_geom si ON t.fid = si.id
				WHERE
					!BBOX! AND min_zoom = !ZOOM!`,
			extent: &geom.Extent{
				-180, -85.0511,
				180, 85.0511,
			},
			tile: provider.NewTile(3, 0, 0, 0, tegola.WebMercator),
			expected: `
				SELECT
					fid, geom, featurecla, min_zoom, 22 as max_zoom, minx, miny, maxx, maxy
				FROM
					ne_110m_land t JOIN rtree_ne_110m_land_geom si ON t.fid = si.id
				WHERE
					` + "`MAXX` >= -180 AND `MINX` <= 180 AND `MAXY` >= -85.0511 AND `MINY` <= 85.0511 AND min_zoom = 3",
		},
		"tile coordinates, scale and layer metadata": {
			qtext: `SELECT !id_field!, !geom_field!, '!geom_type!',
				!zoom!, !x!, !y!, !z!,
				!pixel_width!, !pixel_height!, !scale_denominator!`,
			layer: Layer{
				idFieldname:   "feature_id",
				geomFieldname: "shape",
				geomType:      geom.Point{},
			},
			tile: provider.NewTile(11, 1070, 676, 64, tegola.WebMercator),
			expected: `SELECT feature_id, shape, 'POINT',
				11, 1070, 676, 11,
				76.43702829, 76.43702829, 272989.38673277`,
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

func TestTrimTrailingSemicolon(t *testing.T) {
	if got := trimTrailingSemicolon(" SELECT * FROM features ;  "); got != "SELECT * FROM features" {
		t.Fatalf("trimTrailingSemicolon() = %q", got)
	}
}

// P6-16: GeoPackage files must be opened read-only, with a busy timeout.
func TestSQLiteReadOnlyDSN(t *testing.T) {
	got := sqliteReadOnlyDSN(`C:\data\file.gpkg`)
	want := `file:C:\data\file.gpkg?mode=ro&_busy_timeout=5000`
	if got != want {
		t.Fatalf("sqliteReadOnlyDSN() = %q, want %q", got, want)
	}
}

// A12: bounds predicate build errors must fail closed (returned to the
// caller), never silently fall back to 1=1.
func TestReplaceTokensFailClosed(t *testing.T) {
	tile := provider.NewTile(0, 0, 0, 0, tegola.WebMercator)
	layer := Layer{}
	out, err := replaceTokens("SELECT * FROM t WHERE !BBOX!", &layer, tile, nil)
	if err == nil {
		t.Fatalf("expected bounds predicate build error to surface (A12), got output %q", out)
	}
}

// TestBuildBBoxPredicateContract pins the two bounds-predicate modes for
// custom SQL (audit A06) and the verbatim quoting of the resolved bounds
// column names (audit A09):
//   - custom MOS SQL filters over raw quantized bounds columns
//     (BoundsMOSRaw);
//   - custom geometry_format=gpkg SQL filters in the source CRS
//     (BoundsSourceCRS, no MOS scaling).
func TestBuildBBoxPredicateContract(t *testing.T) {
	extent := geom.NewExtent([2]float64{10, 20}, [2]float64{30, 40})

	mosLayer := &Layer{
		geometryFormat: codec.FormatMOS,
		bboxFields:     codec.BBoxFields{"minx", "maxx", "miny", "maxy"},
		mosConfig:      codec.MOSConfig{Precision: 1, UnitFactor: 1},
	}
	pred, err := buildBBoxPredicate(mosLayer, extent)
	if err != nil {
		t.Fatalf("mos predicate: %v", err)
	}
	// raw quantized values (x10^precision) and actual column names quoted
	// verbatim
	want := "`maxx` >= 100 AND `minx` <= 300 AND `maxy` >= 200 AND `miny` <= 400"
	if pred != want {
		t.Errorf("MOS custom SQL predicate: want %q, got %q", want, pred)
	}

	// (the "gpkg" value of GeometryFormatGPKG, inlined because the
	// constant lives in the cgo-gated gpkg.go)
	gpkgLayer := &Layer{
		geometryFormat: "gpkg",
		bboxFields:     codec.DefaultBBoxFields(),
	}
	pred, err = buildBBoxPredicate(gpkgLayer, extent)
	if err != nil {
		t.Fatalf("gpkg predicate: %v", err)
	}
	want = "`MAXX` >= 10 AND `MINX` <= 30 AND `MAXY` >= 20 AND `MINY` <= 40"
	if pred != want {
		t.Errorf("gpkg custom SQL predicate must run in the source CRS without MOS scaling: want %q, got %q", want, pred)
	}
}

// TestReplaceTokensPreservesTrailingClauses covers the regression matrix
// row "WHERE ... AND !BBOX! ORDER BY ... -> ORDER BY preserved": token
// replacement must keep trailing clauses verbatim.
func TestReplaceTokensPreservesTrailingClauses(t *testing.T) {
	tile := provider.NewTile(0, 0, 0, 0, tegola.WebMercator)
	extent, _ := tile.BufferedExtent()
	layer := &Layer{bboxFields: codec.DefaultBBoxFields()}
	out, err := replaceTokens("SELECT * FROM t WHERE kind = 'a' AND !BBOX! ORDER BY id DESC", layer, tile, extent)
	if err != nil {
		t.Fatalf("replaceTokens: %v", err)
	}
	if !strings.Contains(out, "ORDER BY id DESC") {
		t.Errorf("ORDER BY must be preserved: %q", out)
	}
	if strings.Contains(out, "!BBOX!") {
		t.Errorf("!BBOX! must be replaced: %q", out)
	}
}
