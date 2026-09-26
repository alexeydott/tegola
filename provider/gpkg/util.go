package gpkg

import (
	"strconv"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/config"
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

// sqliteReadOnlyDSN builds the sqlite3 DSN used to open GeoPackage files.
// The provider never writes to a GeoPackage, so the database is opened in
// read-only mode (audit P6-16); _busy_timeout keeps reads patient against a
// file locked by another process.
func sqliteReadOnlyDSN(path string) string {
	return "file:" + path + "?mode=ro&_busy_timeout=5000"
}

// replaceTokens replaces tile and layer metadata tokens in a SQL query.
//
// bboxExtent must be the tile's buffered extent transformed to the layer's
// source SRID. Pixel dimensions and scale denominator are intentionally
// calculated from the tile's unbuffered Web Mercator extent, matching the
// PostGIS provider.
//
// Bounds-backed custom SQL (!BBOX! expanding into the configured bounds
// fields predicate) is fail-closed (A12): a predicate build error is
// returned to the caller instead of silently substituting "1=1". The
// predicate is built lazily — only when the query actually carries the
// !BBOX!/!BOX! token.
func replaceTokens(qtext string, layer *Layer, tile provider.Tile, bboxExtent *geom.Extent) (string, error) {
	qtext = uppercaseTokens(qtext)

	// only build the bounds predicate when the query actually uses it;
	// build errors are fail-closed (A12).
	bboxSQL := ""
	if strings.Contains(qtext, config.BboxToken) || strings.Contains(qtext, "!BOX!") {
		var err error
		bboxSQL, err = buildBBoxPredicate(layer, bboxExtent)
		if err != nil {
			return "", err
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

	return tokenReplacer.Replace(qtext), nil
}

// buildBBoxPredicate builds the !BBOX! expansion for the layer. Custom MOS
// SQL filters over bounds columns quantized to raw MOS integers
// (BoundsMOSRaw); every other bounds-backed use (custom gpkg SQL, the
// native GeoPackage RTree path) filters in the source CRS over the resolved
// bounds fields (layer > provider > defaults MINX/MAXX/MINY/MAXY; SQLite
// identifiers are case-insensitive).
func buildBBoxPredicate(layer *Layer, bboxExtent *geom.Extent) (string, error) {
	fields := layer.bboxFields
	if fields == (codec.BBoxFields{}) {
		// registration resolves the names (layer > provider > defaults);
		// fall back to the defaults for bare layers.
		fields = codec.DefaultBBoxFields()
	}
	if layer.tablename == "" && layer.geometryFormat == codec.FormatMOS {
		return codec.BuildBoundsPredicate(fields, bboxExtent, codec.BoundsMOSRaw, layer.mosConfig, sqliteQuoteIdent)
	}
	return codec.BuildBoundsPredicate(fields, bboxExtent, codec.BoundsSourceCRS, layer.mosConfig, sqliteQuoteIdent)
}

// uppercaseTokens makes SQL tokens case-insensitive, matching PostGIS.
func uppercaseTokens(str string) string {
	return provider.ParameterTokenRegexp.ReplaceAllStringFunc(str, strings.ToUpper)
}

func trimTrailingSemicolon(sqlText string) string {
	sqlText = strings.TrimSpace(sqlText)
	return strings.TrimSpace(strings.TrimSuffix(sqlText, ";"))
}
