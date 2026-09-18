package mysql

import (
	"fmt"
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
	bboxSQL := fmt.Sprintf(
		"ST_Intersects(%v, ST_GeomFromText('%v'))",
		quoteIdentifier(layer.geomFieldname),
		wktPolygon(bboxExtent),
	)

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

// uppercaseTokens makes SQL tokens case-insensitive, matching PostGIS and GPKG.
func uppercaseTokens(str string) string {
	return provider.ParameterTokenRegexp.ReplaceAllStringFunc(str, strings.ToUpper)
}
