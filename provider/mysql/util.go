package mysql

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/config"
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
	// the tile bbox polygon.
	geomRef := quoteIdentifier(layer.geomFieldname)
	if layer.geometryFormat == GeometryFormatWKT {
		geomRef = fmt.Sprintf("ST_GeomFromText(%v)", geomRef)
	}

	bboxSQL := fmt.Sprintf(
		"ST_Intersects(%v, ST_GeomFromText('%v'))",
		geomRef,
		wktPolygon(bboxExtent),
	)

	// MOS blobs are opaque proprietary binaries: the server has no geometry
	// functions over them. EGKO MOS tables nevertheless expose indexed raw
	// bounds (MINX/MAXX/MINY/MAXY), so use those as a coarse SQL filter and
	// keep the exact decoded-geometry check in TileFeatures.
	if layer.geometryFormat == GeometryFormatMOS {
		bboxSQL = mosBoundsSQL(layer, bboxExtent)
	}

	extent, _ := tile.Extent()
	pixelWidth := (extent.MaxX() - extent.MinX()) / 256
	pixelHeight := (extent.MaxY() - extent.MinY()) / 256
	scaleDenominator := pixelWidth / 0.00028

	var geomType string
	if layer.geomType != nil {
		geomType = geomTypeName(layer.geomType)
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

// mosBoundsSQL builds a coarse indexed filter for the raw bounds stored
// alongside MOS blobs. The bounds use the quantized MOS coordinate units,
// while bboxExtent is expressed in the layer CRS metres after applying
// mosUnitsFactor.
func mosBoundsSQL(layer *Layer, bboxExtent *geom.Extent) string {
	if bboxExtent == nil {
		return "1=1"
	}

	precisionScale := math.Pow(10, layer.mosPrecision)
	unitFactor := layer.mosUnitsFactor
	if math.IsNaN(precisionScale) || math.IsInf(precisionScale, 0) || precisionScale <= 0 ||
		math.IsNaN(unitFactor) || math.IsInf(unitFactor, 0) || unitFactor <= 0 {
		return "1=1"
	}

	rawScale := precisionScale / unitFactor
	if math.IsNaN(rawScale) || math.IsInf(rawScale, 0) || rawScale <= 0 {
		return "1=1"
	}

	minX := math.Floor(bboxExtent.MinX() * rawScale)
	maxX := math.Ceil(bboxExtent.MaxX() * rawScale)
	minY := math.Floor(bboxExtent.MinY() * rawScale)
	maxY := math.Ceil(bboxExtent.MaxY() * rawScale)
	for _, value := range []float64{minX, maxX, minY, maxY} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "1=1"
		}
	}

	format := func(value float64) string {
		return strconv.FormatFloat(value, 'f', 0, 64)
	}
	return fmt.Sprintf(
		"%sMINX <= %s AND %sMAXX >= %s AND %sMINY <= %s AND %sMAXY >= %s",
		"", format(maxX), "", format(minX),
		"", format(maxY), "", format(minY),
	)
}

// uppercaseTokens makes SQL tokens case-insensitive, matching PostGIS and GPKG.
func uppercaseTokens(str string) string {
	return provider.ParameterTokenRegexp.ReplaceAllStringFunc(str, strings.ToUpper)
}
