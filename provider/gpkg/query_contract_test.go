//go:build cgo

package gpkg_test

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/alexeydott/tegola/dict"
	"github.com/alexeydott/tegola/provider"
	"github.com/alexeydott/tegola/provider/gpkg"
	"github.com/alexeydott/tegola/provider/internal/querytest"
)

func TestFeatureQueryContract(t *testing.T) {
	querytest.Run(t, func(t *testing.T, fixture querytest.Fixture) querytest.Instance {
		format := "wkb"
		for _, row := range fixture.Rows {
			if row.EmptyGeometry || row.MalformedGeometry == "native" {
				format = "gpkg"
			}
			if row.Metadata {
				format = "mos"
			}
		}
		fx := newRawFixture(t, []string{"CREATE TABLE items (id INTEGER UNIQUE, geom BLOB, name TEXT, value INTEGER, start_time INTEGER, end_time INTEGER)"})
		if format == "gpkg" {
			addFeatureMetadata(t, fx.path, "items", "geom", 4326)
		}
		layer := map[string]interface{}{
			"name": "items", "tablename": "items", "id_fieldname": "id", "geometry_fieldname": "geom",
			"geometry_format": format, "geometry_type": "point", "srid": 4326, "fields": []string{"name", "value"},
			"temporal_start_field": "start_time", "temporal_end_field": "end_time", "temporal_storage": "unix_nanoseconds",
		}
		if fixture.InvalidTemporalMapping {
			layer["temporal_end_field"] = "missing"
		}
		tiler, err := gpkg.NewTileProvider(dict.Dict{"filepath": fx.path, "layers": []map[string]interface{}{layer}}, nil)
		if err != nil {
			return querytest.Instance{SetupError: err}
		}
		p := tiler.(*gpkg.Provider)
		t.Cleanup(func() {
			if err := p.Close(); err != nil {
				t.Error(err)
			}
		})
		var rows [][]interface{}
		for _, row := range fixture.Rows {
			var id, geometry, start, end any
			if !row.MissingID {
				id = int64(row.Feature.ID)
			}
			if row.Feature.Geometry != nil {
				geometry = wkbGeomBytes(t, row.Feature.Geometry)
				if format == "gpkg" {
					geometry = append(featureHeader(false, 4326), geometry.([]byte)...)
				}
			}
			if row.EmptyGeometry {
				geometry = featureHeader(true, 4326)
			}
			if row.MalformedGeometry != "" {
				geometry = []byte{0xff}
			}
			if row.Metadata {
				geometry = mosSystemInfoBlob(2, "", 0, false)
			}
			if row.Start != nil {
				start = row.Start.UnixNano()
			}
			if row.End != nil {
				end = row.End.UnixNano()
			}
			rows = append(rows, []interface{}{id, geometry, row.Feature.Tags["name"], row.Feature.Tags["value"], start, end})
		}
		insertRows(t, fx.path, "items", []string{"id", "geom", "name", "value", "start_time", "end_time"}, rows)
		return querytest.Instance{Querier: p, Layer: "items", CountMode: querytest.OptionalCount}
	})
}

func featureHeader(empty bool, srid uint32) []byte {
	flags := byte(1)
	if empty {
		flags |= 1 << 4
	}
	return binary.LittleEndian.AppendUint32([]byte{'G', 'P', 0, flags}, srid)
}

func addFeatureMetadata(t *testing.T, path, table, geometry string, srid int) {
	t.Helper()
	db, err := openSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, ddl := range []string{
		"CREATE TABLE gpkg_contents (table_name TEXT, data_type TEXT, min_x REAL, min_y REAL, max_x REAL, max_y REAL, srs_id INTEGER)",
		"CREATE TABLE gpkg_geometry_columns (table_name TEXT, column_name TEXT, geometry_type_name TEXT, srs_id INTEGER, z INTEGER, m INTEGER)",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO gpkg_contents VALUES (?, 'features', -180, -90, 180, 90, ?)", table, srid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO gpkg_geometry_columns VALUES (?, ?, 'POINT', ?, 0, 0)", table, geometry, srid); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureQueryFieldsAndUnsignedIDs(t *testing.T) {
	fx := newRawFixture(t, []string{"CREATE TABLE items (id INTEGER PRIMARY KEY, geom TEXT, name TEXT)"})
	insertRows(t, fx.path, "items", []string{"id", "geom", "name"}, [][]interface{}{{1, "POINT (1 2)", "one"}})
	tiler, err := gpkg.NewTileProvider(dict.Dict{"filepath": fx.path, "layers": []map[string]interface{}{{"name": "items", "tablename": "items", "id_fieldname": "id", "geometry_fieldname": "geom", "geometry_format": "wkt", "srid": 4326}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := tiler.(*gpkg.Provider)
	t.Cleanup(func() { _ = p.Close() })
	var ids []uint64
	result, err := p.QueryFeatures(context.Background(), "items", provider.FeatureQuery{Limit: 10, IDs: []uint64{math.MaxUint64, 1}}, func(f *provider.Feature) error { ids = append(ids, f.ID); return nil })
	if err != nil || result.NumberReturned != 1 || !reflect.DeepEqual(ids, []uint64{1}) {
		t.Fatalf("unsigned identity: %+v %v %v", result, ids, err)
	}
	result, err = p.QueryFeatures(context.Background(), "items", provider.FeatureQuery{Limit: 10, IDs: []uint64{math.MaxUint64}}, func(*provider.Feature) error { t.Fatal("impossible identity delivered"); return nil })
	if err != nil || result.NumberMatched == nil || *result.NumberMatched != 0 {
		t.Fatalf("impossible ID: %+v %v", result, err)
	}
	_, err = p.QueryFeatures(context.Background(), "items", provider.FeatureQuery{Limit: 10, Fields: []string{"name` FROM items;--"}}, func(*provider.Feature) error { t.Fatal("invalid field delivered"); return nil })
	var invalid provider.InvalidFeatureQueryError
	if !errors.As(err, &invalid) {
		t.Fatalf("field allowlist: %v", err)
	}
}

var _ provider.FeatureQuerier = (*gpkg.Provider)(nil)
var _ provider.TemporalLayerInfo = gpkg.Layer{}
var _ provider.FeatureQueryLayerInfo = gpkg.Layer{}
