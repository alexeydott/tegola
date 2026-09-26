//go:build cgo

package gpkg_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/provider"
	"github.com/go-spatial/tegola/provider/gpkg"
)

// TestNullFeatureIDSkipped covers the gpkg part of audit P6-11: a NULL
// feature id must not silently become ID 0 (duplicate IDs collapse in the
// MVT). The documented strategy is to skip the row and warn, since
// provider.Feature.ID is a plain uint64 and cannot encode "no id".
func TestNullFeatureIDSkipped(t *testing.T) {
	// id is deliberately NOT a rowid alias so NULLs can be stored.
	fx := newRawFixture(t, []string{
		"CREATE TABLE places (id INTEGER, geom BLOB, name TEXT)",
	})
	insertRows(t, fx.path, "places", []string{"id", "geom", "name"}, [][]interface{}{
		{7, wkbGeomBytes(t, geom.Point{50, 50}), "kept"},
		{nil, wkbGeomBytes(t, geom.Point{60, 60}), "dropped"},
	})

	conf := dict.Dict{
		"filepath": fx.path,
		"layers": []map[string]interface{}{
			{
				"name":               "raw_layer",
				"tablename":          "places",
				"id_fieldname":       "id",
				"geometry_fieldname": "geom",
				"geometry_format":    "wkb",
				"srid":               3857,
				"fields":             []string{"name"},
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

	var ids []uint64
	var names []interface{}
	err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
		ids = append(ids, f.ID)
		names = append(names, f.Tags["name"])
		return nil
	})
	if err != nil {
		t.Fatalf("TileFeatures: %v", err)
	}

	if len(ids) != 1 {
		t.Fatalf("feature count = %v (%v), want 1 (NULL-id row skipped)", len(ids), ids)
	}
	if ids[0] != 7 {
		t.Errorf("feature id = %v, want 7", ids[0])
	}
	if names[0] != "kept" {
		t.Errorf("feature tag name = %v, want kept", names[0])
	}
}

// TestBlobTagBase64 covers audit P6-18: BLOB tag values are arbitrary
// binary and must not be string()ed into the MVT as invalid UTF-8. The
// chosen strategy is base64-encoding the raw bytes (lossless, cheap).
func TestBlobTagBase64(t *testing.T) {
	blob := []byte{0x00, 0xff, 0x41, 0x7f, 0xfe}
	fx := newRawFixture(t, []string{
		"CREATE TABLE items (id INTEGER PRIMARY KEY, geom BLOB, data BLOB)",
	})
	insertRows(t, fx.path, "items", []string{"geom", "data"}, [][]interface{}{
		{wkbGeomBytes(t, geom.Point{50, 50}), blob},
	})

	conf := dict.Dict{
		"filepath": fx.path,
		"layers": []map[string]interface{}{
			{
				"name":               "raw_layer",
				"tablename":          "items",
				"id_fieldname":       "id",
				"geometry_fieldname": "geom",
				"geometry_format":    "wkb",
				"srid":               3857,
				"fields":             []string{"data"},
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

	var tags []map[string]interface{}
	err = p.TileFeatures(context.TODO(), "raw_layer", &tile, nil, func(f *provider.Feature) error {
		tags = append(tags, f.Tags)
		return nil
	})
	if err != nil {
		t.Fatalf("TileFeatures: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("feature count = %v, want 1", len(tags))
	}

	want := base64.StdEncoding.EncodeToString(blob)
	got, ok := tags[0]["data"].(string)
	if !ok {
		t.Fatalf("blob tag = %#v (%T), want a string", tags[0]["data"], tags[0]["data"])
	}
	if got != want {
		t.Errorf("blob tag = %q, want %q (standard base64 of raw bytes)", got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("blob tag %q is not valid UTF-8", got)
	}
}

// TestMixedCaseColumnNames covers audit P6-17: column-name lookups must be
// case-insensitive because SQLite column names can differ in case from the
// configured names. Two seams: the registration colSet lookup (tablename
// layers) and the id/geom scan dispatch (custom SQL layers, where the result
// columns carry the table's own casing).
func TestMixedCaseColumnNames(t *testing.T) {
	// table columns deliberately differ in case from the configured names
	fx := newRawFixture(t, []string{
		"CREATE TABLE mixed (ID INTEGER, Geom BLOB, Name TEXT)",
	})
	insertRows(t, fx.path, "mixed", []string{"ID", "Geom", "Name"}, [][]interface{}{
		{7, wkbGeomBytes(t, geom.Point{50, 50}), "kept"},
	})

	t.Run("tablename layer registration", func(t *testing.T) {
		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":               "raw_layer",
					"tablename":          "mixed",
					"id_fieldname":       "id",
					"geometry_fieldname": "geom",
					"geometry_format":    "wkb",
					"srid":               3857,
					"fields":             []string{"name"},
				},
			},
		}

		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			// pre-fix the case-sensitive colSet lookup fails with
			// "table ... has no geometry column ..."
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		f := fetchOneFeature(t, p, "raw_layer")
		if f.ID != 7 {
			t.Errorf("feature id = %v, want 7 (id column matched case-insensitively)", f.ID)
		}
		if f.Geometry == nil {
			t.Errorf("feature geometry = nil, want decoded point (geom column matched case-insensitively)")
		}
		// the result columns carry the table's own casing
		if f.Tags["Name"] != "kept" {
			t.Errorf("feature tags = %v, want Name=kept", f.Tags)
		}
	})

	t.Run("custom sql layer scan dispatch", func(t *testing.T) {
		conf := dict.Dict{
			"filepath": fx.path,
			"layers": []map[string]interface{}{
				{
					"name":               "raw_layer",
					"sql":                "SELECT * FROM mixed",
					"id_fieldname":       "id",
					"geometry_fieldname": "geom",
					"geometry_format":    "wkb",
					"srid":               3857,
					"fields":             []string{"Name"},
				},
			},
		}

		p, err := gpkg.NewTileProvider(conf, nil)
		if err != nil {
			t.Fatalf("NewTileProvider: %v", err)
		}
		t.Cleanup(gpkg.Cleanup)

		f := fetchOneFeature(t, p, "raw_layer")
		// SELECT * returns the table's own column casing, so the id/geom
		// dispatch must compare case-insensitively (pre-fix the id lands
		// in tags and the geometry is nil)
		if f.ID != 7 {
			t.Errorf("feature id = %v, want 7 (id column matched case-insensitively)", f.ID)
		}
		if f.Geometry == nil {
			t.Errorf("feature geometry = nil, want decoded point (geom column matched case-insensitively)")
		}
		if f.Tags["Name"] != "kept" {
			t.Errorf("feature tags = %v, want Name=kept", f.Tags)
		}
	})
}

// fetchOneFeature returns the single feature produced for a layer covering
// the fixture point at (50, 50).
func fetchOneFeature(t *testing.T, p provider.Tiler, layer string) provider.Feature {
	t.Helper()

	tile := MockTile{
		srid: 3857,
		bufferedExtent: geom.NewExtent(
			[2]float64{0, 0},
			[2]float64{100, 100},
		),
	}

	var got []provider.Feature
	err := p.TileFeatures(context.TODO(), layer, &tile, nil, func(f *provider.Feature) error {
		got = append(got, *f)
		return nil
	})
	if err != nil {
		t.Fatalf("TileFeatures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("feature count = %v, want 1", len(got))
	}
	return got[0]
}

// gpkgPointBlob crafts a GeoPackage binary geometry blob: 8-byte header
// (magic 'GP', version 0, flags 0x01 = little-endian + no envelope) plus a
// little-endian WKB point body.
func gpkgPointBlob(t *testing.T, srid int32, x, y float64) []byte {
	t.Helper()
	hdr := make([]byte, 8)
	hdr[0], hdr[1], hdr[2], hdr[3] = 'G', 'P', 0, 0x01
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(srid))
	return append(hdr, wkbGeomBytes(t, geom.Point{x, y})...)
}

// TestGeometryColumnSelectedByConfig exercises audit P6-13 end to end: a
// table with multiple geometry columns must serve the column named by
// geometry_fieldname (with its own SRID), not whichever column won the old
// last-one-wins metadata map, and an explicit unknown geometry column must
// fail registration with a clear error.
func TestGeometryColumnSelectedByConfig(t *testing.T) {
	ddl := `
		CREATE TABLE dual (fid INTEGER PRIMARY KEY, geom_a BLOB, geom_b BLOB, note TEXT);
		CREATE TABLE gpkg_contents (table_name TEXT NOT NULL, data_type TEXT NOT NULL, min_x DOUBLE, min_y DOUBLE, max_x DOUBLE, max_y DOUBLE, srs_id INTEGER);
		CREATE TABLE gpkg_geometry_columns (table_name TEXT NOT NULL, column_name TEXT NOT NULL, geometry_type_name TEXT NOT NULL, srs_id INTEGER NOT NULL, z TINYINT NOT NULL, m TINYINT NOT NULL);
		CREATE TABLE rtree_dual_geom_a (id INTEGER, minx DOUBLE, maxx DOUBLE, miny DOUBLE, maxy DOUBLE);
		CREATE TABLE rtree_dual_geom_b (id INTEGER, minx DOUBLE, maxx DOUBLE, miny DOUBLE, maxy DOUBLE);`
	fx := newRawFixture(t, []string{ddl})
	insertRows(t, fx.path, "dual",
		[]string{"fid", "geom_a", "geom_b", "note"},
		[][]interface{}{
			{1, gpkgPointBlob(t, 3857, 1, 1), gpkgPointBlob(t, 4326, 2, 2), "note"},
		})
	insertRows(t, fx.path, "gpkg_contents",
		[]string{"table_name", "data_type", "min_x", "min_y", "max_x", "max_y", "srs_id"},
		[][]interface{}{{"dual", "features", 0.0, 0.0, 10.0, 10.0, nil}})
	insertRows(t, fx.path, "gpkg_geometry_columns",
		[]string{"table_name", "column_name", "geometry_type_name", "srs_id", "z", "m"},
		[][]interface{}{
			// inserted out of name order: pre-fix the map kept geom_b
			{"dual", "geom_b", "POINT", 4326, 0, 0},
			{"dual", "geom_a", "POINT", 3857, 0, 0},
		})
	for _, rt := range []string{"rtree_dual_geom_a", "rtree_dual_geom_b"} {
		insertRows(t, fx.path, rt,
			[]string{"id", "minx", "maxx", "miny", "maxy"},
			[][]interface{}{{1, 0.0, 10.0, 0.0, 10.0}})
	}

	conf := dict.Dict{
		"filepath": fx.path,
		"srid":     3857,
		"layers": []map[string]interface{}{
			{
				"name":               "dual_layer",
				"tablename":          "dual",
				"id_fieldname":       "fid",
				"geometry_fieldname": "geom_a",
				"fields":             []string{"note"},
			},
		},
	}
	p, err := gpkg.NewTileProvider(conf, nil)
	if err != nil {
		t.Fatalf("NewTileProvider: %v", err)
	}
	t.Cleanup(gpkg.Cleanup)

	f := fetchOneFeature(t, p, "dual_layer")
	want := geom.Point{1, 1}
	if fmt.Sprintf("%v", f.Geometry) != fmt.Sprintf("%v", want) {
		t.Errorf("geometry = %v (%T), expected %v (geom_a blob; pre-P6-13 the last gpkg_geometry_columns row won and geom_b was served)",
			f.Geometry, f.Geometry, want)
	}

	// explicit unknown geometry column: clear registration error
	conf["layers"] = []map[string]interface{}{
		{
			"name":               "bad_layer",
			"tablename":          "dual",
			"id_fieldname":       "fid",
			"geometry_fieldname": "geom_no_such",
			"fields":             []string{"note"},
		},
	}
	if _, err := gpkg.NewTileProvider(conf, nil); err == nil {
		t.Fatal("NewTileProvider with unknown geometry_fieldname errored = false, expected true")
	} else if !strings.Contains(err.Error(), "no geometry column") {
		t.Errorf("error = %q, expected it to name the missing geometry column", err.Error())
	}
}
