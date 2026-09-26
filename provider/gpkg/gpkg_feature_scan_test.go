//go:build cgo

package gpkg_test

import (
	"context"
	"testing"

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
