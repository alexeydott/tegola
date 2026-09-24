//go:build cgo

package gpkg_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/mos"
	"github.com/go-spatial/tegola/provider"
	"github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider/gpkg"
)

// openSQLite opens the fixture database; the sqlite3 driver is registered by
// the gpkg package import.
func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite3", path)
}

// rawFixture builds a SQLite file holding a plain table of raw-format
// geometries plus optional GPKG metadata tables, so raw-format layers can be
// exercised without gpkg_geometry_columns support.
type rawFixture struct {
	path string
}

// newRawFixture creates an empty SQLite database with the given tables.
func newRawFixture(t *testing.T, tables []string) rawFixture {
	t.Helper()

	path := filepath.Join(t.TempDir(), "raw.gpkg")
	db, err := openSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, ddl := range tables {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("exec %q: %v", ddl, err)
		}
	}
	return rawFixture{path: path}
}

// mosSystemInfoBlob assembles a MapplGIS LayerInfo blob (packed record,
// mirrors mos/systeminfo_test.go systemInfoBlobBuilder).
func mosSystemInfoBlob(precision int32, projection string, mapUnits byte, unitsDefined bool) []byte {
	buf := make([]byte, 64+len(projection))
	buf[0] = byte(len("Ver 1"))
	copy(buf[1:], "Ver 1")
	binary.LittleEndian.PutUint32(buf[11:15], uint32(precision))
	if projection != "" {
		buf[15] = 1
	}
	binary.LittleEndian.PutUint32(buf[26:30], uint32(1))
	buf[52] = mapUnits
	if unitsDefined {
		buf[53] = 1
	}
	binary.LittleEndian.PutUint32(buf[60:64], uint32(len(projection)))
	copy(buf[64:], projection)
	return buf
}

// mosPolylineBlob assembles a minimal MOS polyline blob (native 10-byte
// header + per-subobject point count, quantized coordinates,
// precision 2 => hundredths of a unit).
func mosPolylineBlob(points [][2]int32) []byte {
	buf := make([]byte, 0, 14+8*len(points))
	buf = append(buf, mos.TypePolyline, 0)
	buf = binary.LittleEndian.AppendUint16(buf, 0) // add flag
	buf = binary.LittleEndian.AppendUint16(buf, 1) // sub-object count
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(points)))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(points))) // per-subobject point count
	for _, p := range points {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(p[0]))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(p[1]))
	}
	return buf
}

func wkbGeomBytes(t *testing.T, g geom.Geometry) []byte {
	t.Helper()
	b, err := wkb.EncodeBytes(g)
	if err != nil {
		t.Fatalf("encode wkb: %v", err)
	}
	return b
}

// TestRawFormatTableLayer covers the R3-02 audit items: raw-format (wkb /
// wkt / mos) layers backed by plain SQLite tables that are NOT registered in
// gpkg_geometry_columns.
// TestMOSPointAndBBox covers the R3-08 e2e contract items for the gpkg
// provider: Point geometries survive the decode pipeline, bbox inside/outside
// filtering holds, and a malformed MOS blob yields a controlled error rather
// than a panic.
func TestMOSPointAndBBox(t *testing.T) {
	t.Run("mos point inside bbox is emitted", func(t *testing.T) {
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		insertRows(t, fx.path, "mos_layer", []string{"geom"}, [][]interface{}{
			{mosSystemInfoBlob(2, "", byte(mos.UnitsMetres), true)},
			// point object: type 2, one sub-object with one point (200, 400)
			// quantized => (2, 4) metres with precision 2
			{mosPointBlob([][2]int32{{200, 400}})},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "mos_layer",
					"tablename":       "mos_layer",
					"geometry_format": "mos",
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{0, 0},
				[2]float64{10, 10},
			),
		}
		var count int
		err = p.TileFeatures(context.TODO(), "mos_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			pt, ok := f.Geometry.(geom.Point)
			if !ok {
				t.Errorf("geometry type = %T, want geom.Point", f.Geometry)
				return nil
			}
			if pt[0] != 2 || pt[1] != 4 {
				t.Errorf("point = %v, want [2 4]", pt)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 1 {
			t.Errorf("feature count = %v, want 1", count)
		}
	})

	t.Run("mos point outside bbox is filtered", func(t *testing.T) {
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		insertRows(t, fx.path, "mos_layer", []string{"geom"}, [][]interface{}{
			{mosSystemInfoBlob(2, "", byte(mos.UnitsMetres), true)},
			// (9000, 9000) metres, far outside the tile window below
			{mosPointBlob([][2]int32{{900000, 900000}})},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "mos_layer",
					"tablename":       "mos_layer",
					"geometry_format": "mos",
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{0, 0},
				[2]float64{10, 10},
			),
		}
		var count int
		err = p.TileFeatures(context.TODO(), "mos_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 0 {
			t.Errorf("feature count = %v, want 0 (outside bbox)", count)
		}
	})

	t.Run("malformed mos blob yields controlled error", func(t *testing.T) {
		// a truncated polyline blob (no point coordinates) must produce a
		// controlled error at provider registration, never a panic.
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		insertRows(t, fx.path, "mos_layer", []string{"geom"}, [][]interface{}{
			{mosSystemInfoBlob(2, "", byte(mos.UnitsMetres), true)},
			// polyline header claiming one sub-object but with no points:
			// native 10-byte header truncated after the count fields
			{[]byte{mos.TypePolyline, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0}},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "mos_layer",
					"tablename":       "mos_layer",
					"geometry_format": "mos",
				},
			},
		}
		_, err := gpkg.NewTileProvider(conf, nil)
		if err == nil {
			t.Fatal("expected NewTileProvider to fail on a malformed MOS blob, got nil")
		}
		if !strings.Contains(err.Error(), "invalid geometry prefix") {
			t.Fatalf("unexpected error for malformed blob: %v", err)
		}
		t.Logf("got expected controlled error: %v", err)
	})
}

// mosPointBlob assembles a minimal MOS point blob (native 10-byte header,
// one sub-object, one quantized point).
func mosPointBlob(points [][2]int32) []byte {
	buf := make([]byte, 0, 14+8*len(points))
	buf = append(buf, mos.TypePoint, 0)
	buf = binary.LittleEndian.AppendUint16(buf, 0) // add flag
	buf = binary.LittleEndian.AppendUint16(buf, 1) // sub-object count
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(points)))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(points))) // per-subobject point count
	for _, p := range points {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(p[0]))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(p[1]))
	}
	return buf
}

func TestRawFormatTableLayer(t *testing.T) {
	t.Run("wkb plain table no rtree", func(t *testing.T) {
		// table is NOT in gpkg_geometry_columns; registration must succeed
		// via SQLite inspection and TileFeatures must filter in memory.
		fx := newRawFixture(t, []string{
			"CREATE TABLE roads (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB, name TEXT)",
		})
		insertRows(t, fx.path, "roads", []string{"geom", "name"}, [][]interface{}{
			{wkbGeomBytes(t, geom.LineString{{-100, -100}, {100, 100}}), "a"},
			{wkbGeomBytes(t, geom.LineString{{-1000, -1000}, {-500, -500}}), "b"},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"tablename":       "roads",
					"geometry_format": "wkb",
					"srid":            3857,
				},
			},
		}

		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		// providers keep their SQLite connection open until Cleanup;
		// registered here so the file is released before t.TempDir's
		// RemoveAll runs (t.Cleanup is LIFO within the subtest).
		t.Cleanup(gpkg.Cleanup)

		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{-200, -200},
				[2]float64{200, 200},
			),
		}

		var count int
		err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			if f.SRID != 3857 {
				t.Errorf("feature SRID = %v, want 3857", f.SRID)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 1 {
			t.Errorf("feature count = %v, want 1 (in-memory bbox filter)", count)
		}
	})

	t.Run("wkt plain table", func(t *testing.T) {
		fx := newRawFixture(t, []string{
			"CREATE TABLE pois (id INTEGER PRIMARY KEY AUTOINCREMENT, geom TEXT)",
		})
		insertRows(t, fx.path, "pois", []string{"geom"}, [][]interface{}{
			{"POINT(10 10)"},
			{"POINT(9000 9000)"},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"tablename":       "pois",
					"geometry_format": "wkt",
					"srid":            3857,
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{0, 0},
				[2]float64{100, 100},
			),
		}
		var count int
		err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 1 {
			t.Errorf("feature count = %v, want 1", count)
		}
	})

	t.Run("mos table with system info first", func(t *testing.T) {
		// The system-info blob precedes geometries: registration must
		// consume it (precision 2, metre units), infer the geometry type
		// from the first real geometry, and never emit the metadata row.
		// Projection registers a synthetic CRS and shifts the feature SRID
		// away from the explicit layer value.
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		insertRows(t, fx.path, "mos_layer", []string{"geom"}, [][]interface{}{
			// system info: precision 2, +proj=merc (metres), units defined
			{mosSystemInfoBlob(2, "+proj=merc +ellps=WGS84 +datum=WGS84 +units=m +no_defs", byte(mos.UnitsMetres), true)},
			// quantized 1 => 0.01 with precision 2
			{mosPolylineBlob([][2]int32{{100, 100}, {300, 300}})},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"tablename":       "mos_layer",
					"geometry_format": "mos",
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		// decoded coords are 0.01-scale metres (1..3 metres); a small
		// WebMercator window around the origin must contain them.
		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{-10, -10},
				[2]float64{10, 10},
			),
		}
		var count int
		var firstGeom geom.Geometry
		err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			if firstGeom == nil {
				firstGeom = f.Geometry
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 1 {
			t.Fatalf("feature count = %v, want 1 (system-info row must be skipped)", count)
		}
		ls, ok := firstGeom.(geom.LineString)
		if !ok {
			t.Fatalf("geometry type = %T, want geom.LineString", firstGeom)
		}
		if len(ls) != 2 || ls[0][0] != 1 || ls[1][0] != 3 {
			t.Errorf("linestring = %v, want [[1 1] [3 3]] (precision 2 applied)", ls)
		}
	})

	t.Run("mos explicit precision overrides system info", func(t *testing.T) {
		// explicit mos_precision must win over the system-info value
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		insertRows(t, fx.path, "mos_layer", []string{"geom"}, [][]interface{}{
			{mosSystemInfoBlob(3, "", byte(mos.UnitsMillimetres), true)},
			{mosPolylineBlob([][2]int32{{1000, 1000}, {3000, 3000}})},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"tablename":       "mos_layer",
					"geometry_format": "mos",
					"mos_precision":   2, // explicit, overrides system-info's 3
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{-10, -10},
				[2]float64{10, 10},
			),
		}
		var count int
		err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			ls, ok := f.Geometry.(geom.LineString)
			if !ok {
				t.Errorf("geometry type = %T, want geom.LineString", f.Geometry)
				return nil
			}
			// explicit precision 2 => 1000/100 = 10; system-info millimetre
			// units (not overridden) scale it by 0.001 => 0.01. Before the
			// PrecisionSet merge fix the system-info precision 3 won and
			// produced 0.001.
			if ls[0][0] != 0.01 {
				t.Errorf("linestring start x = %v, want 0.01 (explicit precision 2, mm units)", ls[0][0])
			}
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 1 {
			t.Errorf("feature count = %v, want 1", count)
		}
	})

	t.Run("negative gpkg format on plain table fails", func(t *testing.T) {
		// geometry_format=gpkg against a table that is not registered in
		// gpkg_geometry_columns must fail with a clear error, not silently
		// succeed or panic.
		fx := newRawFixture(t, []string{
			"CREATE TABLE plain (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		insertRows(t, fx.path, "plain", []string{"geom"}, [][]interface{}{
			{mosPolylineBlob([][2]int32{{1, 1}, {2, 2}})},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"tablename":       "plain",
					"geometry_format": "gpkg",
				},
			},
		}
		_, err := gpkg.NewTileProvider(conf, nil)
		if err == nil {
			t.Fatal("expected NewTileProvider to fail with geometry_format=gpkg on a plain table, got nil")
		}
		t.Logf("got expected error: %v", err)
	})

	t.Run("raw custom sql mos system info first", func(t *testing.T) {
		// custom SQL against a MOS table where the system-info blob is the
		// first row of the inspection query: it must be consumed and the
		// next row decoded.
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		insertRows(t, fx.path, "mos_layer", []string{"geom"}, [][]interface{}{
			{mosSystemInfoBlob(2, "", byte(mos.UnitsMetres), true)},
			{mosPolylineBlob([][2]int32{{100, 100}, {300, 300}})},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"sql":             "SELECT id, geom FROM mos_layer",
					"geometry_format": "mos",
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{-10, -10},
				[2]float64{10, 10},
			),
		}
		var count int
		err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 1 {
			t.Errorf("feature count = %v, want 1", count)
		}
	})

	t.Run("raw custom sql mos system info applies projection", func(t *testing.T) {
		// custom SQL against a MOS table where the system-info blob carries
		// a non-empty projection: it must be consumed, the projection
		// registered as the layer SRID (ApplySystemInfoCRS), and the next
		// row decoded.
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		proj4 := "+proj=merc +lat_ts=56.5 +ellps=clrk66 +type=crs"
		insertRows(t, fx.path, "mos_layer", []string{"geom"}, [][]interface{}{
			{mosSystemInfoBlob(2, proj4, byte(mos.UnitsMetres), true)},
			{mosPolylineBlob([][2]int32{{100, 100}, {300, 300}})},
		})

		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"sql":             "SELECT id, geom FROM mos_layer",
					"geometry_format": "mos",
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		// the non-empty system info projection must be applied: the layer
		// SRID becomes the synthetic SRID registered from the proj.4 defn
		// instead of the provider default (3857).
		lyrs, lerr := p.Layers()
		if lerr != nil {
			t.Fatalf("Layers: %v", lerr)
		}
		if len(lyrs) != 1 {
			t.Fatalf("layer count = %v, want 1", len(lyrs))
		}
		if srid := lyrs[0].SRID(); srid == 3857 {
			t.Errorf("layer srid = %v, want the synthetic SRID from the system info projection", srid)
		}

		tile := MockTile{
			srid: 3857,
			bufferedExtent: geom.NewExtent(
				[2]float64{-10, -10},
				[2]float64{10, 10},
			),
		}
		var count int
		err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
			count++
			return nil
		})
		if err != nil {
			t.Fatalf("TileFeatures: %v", err)
		}
		if count != 1 {
			t.Errorf("feature count = %v, want 1", count)
		}
	})

	t.Run("raw custom sql empty result registers", func(t *testing.T) {
		// custom SQL returning no rows must register a placeholder layer
		// rather than fail startup.
		fx := newRawFixture(t, []string{
			"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
		})
		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"sql":             "SELECT id, geom FROM mos_layer WHERE id = -1",
					"geometry_format": "mos",
				},
			},
		}
		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		layers, err := p.Layers()
		if err != nil {
			t.Fatalf("Layers: %v", err)
		}
		if len(layers) != 1 {
			t.Fatalf("layer count = %v, want 1", len(layers))
		}
	})

	t.Run("raw custom sql with bbox token rejected", func(t *testing.T) {
		// Raw custom-SQL contract: raw formats (wkb/wkt/mos) cannot use the
		// native-spatial !BBOX! token (its expansion assumes a native spatial
		// column); startup must fail with a descriptive error instead of
		// generating invalid per-tile SQL. Matching is case-insensitive,
		// mirroring uppercaseTokens normalization at tile time.
		for _, tc := range []struct {
			name string
			sql  string
		}{
			{"upper", "SELECT id, geom FROM wkb_layer WHERE geom && !BBOX!"},
			{"lower", "SELECT id, geom FROM wkb_layer WHERE geom && !bbox!"},
		} {
			fx := newRawFixture(t, []string{
				"CREATE TABLE wkb_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
			})
			insertRows(t, fx.path, "wkb_layer", []string{"geom"}, [][]interface{}{
				{wkbGeomBytes(t, geom.Point{10, 20})},
			})

			conf := dict.Dict{
				"filepath": fx.path,
				"layers": []map[string]interface{}{
					{
						"name":            "raw_layer",
						"sql":             tc.sql,
						"geometry_format": "wkb",
					},
				},
			}
			_, err := gpkg.NewTileProvider(conf, nil)
			t.Cleanup(gpkg.Cleanup)
			if err == nil {
				t.Fatalf("%v: expected NewTileProvider to reject !BBOX! in raw custom SQL", tc.name)
			}
			if !strings.Contains(err.Error(), "raw_layer") {
				t.Errorf("%v: error must name the layer, got: %v", tc.name, err)
			}
			if !strings.Contains(err.Error(), "in-memory") {
				t.Errorf("%v: error must suggest the in-memory filter, got: %v", tc.name, err)
			}
		}
	})

	t.Run("missing geometry column fails", func(t *testing.T) {		fx := newRawFixture(t, []string{
			"CREATE TABLE plain (id INTEGER PRIMARY KEY AUTOINCREMENT, data BLOB)",
		})
		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":            "raw_layer",
					"tablename":       "plain",
					"geometry_format": "wkb",
				},
			},
		}
		_, err := gpkg.NewTileProvider(conf, nil)
		if err == nil {
			t.Fatal("expected NewTileProvider to fail with missing geometry column, got nil")
		}
		t.Logf("got expected error: %v", err)
	})
}

// insertRows inserts rows into the given table with named columns (the id
// column is autoincrement and is omitted).
func insertRows(t *testing.T, path, table string, cols []string, rows [][]interface{}) {
	t.Helper()

	db, err := openSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, row := range rows {
		ph := make([]string, 0, len(row))
		args := make([]interface{}, 0, len(row))
		for _, v := range row {
			if v == nil {
				continue
			}
			ph = append(ph, "?")
			args = append(args, v)
		}
		q := fmt.Sprintf("INSERT INTO %v (%v) VALUES (%v)", table, joinStrings(cols, ", "), joinStrings(ph, ", "))
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("insert %v: %v", q, err)
		}
	}
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func TestMOSLayerInfoPositionInvariant(t *testing.T) {
	// Sampling invariant (docs/provider-contract.md): the MOS system-info
	// blob must configure the layer identically wherever it sits inside the
	// shared 16-row sample window — first row, middle or last.
	proj4 := "+proj=merc +lat_ts=56.5 +ellps=clrk66 +type=crs"
	geomBlob := mosPolylineBlob([][2]int32{{100, 100}, {300, 300}})

	for _, pos := range []int{1, 5, geometrycodec.InspectionSampleLimit} {
		for _, mode := range []string{"table", "custom sql"} {
			pos, mode := pos, mode
			t.Run(fmt.Sprintf("%s position %d", mode, pos), func(t *testing.T) {
				fx := newRawFixture(t, []string{
					"CREATE TABLE mos_layer (id INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB)",
				})
				rows := make([][]interface{}, 0, geometrycodec.InspectionSampleLimit)
				for i := 1; i <= geometrycodec.InspectionSampleLimit; i++ {
					if i == pos {
						rows = append(rows, []interface{}{mosSystemInfoBlob(2, proj4, byte(mos.UnitsMetres), true)})
						continue
					}
					rows = append(rows, []interface{}{geomBlob})
				}
				insertRows(t, fx.path, "mos_layer", []string{"geom"}, rows)

				layerConf := map[string]interface{}{
					"name":            "raw_layer",
					"geometry_format": "mos",
				}
				if mode == "custom sql" {
					layerConf["sql"] = "SELECT id, geom FROM mos_layer"
				} else {
					layerConf["tablename"] = "mos_layer"
				}
				conf := dict.Dict{
					"filepath": fx.path,
					"layers":   []map[string]interface{}{layerConf},
				}
				p, err := gpkg.NewTileProvider(conf, nil)
				if err != nil {
					t.Fatalf("NewTileProvider: %v", err)
				}
				t.Cleanup(gpkg.Cleanup)

				lyrs, lerr := p.Layers()
				if lerr != nil {
					t.Fatalf("Layers: %v", lerr)
				}
				if len(lyrs) != 1 {
					t.Fatalf("layer count = %v, want 1", len(lyrs))
				}
				// the projection must register the synthetic SRID in every
				// position, never the provider default (3857)
				if srid := lyrs[0].SRID(); srid == 3857 {
					t.Errorf("layer srid = %v, want the synthetic SRID from the system info projection", srid)
				}

				tile := MockTile{
					srid: 3857,
					bufferedExtent: geom.NewExtent(
						[2]float64{-10, -10},
						[2]float64{10, 10},
					),
				}
				var count int
				err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
					count++
					return nil
				})
				if err != nil {
					t.Fatalf("TileFeatures: %v", err)
				}
				if count != geometrycodec.InspectionSampleLimit-1 {
					t.Errorf("feature count = %v, want %v (system-info row skipped)", count, geometrycodec.InspectionSampleLimit-1)
				}
			})
		}
	}
}
