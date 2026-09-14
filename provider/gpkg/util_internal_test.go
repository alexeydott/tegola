package gpkg

import (
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/provider"
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
			output := replaceTokens(tc.qtext, &tc.layer, tc.tile, tc.extent)

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
					minx <= 180 AND maxx >= -180 AND miny <= 85.0511 AND maxy >= -85.0511`,
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
					minx <= 180 AND maxx >= -180 AND miny <= 85.0511 AND maxy >= -85.0511 AND min_zoom = 3`,
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
			expected: `SELECT feature_id, shape, '[0 0]',
				11, 1070, 676, 11,
				76.43702829, 76.43702829, 272989.38673277`,
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}
