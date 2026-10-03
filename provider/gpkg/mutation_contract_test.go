//go:build cgo

// mutation_contract_test.go proves the GeoPackage native writer against a
// real database file: admission, Insert/Replace/Update/Delete, rollback,
// multi-action transactions and negative controls (W14, ADR-0014).
package gpkg_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/geom/encoding/wkb"
	"github.com/alexeydott/tegola/dict"
	"github.com/alexeydott/tegola/provider"
	"github.com/alexeydott/tegola/provider/gpkg"
)

const mutationDDL = `CREATE TABLE parcels (
	fid INTEGER PRIMARY KEY,
	geom BLOB,
	name TEXT,
	lots INTEGER NOT NULL,
	price REAL
)`

func newMutationFixture(t *testing.T) (string, provider.MutationProvider) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mut.gpkg")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(mutationDDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	conf := dict.Dict{
		"filepath": path,
		"layers": []map[string]interface{}{
			{
				"name":               "parcels",
				"tablename":          "parcels",
				"id_fieldname":       "fid",
				"geometry_fieldname": "geom",
				"geometry_format":    "wkb",
				"srid":               4326,
				"fields":             []string{"name", "lots", "price"},
			},
		},
	}
	p, err := gpkg.NewTileProvider(conf, nil)
	if err != nil {
		t.Fatalf("NewTileProvider: %v", err)
	}
	t.Cleanup(gpkg.Cleanup)
	gp, ok := p.(*gpkg.Provider)
	if !ok {
		t.Fatalf("not a *gpkg.Provider")
	}
	return path, gp.MutationWriter()
}

func wkbPoint(t *testing.T, x, y float64) []byte {
	t.Helper()
	b, err := wkb.EncodeBytes(geom.Point{x, y})
	if err != nil {
		t.Fatalf("wkb: %v", err)
	}
	return b
}

func strVal(s string) provider.MutationValue {
	return provider.MutationValue{Kind: provider.MutationValueString, String: s}
}
func intVal(n int64) provider.MutationValue {
	return provider.MutationValue{Kind: provider.MutationValueInteger, Integer: n}
}

func countRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM parcels").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestMutationAdmission(t *testing.T) {
	_, w := newMutationFixture(t)
	wd, err := w.DescribeWritable(context.Background(), "parcels")
	if err != nil {
		t.Fatalf("DescribeWritable: %v", err)
	}
	if wd.Table != "parcels" || wd.IDColumn != "fid" || wd.GeometryColumn != "geom" {
		t.Fatalf("bad descriptor: %+v", wd)
	}
	if wd.WritableColumns["name"] != "name" || wd.WritableColumns["lots"] != "lots" {
		t.Fatalf("writable columns: %v", wd.WritableColumns)
	}
	if _, err := w.DescribeWritable(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown layer")
	}
}

func TestMutationInsertReadback(t *testing.T) {
	path, w := newMutationFixture(t)
	ctx := context.Background()
	tx, err := w.BeginFeatureTx(ctx, provider.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	out, err := tx.Apply(ctx, provider.Mutation{
		Op:         provider.MutationInsert,
		Collection: "parcels",
		Properties: map[string]provider.MutationValue{
			"name": strVal("alpha"),
			"lots": intVal(3),
		},
		GeometryWKB: wkbPoint(t, 10, 20),
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.FeatureID == 0 || out.Affected != 1 {
		t.Fatalf("bad outcome: %+v", out)
	}
	if _, err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := countRows(t, path); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	// Geometry round-trips as a GPKG binary readable by the read path.
	db, _ := sql.Open("sqlite3", path)
	defer func() { _ = db.Close() }()
	var raw []byte
	var name string
	var lots int64
	if err := db.QueryRow("SELECT geom, name, lots FROM parcels WHERE fid = ?", out.FeatureID).Scan(&raw, &name, &lots); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if name != "alpha" || lots != 3 {
		t.Fatalf("readback values: %q %d", name, lots)
	}
	if len(raw) < 5 || (raw[0] != 0x01 && raw[0] != 0x00) {
		t.Fatal("geometry is not WKB")
	}
	// WKB round-trips through the decoder.
	if _, err := wkb.DecodeBytes(raw); err != nil {
		t.Fatalf("WKB decode: %v", err)
	}
}

func TestMutationUpdateAttributeOnlyPreservesGeometry(t *testing.T) {
	path, w := newMutationFixture(t)
	ctx := context.Background()
	tx, _ := w.BeginFeatureTx(ctx, provider.TxOptions{})
	out, err := tx.Apply(ctx, provider.Mutation{
		Op:          provider.MutationInsert,
		Collection:  "parcels",
		Properties:  map[string]provider.MutationValue{"name": strVal("a"), "lots": intVal(1)},
		GeometryWKB: wkbPoint(t, 1, 2),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	db, _ := sql.Open("sqlite3", path)
	var before []byte
	if err := db.QueryRow("SELECT geom FROM parcels WHERE fid = ?", out.FeatureID).Scan(&before); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	tx2, _ := w.BeginFeatureTx(ctx, provider.TxOptions{})
	if _, err := tx2.Apply(ctx, provider.Mutation{
		Op:         provider.MutationUpdate,
		Collection: "parcels",
		FeatureID:  out.FeatureID,
		Properties: map[string]provider.MutationValue{"name": strVal("b")},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	db2, _ := sql.Open("sqlite3", path)
	defer func() { _ = db2.Close() }()
	var after []byte
	var name string
	if err := db2.QueryRow("SELECT geom, name FROM parcels WHERE fid = ?", out.FeatureID).Scan(&after, &name); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if name != "b" {
		t.Fatalf("name = %q", name)
	}
	if string(after) != string(before) {
		t.Fatal("attribute-only update changed geometry bytes")
	}
	_ = path
}

func TestMutationReplaceAndDelete(t *testing.T) {
	_, w := newMutationFixture(t)
	ctx := context.Background()
	tx, _ := w.BeginFeatureTx(ctx, provider.TxOptions{})
	out, err := tx.Apply(ctx, provider.Mutation{
		Op:          provider.MutationInsert,
		Collection:  "parcels",
		Properties:  map[string]provider.MutationValue{"name": strVal("x"), "lots": intVal(9)},
		GeometryWKB: wkbPoint(t, 5, 5),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Replace sets every writable property; absent ones become NULL.
	if _, err := tx.Apply(ctx, provider.Mutation{
		Op:          provider.MutationReplace,
		Collection:  "parcels",
		FeatureID:   out.FeatureID,
		Properties:  map[string]provider.MutationValue{"lots": intVal(10)},
		GeometryWKB: wkbPoint(t, 6, 6),
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := tx.Apply(ctx, provider.Mutation{
		Op:         provider.MutationDelete,
		Collection: "parcels",
		FeatureID:  out.FeatureID,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// All three actions in one native transaction; delete wins.
	tx2, _ := w.BeginFeatureTx(ctx, provider.TxOptions{})
	_, err = tx2.Apply(ctx, provider.Mutation{
		Op:         provider.MutationUpdate,
		Collection: "parcels",
		FeatureID:  out.FeatureID,
		Properties: map[string]provider.MutationValue{"name": strVal("ghost")},
	})
	if err == nil {
		t.Fatal("expected not-found for deleted feature")
	}
	_ = tx2.Rollback(ctx)
	if me, ok := provider.AsMutationError(err); !ok || me.Kind != provider.MutationErrNotFound {
		t.Fatalf("expected not-found, got %v", err)
	}
}

func TestMutationRollback(t *testing.T) {
	path, w := newMutationFixture(t)
	ctx := context.Background()
	tx, _ := w.BeginFeatureTx(ctx, provider.TxOptions{})
	if _, err := tx.Apply(ctx, provider.Mutation{
		Op:          provider.MutationInsert,
		Collection:  "parcels",
		Properties:  map[string]provider.MutationValue{"name": strVal("tmp"), "lots": intVal(1)},
		GeometryWKB: wkbPoint(t, 0, 0),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countRows(t, path); n != 0 {
		t.Fatalf("rows = %d after rollback, want 0", n)
	}
}

func TestMutationUnknownPropertyRejected(t *testing.T) {
	_, w := newMutationFixture(t)
	ctx := context.Background()
	tx, _ := w.BeginFeatureTx(ctx, provider.TxOptions{})
	defer func() { _ = tx.Rollback(ctx) }()
	_, err := tx.Apply(ctx, provider.Mutation{
		Op:          provider.MutationInsert,
		Collection:  "parcels",
		Properties:  map[string]provider.MutationValue{"nope": strVal("x"), "lots": intVal(1)},
		GeometryWKB: wkbPoint(t, 0, 0),
	})
	if err == nil {
		t.Fatal("expected schema violation for unknown property")
	}
	if me, ok := provider.AsMutationError(err); !ok || me.Kind != provider.MutationErrSchemaViolation {
		t.Fatalf("expected schema violation, got %v", err)
	}
}

func TestMutationNotFound(t *testing.T) {
	_, w := newMutationFixture(t)
	ctx := context.Background()
	tx, _ := w.BeginFeatureTx(ctx, provider.TxOptions{})
	defer func() { _ = tx.Rollback(ctx) }()
	for _, op := range []provider.MutationOp{provider.MutationUpdate, provider.MutationReplace, provider.MutationDelete} {
		_, err := tx.Apply(ctx, provider.Mutation{
			Op:         op,
			Collection: "parcels",
			FeatureID:  9999,
			Properties: map[string]provider.MutationValue{"lots": intVal(1)},
		})
		if me, ok := provider.AsMutationError(err); !ok || me.Kind != provider.MutationErrNotFound {
			t.Fatalf("op %v: expected not-found, got %v", op, err)
		}
	}
}
