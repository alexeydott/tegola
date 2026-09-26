//go:build cgo

package gpkg

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newGpkgMetadataDB opens a file-backed database with the GPKG metadata
// tables created (subset of the schema the provider reads). A file is used
// instead of :memory: so featureTableMetaData can run nested queries while
// its metadata rows cursor is open.
func newGpkgMetadataDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "meta.gpkg"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE gpkg_contents (
			table_name TEXT NOT NULL PRIMARY KEY,
			data_type TEXT NOT NULL,
			identifier TEXT UNIQUE,
			description TEXT DEFAULT '',
			last_change DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			min_x DOUBLE, min_y DOUBLE, max_x DOUBLE, max_y DOUBLE,
			srs_id INTEGER
		)`,
		`CREATE TABLE gpkg_geometry_columns (
			table_name TEXT NOT NULL,
			column_name TEXT NOT NULL,
			geometry_type_name TEXT NOT NULL,
			srs_id INTEGER NOT NULL,
			z TINYINT NOT NULL, m TINYINT NOT NULL,
			PRIMARY KEY (table_name, column_name)
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("exec %q: %v", ddl, err)
		}
	}
	return db
}

func TestHasGpkgMetadataTables(t *testing.T) {
	t.Run("no metadata tables", func(t *testing.T) {
		db, err := sql.Open("sqlite3", ":memory:")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = db.Close() }()

		has, err := hasGpkgMetadataTables(db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if has {
			t.Error("has = true, want false for a db without metadata tables")
		}
	})

	t.Run("both metadata tables", func(t *testing.T) {
		db := newGpkgMetadataDB(t)

		has, err := hasGpkgMetadataTables(db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Error("has = false, want true for a db with both metadata tables")
		}
	})

	t.Run("only gpkg_contents is rejected", func(t *testing.T) {
		db := newGpkgMetadataDB(t)
		if _, err := db.Exec(`DROP TABLE gpkg_geometry_columns`); err != nil {
			t.Fatalf("drop: %v", err)
		}

		has, err := hasGpkgMetadataTables(db)
		if err == nil {
			t.Fatal("expected error for partially present metadata tables, got nil")
		}
		if has {
			t.Error("has = true, want false on error")
		}
		if !strings.Contains(err.Error(), "partially present") {
			t.Errorf("error %q does not mention partial presence", err)
		}
	})

	t.Run("only gpkg_geometry_columns is rejected", func(t *testing.T) {
		db := newGpkgMetadataDB(t)
		if _, err := db.Exec(`DROP TABLE gpkg_contents`); err != nil {
			t.Fatalf("drop: %v", err)
		}

		has, err := hasGpkgMetadataTables(db)
		if err == nil {
			t.Fatal("expected error for partially present metadata tables, got nil")
		}
		if has {
			t.Error("has = true, want false on error")
		}
		if !strings.Contains(err.Error(), "partially present") {
			t.Errorf("error %q does not mention partial presence", err)
		}
	})
}

func TestFeatureTableMetaDataSkipsOrphanEntry(t *testing.T) {
	db := newGpkgMetadataDB(t)

	// a real feature table plus an orphaned metadata entry whose physical
	// table no longer exists
	if _, err := db.Exec(`CREATE TABLE roads (id INTEGER PRIMARY KEY AUTOINCREMENT, geom MULTIPOLYGON)`); err != nil {
		t.Fatalf("create roads: %v", err)
	}
	for _, ins := range []string{
		`INSERT INTO gpkg_contents (table_name, data_type, min_x, min_y, max_x, max_y, srs_id) VALUES ('roads', 'features', 1, 2, 3, 4, 3857)`,
		`INSERT INTO gpkg_contents (table_name, data_type, min_x, min_y, max_x, max_y, srs_id) VALUES ('ghost', 'features', 0, 0, 1, 1, 4326)`,
		`INSERT INTO gpkg_geometry_columns (table_name, column_name, geometry_type_name, srs_id, z, m) VALUES ('roads', 'geom', 'MULTIPOLYGON', 3857, 0, 0)`,
		`INSERT INTO gpkg_geometry_columns (table_name, column_name, geometry_type_name, srs_id, z, m) VALUES ('ghost', 'geom', 'MULTIPOLYGON', 4326, 0, 0)`,
	} {
		if _, err := db.Exec(ins); err != nil {
			t.Fatalf("insert %q: %v", ins, err)
		}
	}

	ftmd, err := featureTableMetaData(db)
	if err != nil {
		t.Fatalf("featureTableMetaData: expected orphan entry to be skipped, got error: %v", err)
	}
	if _, ok := ftmd["roads"]; !ok {
		t.Error("roads missing from result: valid table must still be registered")
	}
	if _, ok := ftmd["ghost"]; ok {
		t.Error("ghost present in result: orphan entry must be skipped")
	}
	if len(ftmd) != 1 {
		t.Errorf("len(result) = %v, want 1", len(ftmd))
	}
}

// TestFeatureTableMetaDataPerGeometryColumn exercises audit P6-13: the SRID
// must come from gpkg_geometry_columns.srs_id (per geometry column) with
// gpkg_contents.srs_id only as fallback, and a table with multiple geometry
// columns must yield one detail entry per column.
func TestFeatureTableMetaDataPerGeometryColumn(t *testing.T) {
	db := newGpkgMetadataDB(t)

	// make gc.srs_id nullable so the COALESCE fallback path is reachable
	// (the shared helper declares it NOT NULL).
	if _, err := db.Exec(`
		DROP TABLE gpkg_geometry_columns;
		CREATE TABLE gpkg_geometry_columns (
			table_name TEXT NOT NULL,
			column_name TEXT NOT NULL,
			geometry_type_name TEXT NOT NULL,
			srs_id INTEGER,
			z TINYINT NOT NULL,
			m TINYINT NOT NULL);`); err != nil {
		t.Fatal(err)
	}

	// dual: two geometry columns, each with its own authoritative srs_id;
	// gpkg_contents.srs_id is NULL and must NOT override the per-column
	// values.
	if _, err := db.Exec(`CREATE TABLE dual (fid INTEGER, geom_a BLOB, geom_b BLOB);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gpkg_contents (table_name, data_type, min_x, min_y, max_x, max_y, srs_id)
		VALUES ('dual', 'features', 0, 0, 10, 10, NULL)`); err != nil {
		t.Fatal(err)
	}
	// inserted out of name order on purpose: resolution must sort
	if _, err := db.Exec(`INSERT INTO gpkg_geometry_columns VALUES
		('dual', 'geom_b', 'POINT', 4326, 0, 0),
		('dual', 'geom_a', 'POINT', 3857, 0, 0)`); err != nil {
		t.Fatal(err)
	}

	// lone: gc.srs_id is NULL → falls back to gpkg_contents.srs_id
	if _, err := db.Exec(`CREATE TABLE lone (fid INTEGER, geom BLOB);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gpkg_contents (table_name, data_type, min_x, min_y, max_x, max_y, srs_id)
		VALUES ('lone', 'features', 0, 0, 1, 1, 2154)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gpkg_geometry_columns VALUES ('lone', 'geom', 'POINT', NULL, 0, 0)`); err != nil {
		t.Fatal(err)
	}

	ftmd, err := featureTableMetaData(db)
	if err != nil {
		t.Fatal(err)
	}

	dual := ftmd["dual"]
	if len(dual) != 2 {
		t.Fatalf("dual geometry columns = %d, expected 2 (pre-P6-13 the map overwrote entries)", len(dual))
	}
	if dual[0].geomFieldname != "geom_a" || dual[0].srid != 3857 {
		t.Errorf("dual[0] = %+v, expected geom_a/srid 3857 (sorted by column name, srs_id from gpkg_geometry_columns)", dual[0])
	}
	if dual[1].geomFieldname != "geom_b" || dual[1].srid != 4326 {
		t.Errorf("dual[1] = %+v, expected geom_b/srid 4326 (sorted by column name, srs_id from gpkg_geometry_columns)", dual[1])
	}

	lone := ftmd["lone"]
	if len(lone) != 1 {
		t.Fatalf("lone geometry columns = %d, expected 1", len(lone))
	}
	if lone[0].srid != 2154 {
		t.Errorf("lone srid = %d, expected 2154 (COALESCE fallback to gpkg_contents.srs_id)", lone[0].srid)
	}
}

// TestPickGeometryColumn exercises the per-configuration geometry column
// selection introduced by audit P6-13.
func TestPickGeometryColumn(t *testing.T) {
	cols := []featureTableDetails{
		{geomFieldname: "geom_a", srid: 3857},
		{geomFieldname: "geom_b", srid: 4326},
	}

	// explicit match is case-insensitive
	got, err := pickGeometryColumn(cols, "Geom_A", true)
	if err != nil {
		t.Fatalf("explicit case-insensitive match errored: %v", err)
	}
	if got.geomFieldname != "geom_a" {
		t.Errorf("picked %q, expected geom_a", got.geomFieldname)
	}

	// explicit mismatch is a clear error naming the available columns
	_, err = pickGeometryColumn(cols, "nope", true)
	if err == nil {
		t.Fatal("explicit unknown column errored = false, expected true")
	}
	if !strings.Contains(err.Error(), "no geometry column") || !strings.Contains(err.Error(), "geom_a, geom_b") {
		t.Errorf("error = %q, expected it to name the missing and available columns", err.Error())
	}

	// implicit pick on an ambiguous table: first entry, no error
	got, err = pickGeometryColumn(cols, "whatever", false)
	if err != nil {
		t.Fatalf("implicit pick errored: %v", err)
	}
	if got.geomFieldname != "geom_a" {
		t.Errorf("implicit pick = %q, expected geom_a (first entry)", got.geomFieldname)
	}

	// implicit pick of a single-column table ignores the configured name
	single := []featureTableDetails{{geomFieldname: "the_geom", srid: 4326}}
	got, err = pickGeometryColumn(single, "whatever", false)
	if err != nil {
		t.Fatalf("implicit single pick errored: %v", err)
	}
	if got.geomFieldname != "the_geom" {
		t.Errorf("implicit single pick = %q, expected the_geom", got.geomFieldname)
	}
}
