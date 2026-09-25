//go:build cgo

package gpkg

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/slippy"
	"github.com/go-spatial/tegola/provider"

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
		defer db.Close()

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

// fixesTestTile is a minimal provider.Tile for internal unit tests.
type fixesTestTile struct {
	z    slippy.Zoom
	x, y uint
	ext  *geom.Extent
	srid uint64
}

func (t *fixesTestTile) Extent() (*geom.Extent, uint64)         { return t.ext, t.srid }
func (t *fixesTestTile) BufferedExtent() (*geom.Extent, uint64) { return t.ext, t.srid }
func (t *fixesTestTile) ZXY() (slippy.Zoom, uint, uint)         { return t.z, t.x, t.y }

func TestBuildDeferredInspectionSQLReplacesTokens(t *testing.T) {
	layer := &Layer{
		name:          "mos_layer",
		tablename:     "mos_layer",
		geomFieldname: "geom",
		sql: `SELECT id, geom FROM mos_layer
			WHERE !ZOOM! >= 0 AND !BBOX! AND x = !X! AND y = !Y! AND z = !Z!;`,
	}

	tile := &fixesTestTile{z: 7, x: 68, y: 41, srid: 3857}
	got := buildDeferredInspectionSQL(layer, tile)

	for _, leftover := range []string{"!ZOOM!", "!BBOX!", "!X!", "!Y!", "!Z!"} {
		if strings.Contains(got, leftover) {
			t.Errorf("built SQL still contains token %v: %v", leftover, got)
		}
	}
	for _, want := range []string{" 7 ", " 68 ", " 41 ", "1=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("built SQL %q does not contain %q", got, want)
		}
	}
}

var _ provider.Tile = (*fixesTestTile)(nil)
