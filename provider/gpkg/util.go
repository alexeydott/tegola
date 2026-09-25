package gpkg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/config"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider"
)

// replaceTokens replaces tile and layer metadata tokens in a SQL query.
//
// bboxExtent must be the tile's buffered extent transformed to the layer's
// source SRID. Pixel dimensions and scale denominator are intentionally
// calculated from the tile's unbuffered Web Mercator extent, matching the
// PostGIS provider.
func replaceTokens(qtext string, layer *Layer, tile provider.Tile, bboxExtent *geom.Extent) string {
	bboxSQL := fmt.Sprintf(
		"minx <= %v AND maxx >= %v AND miny <= %v AND maxy >= %v",
		bboxExtent.MaxX(),
		bboxExtent.MinX(),
		bboxExtent.MaxY(),
		bboxExtent.MinY(),
	)

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

// uppercaseTokens makes SQL tokens case-insensitive, matching PostGIS.
func uppercaseTokens(str string) string {
	return provider.ParameterTokenRegexp.ReplaceAllStringFunc(str, strings.ToUpper)
}

// permissiveTokenReplacer replaces zoom comparisons with an all-zoom list
// and spatial filters with an always-true expression, for inspection queries
// built from custom SQL that must return rows regardless of the requested
// tile.
func permissiveTokenReplacer() *strings.Replacer {
	const allZoomsSQL = "IN (0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24)"
	return strings.NewReplacer(
		">= "+config.ZoomToken, allZoomsSQL,
		">="+config.ZoomToken, allZoomsSQL,
		"=> "+config.ZoomToken, allZoomsSQL,
		"=>"+config.ZoomToken, allZoomsSQL,
		"=< "+config.ZoomToken, allZoomsSQL,
		"=<"+config.ZoomToken, allZoomsSQL,
		"<= "+config.ZoomToken, allZoomsSQL,
		"<="+config.ZoomToken, allZoomsSQL,
		"!= "+config.ZoomToken, allZoomsSQL,
		"!="+config.ZoomToken, allZoomsSQL,
		"= "+config.ZoomToken, allZoomsSQL,
		"="+config.ZoomToken, allZoomsSQL,
		"> "+config.ZoomToken, allZoomsSQL,
		">"+config.ZoomToken, allZoomsSQL,
		"< "+config.ZoomToken, allZoomsSQL,
		"<"+config.ZoomToken, allZoomsSQL,
		config.BboxToken, "1=1",
		"!BOX!", "1=1",
		"!bbox!", "1=1",
	)
}

// buildDeferredInspectionSQL builds the runtime system-info pre-query for a
// tile-dependent custom-SQL layer (R6): zoom comparisons cover all zooms and
// the spatial filter is dropped so the sample window can reach MOS
// system-info blobs regardless of the requested tile, while tile position
// tokens are replaced with the current tile's values so the statement is
// valid SQL.
func buildDeferredInspectionSQL(layer *Layer, tile provider.Tile) string {
	qtext := permissiveTokenReplacer().Replace(trimTrailingSemicolon(uppercaseTokens(layer.sql)))
	inspectionExtent, _ := tile.BufferedExtent()
	return replaceTokens(qtext, layer, tile, inspectionExtent)
}

func trimTrailingSemicolon(sqlText string) string {
	sqlText = strings.TrimSpace(sqlText)
	return strings.TrimSpace(strings.TrimSuffix(sqlText, ";"))
}
