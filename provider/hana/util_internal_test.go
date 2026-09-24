package hana

import (
	"testing"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/provider"
)

func TestValidateCRSFormatCompatibility(t *testing.T) {
	type tcase struct {
		srid           int
		providerType   string
		geometryFormat string
		expectedErr    string
	}

	fn := func(tc tcase) func(t *testing.T) {
		return func(t *testing.T) {
			err := validateCRSFormatCompatibility(tc.srid, tc.providerType, tc.geometryFormat)
			if tc.expectedErr == "" {
				if err != nil {
					t.Errorf("unexpected error, Expected nil Got %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error, Expected %v Got nil", tc.expectedErr)
				return
			}
			if err.Error() != tc.expectedErr {
				t.Errorf("incorrect error,\n Expected \n \t%v\n Got \n \t%v", tc.expectedErr, err.Error())
			}
		}
	}

	synthetic := int(basic.SyntheticSRIDMin + 1000)

	tests := map[string]tcase{
		"non-synthetic srid, native format": {
			srid: 3857,
		},
		"non-synthetic srid, raw format": {
			srid:           3857,
			geometryFormat: "wkb",
		},
		"synthetic srid, wkb ok": {
			srid:           synthetic,
			geometryFormat: "wkb",
		},
		"synthetic srid, wkt ok": {
			srid:           synthetic,
			geometryFormat: "wkt",
		},
		"synthetic srid, mos ok": {
			srid:           synthetic,
			geometryFormat: "mos",
		},
		"synthetic srid, empty format rejected": {
			srid:        synthetic,
			expectedErr: `crs_defn (synthetic SRID) requires geometry_format = "wkb", "wkt", or "mos"; native HANA ST_Geometry columns use a database-side SRS`,
		},
		"synthetic srid, mvt rejected": {
			srid:           synthetic,
			providerType:   "mvt_hana",
			geometryFormat: "wkb",
			expectedErr:    `crs_defn (synthetic SRID) is not supported for MVT providers`,
		},
		"planar equivalent of round-earth is not synthetic": {
			srid:           int(1000000000 + 4326),
			geometryFormat: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

func TestReplaceTokens(t *testing.T) {
	type tcase struct {
		dbVersion uint
		sql       string
		tile      provider.Tile
		expected  string
		layer     Layer
	}

	fn := func(tc tcase) func(t *testing.T) {
		return func(t *testing.T) {
			sql, err := replaceTokens(tc.dbVersion, tc.sql, tc.layer.IDFieldName(), tc.layer.GeomFieldName(), tc.layer.GeomType(), tc.layer.SRID(), tc.tile, true)
			if err != nil {
				t.Errorf("unexpected error, Expected nil Got %v", err)
				return
			}

			if sql != tc.expected {
				t.Errorf("incorrect sql,\n Expected \n \t%v\n Got \n \t%v", tc.expected, sql)
				return
			}
		}
	}

	tests := map[string]tcase{
		"replace BBOX for HANA 1": {
			dbVersion: 1,
			sql:       "SELECT * FROM foo WHERE !BBOX!",
			layer:     Layer{srid: tegola.WebMercator, geomField: "geom"},
			tile:      provider.NewTile(2, 1, 1, 64, tegola.WebMercator),
			expected:  `SELECT * FROM foo WHERE "geom".ST_IntersectsRect(NEW ST_POINT($1, $3), NEW ST_POINT($2, $3)) = 1`,
		},
		"replace BBOX": {
			dbVersion: 4,
			sql:       "SELECT * FROM foo WHERE !BBOX!",
			layer:     Layer{srid: tegola.WebMercator, geomField: "geom"},
			tile:      provider.NewTile(2, 1, 1, 64, tegola.WebMercator),
			expected:  `SELECT * FROM foo WHERE "geom".ST_IntersectsRectPlanar(NEW ST_POINT($1, $3), NEW ST_POINT($2, $3)) = 1`,
		},
		"replace BBOX for round-earth with planar equivalent": {
			dbVersion: 4,
			sql:       "SELECT * FROM foo WHERE !BBOX!",
			layer:     Layer{srid: 1000004326, geomField: "geom"},
			tile:      provider.NewTile(2, 1, 1, 64, tegola.WebMercator),
			expected:  `SELECT * FROM foo WHERE "geom".ST_SRID($3).ST_IntersectsRectPlanar(NEW ST_POINT($1, $3), NEW ST_POINT($2, $3)) = 1`,
		},
		"replace BBOX with != in query": {
			dbVersion: 4,
			sql:       "SELECT * FROM foo WHERE !BBOX! AND bar != 42",
			layer:     Layer{srid: tegola.WebMercator, geomField: "geom"},
			tile:      provider.NewTile(2, 1, 1, 64, tegola.WebMercator),
			expected:  `SELECT * FROM foo WHERE "geom".ST_IntersectsRectPlanar(NEW ST_POINT($1, $3), NEW ST_POINT($2, $3)) = 1 AND bar != 42`,
		},
		"replace BBOX and ZOOM 1": {
			dbVersion: 4,
			sql:       "SELECT id, scalerank=!ZOOM! FROM foo WHERE !BBOX!",
			layer:     Layer{srid: tegola.WebMercator, geomField: "geom"},
			tile:      provider.NewTile(2, 1, 1, 64, tegola.WebMercator),
			expected:  `SELECT id, scalerank=2 FROM foo WHERE "geom".ST_IntersectsRectPlanar(NEW ST_POINT($1, $3), NEW ST_POINT($2, $3)) = 1`,
		},
		"replace BBOX and ZOOM 2": {
			dbVersion: 4,
			sql:       "SELECT id, scalerank=!ZOOM! FROM foo WHERE !BBOX!",
			layer:     Layer{srid: tegola.WebMercator, geomField: "geom"},
			tile:      provider.NewTile(16, 11241, 26168, 64, tegola.WebMercator),
			expected:  `SELECT id, scalerank=16 FROM foo WHERE "geom".ST_IntersectsRectPlanar(NEW ST_POINT($1, $3), NEW ST_POINT($2, $3)) = 1`,
		},
		"replace pixel_width/height and scale_denominator": {
			dbVersion: 4,
			sql:       "SELECT id, !pixel_width! as width, !pixel_height! as height, !scale_denominator! as scale_denom FROM foo WHERE !BBOX!",
			layer:     Layer{srid: tegola.WebMercator, geomField: "geom"},
			tile:      provider.NewTile(11, 1070, 676, 64, tegola.WebMercator),
			expected:  `SELECT id, 76.43702829 as width, 76.43702829 as height, 272989.38673277 as scale_denom FROM foo WHERE "geom".ST_IntersectsRectPlanar(NEW ST_POINT($1, $3), NEW ST_POINT($2, $3)) = 1`,
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

// TestGenSQLRawFormat covers the R3-08 provider-level regression for the
// HANA SQL generation matrix: raw geometry columns (wkb / wkt / mos) must
// bypass ST_Geometry wrappers and spatial predicates, while native format
// keeps them.
func TestGenSQLRawFormat(t *testing.T) {
	type tcase struct {
		name           string
		geometryFormat string
		expectedSQL    string
	}

	tests := []tcase{
		{
			name:           "native format uses spatial predicate",
			geometryFormat: "",
			expectedSQL:   `SELECT "id", "geom".ST_AsBinary()  AS "geom" FROM "tbl" WHERE !BBOX!`,
		},
		{
			name:           "mos format selects raw blob without bbox predicate",
			geometryFormat: "mos",
			expectedSQL:   `SELECT "id", "geom" AS "geom" FROM "tbl" WHERE "geom" IS NOT NULL`,
		},
		{
			name:           "wkb format selects raw value without bbox predicate",
			geometryFormat: "wkb",
			expectedSQL:   `SELECT "id", "geom" AS "geom" FROM "tbl" WHERE "geom" IS NOT NULL`,
		},
		{
			name:           "wkt format selects raw value without bbox predicate",
			geometryFormat: "wkt",
			expectedSQL:   `SELECT "id", "geom" AS "geom" FROM "tbl" WHERE "geom" IS NOT NULL`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := &Layer{
				name:           "layer",
				geomField:      "geom",
				idField:        "id",
				srid:           tegola.WebMercator,
				geometryFormat: tc.geometryFormat,
			}
			sql, err := genSQL(l, "tbl", []string{"id", "geom"}, false, ProviderType)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sql != tc.expectedSQL {
				t.Errorf("incorrect sql,\n Expected \n \t%v\n Got \n \t%v", tc.expectedSQL, sql)
			}
		})
	}
}

func TestUppercaseTokens(t *testing.T) {
	type tcase struct {
		str      string
		expected string
	}

	fn := func(tc tcase) func(t *testing.T) {
		return func(t *testing.T) {
			out := uppercaseTokens(tc.str)

			if out != tc.expected {
				t.Errorf("expected \n \t%v\n out \n \t%v", tc.expected, out)
				return
			}
		}
	}

	tests := map[string]tcase{
		"uppercase tokens": {
			str:      "this !lower! case !STrInG! should uppercase !TOKENS!",
			expected: "this !LOWER! case !STRING! should uppercase !TOKENS!",
		},
		"no tokens": {
			str:      "no token",
			expected: "no token",
		},
		"empty string": {
			str:      "",
			expected: "",
		},
		"unclosed token": {
			str:      "unclosed !token",
			expected: "unclosed !token",
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}
