package gpkg

import (
	"strconv"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/config"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/provider"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
)

// sqliteQuoteIdent quotes a SQLite identifier, escaping embedded backticks
// so a crafted config value cannot break out of the quoted name. Defined
// separately from quoteIdent in gpkg.go because util.go is built without
// the cgo tag while gpkg.go is not.
func sqliteQuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// replaceTokens replaces tile and layer metadata tokens in a SQL query.
//
// bboxExtent must be the tile's buffered extent transformed to the layer's
// source SRID. Pixel dimensions and scale denominator are intentionally
// calculated from the tile's unbuffered Web Mercator extent, matching the
// PostGIS provider.
func replaceTokens(qtext string, layer *Layer, tile provider.Tile, bboxExtent *geom.Extent) string {
	// For custom-SQL native gpkg geometry layers the !BBOX! token expands
	// to the bounds predicate over the resolved bounds fields (source CRS,
	// no MOS scaling); when no fields were resolved the legacy unquoted
	// lowercase names are kept so hand-rolled fixtures still work.
	bboxPredicate := func() string {
		fields := layer.bboxFields
		if fields == (codec.BBoxFields{}) {
			fields = codec.BBoxFields{"minx", "maxx", "miny", "maxy"}
		}
		predicate, err := codec.BuildBoundsPredicate(
			fields, bboxExtent, codec.BoundsSourceCRS, layer.mosConfig, sqliteQuoteIdent,
		)
		if err != nil {
			log.Errorf("layer (%v): %v; spatial filter disabled", layer.name, err)
			return "1=1"
		}
		return predicate
	}
	if layer.tablename == "" && layer.geometryFormat == codec.FormatMOS {
		predicate, err := codec.BuildBoundsPredicate(
			layer.bboxFields, bboxExtent, codec.BoundsMOSRaw, layer.mosConfig, sqliteQuoteIdent,
		)
		if err != nil {
			log.Errorf("layer (%v): %v; spatial filter disabled", layer.name, err)
			predicate = "1=1"
		}
		bboxSQL := predicate
		bboxPredicate = func() string { return bboxSQL }
	}

	bboxSQL := bboxPredicate()

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

func trimTrailingSemicolon(sqlText string) string {
	sqlText = strings.TrimSpace(sqlText)
	return strings.TrimSpace(strings.TrimSuffix(sqlText, ";"))
}
