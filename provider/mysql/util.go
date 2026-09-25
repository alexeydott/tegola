package mysql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/config"
	"github.com/go-spatial/tegola/internal/log"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider"
)

// replaceTokens replaces tile and layer metadata tokens in a SQL query.
//
// bboxExtent must be the tile's buffered extent transformed to the layer's
// source SRID. Pixel dimensions and scale denominator are intentionally
// calculated from the tile's unbuffered Web Mercator extent, matching the
// PostGIS and GPKG providers.
func replaceTokens(qtext string, layer *Layer, tile provider.Tile, bboxExtent *geom.Extent) string {
	// WKT geometry columns hold text, so ST_Intersects(col, ...) fails; wrap
	// the column in ST_GeomFromText so MariaDB/MySQL can intersect it with
	// the tile bbox polygon. WKB columns hold BLOBs: ST_GeomFromWKB is the
	// documented constructor, avoiding implicit BLOB->geometry coercion.
	geomRef := quoteIdentifier(layer.geomFieldname)
	switch layer.geometryFormat {
	case GeometryFormatWKT:
		geomRef = geomFromTextSQL(geomRef, layer.srid)
	case GeometryFormatWKB:
		geomRef = geomFromWKBSQL(geomRef, layer.srid)
	}

	bboxSQL := "1=1"
	if layer.geometryFormat != GeometryFormatMOS && !layer.deferredInspection && !basic.IsSyntheticSRID(layer.srid) {
		bboxSQL = fmt.Sprintf(
			"ST_Intersects(%v, %v)",
			geomRef,
			geomFromTextSQL(fmt.Sprintf("'%v'", wktPolygon(bboxExtent)), layer.srid),
		)
	}

	// MOS blobs are opaque proprietary binaries: the server has no geometry
	// functions over them. EGKO MOS tables nevertheless expose indexed raw
	// bounds (MINX/MAXX/MINY/MAXY by default; configurable via the common
	// bbox_*_fieldname keys), so use those as a coarse SQL filter and keep
	// the exact decoded-geometry check in TileFeatures. When the bounds
	// predicate cannot be built (invalid quantization, nil extent), the
	// filter degrades to 1=1 and TileFeatures' in-memory check stays
	// authoritative.
	if layer.geometryFormat == GeometryFormatMOS {
		mosSQL, merr := mosBoundsSQL(layer, bboxExtent)
		if merr != nil {
			log.Errorf("layer (%v): %v; falling back to in-memory filtering", layer.name, merr)
		} else {
			bboxSQL = mosSQL
		}
	}

	extent, _ := tile.Extent()
	pixelWidth := (extent.MaxX() - extent.MinX()) / 256
	pixelHeight := (extent.MaxY() - extent.MinY()) / 256
	scaleDenominator := pixelWidth / 0.00028

	var geomType string
	if layer.geomType != nil {
		geomType = codec.GeomTypeName(layer.geomType)
	}

	z, x, y := tile.ZXY()
	tokenReplacer := strings.NewReplacer(
		config.BboxToken, bboxSQL,
		"!BOX!", bboxSQL,
		config.ZoomToken, strconv.FormatUint(uint64(z), 10),
		config.ZToken, strconv.FormatUint(uint64(z), 10),
		config.XToken, strconv.FormatUint(uint64(x), 10),
		config.YToken, strconv.FormatUint(uint64(y), 10),
		config.ScaleDenominatorToken, strconv.FormatFloat(scaleDenominator, 'f', 8, 64),
		config.PixelWidthToken, strconv.FormatFloat(pixelWidth, 'f', 8, 64),
		config.PixelHeightToken, strconv.FormatFloat(pixelHeight, 'f', 8, 64),
		config.IdFieldToken, layer.idFieldname,
		config.GeomFieldToken, layer.geomFieldname,
		config.GeomTypeToken, geomType,
	)

	return tokenReplacer.Replace(uppercaseTokens(qtext))
}

// geomFromTextSQL creates a geometry expression with the layer SRID when one
// is configured. MySQL and MariaDB otherwise assign SRID 0 to WKT values;
// comparing that value with a geometry column that has a non-zero SRID can
// fail with a different-SRID error instead of applying the spatial filter.
func geomFromTextSQL(value string, srid uint64) string {
	if srid == 0 {
		return fmt.Sprintf("ST_GeomFromText(%v)", value)
	}
	return fmt.Sprintf("ST_GeomFromText(%v, %d)", value, srid)
}

// geomFromWKBSQL creates a geometry expression from a raw WKB BLOB column
// with the layer SRID when one is configured, symmetric to geomFromTextSQL.
// ST_GeomFromWKB is the documented MySQL/MariaDB constructor for WKB values;
// without it, spatial predicates would rely on implicit BLOB->geometry
// coercion whose behavior differs between server versions.
func geomFromWKBSQL(value string, srid uint64) string {
	if srid == 0 {
		return fmt.Sprintf("ST_GeomFromWKB(%v)", value)
	}
	return fmt.Sprintf("ST_GeomFromWKB(%v, %d)", value, srid)
}

// mosBoundsSQL builds a coarse indexed filter for the raw bounds stored
// alongside MOS blobs. The bounds use the quantized MOS coordinate units,
// while bboxExtent is expressed in the layer CRS metres after applying
// mosUnitsFactor. Field names come from the resolved layer BBoxFields
// (layer > provider > defaults). An invalid scale is a configuration error,
// not a silent filter bypass.
func mosBoundsSQL(layer *Layer, bboxExtent *geom.Extent) (string, error) {
	return codec.BuildBoundsPredicate(
		layer.bboxFields,
		bboxExtent,
		codec.BoundsMOSRaw,
		layer.mosConfig,
		quoteIdentifier,
	)
}

// uppercaseTokens makes SQL tokens case-insensitive, matching PostGIS and GPKG.
func uppercaseTokens(str string) string {
	return provider.ParameterTokenRegexp.ReplaceAllStringFunc(str, strings.ToUpper)
}

func trimTrailingSemicolon(sqlText string) string {
	sqlText = strings.TrimSpace(sqlText)
	return strings.TrimSpace(strings.TrimSuffix(sqlText, ";"))
}
