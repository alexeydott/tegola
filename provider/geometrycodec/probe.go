// probe.go implements the shared registration-time custom-SQL probe
// machinery used by the standard storage providers (gpkg, mysql, postgis,
// hana): the documented token-neutralization order for the permissive
// inspection query, the sample wrapping, the per-row decode closures, and
// the fail-closed structural contract validation.
//
// Probe contract: the probe always executes the SQL without a spatial
// filter (!BBOX!/!BOX! neutralized to 1=1) and with permissive
// position/zoom placeholders so tile-dependent SQL still returns its rows.
// Every provider prepares the probe query through PrepareProbeSQL in ONE
// documented order and feeds InspectSQLGeometryContract from real scanned
// row values via a next() iterator.
package geometrycodec

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/config"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/mos"
)

// Web Mercator (EPSG:3857) half-extent in metres. The probe reference tile
// is z=0/x=0/y=0, i.e. the full extent (same formula the runtime token
// replacement uses at a tile, see e.g. provider/*/util.go replaceTokens).
const probeWebMercatorMax = 20037508.342789244

// z0/0/0 Web Mercator reference values for the scale/pixel tokens. Computed
// exactly like the runtime token replacement (pixel width over 256px tiles,
// 0.28mm pixel size) so probe SQL matches tile-time semantics.
var (
	probePixelWidth       = (2 * probeWebMercatorMax) / 256
	probePixelHeight      = probePixelWidth
	probeScaleDenominator = probePixelWidth / 0.00028
)

// probePermissivePredicate is the always-true predicate tile/zoom/BBOX
// references collapse to during probe neutralization.
const probePermissivePredicate = "1=1"

// probeAllZooms is the permissive replacement for zoom-level comparisons
// ("x >= !ZOOM!" becomes "x IN (0,...,24)").
const probeAllZooms = "IN (0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24)"

// Comparison-level neutralization (audit P6-9). A comparison whose side
// operand is a tile/zoom token must collapse to the permissive predicate
// no matter which side of the operator the token is on: the token-left form
// ("!ZOOM! >= 5") used to fall through to the bare "0" substitution below
// ("0 >= 5") and silently sampled zero rows. The operand is matched as an
// atom plus any trailing operator/keyword continuation (arithmetic,
// BETWEEN, LIKE, IN, IS NULL...) so the whole predicate is consumed and
// never cut apart; shapes outside this grammar (e.g. function-call
// operands) fall back to the safe bare-token substitution below, which is
// exactly the pre-P6-9 SQL.
const (
	// probeCompareOp matches the comparison operators; two-character
	// forms are matched first so they are not cut in half.
	probeCompareOp = `(?:>=|=>|=<|<=|!=|<>|=|>|<)`
	// probeOperandAtom matches one operand: string/number literals,
	// one-level parenthesized groups, identifiers (optionally schema- or
	// table-qualified), or another token.
	probeOperandAtom = `(?:'[^']*'|"[^"]*"|` +
		`[-+]?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|` +
		`\([^()]*\)|` +
		`[A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)?|` +
		`![A-Z0-9_]+!)`
	// probeExprContinue consumes operator/keyword continuations that must
	// not dangle after the neutralized comparison ("cx = !X! + 1",
	// "!X! = y BETWEEN 1 AND 2", "!X! = y IS NULL"...).
	probeExprContinue = `(?:(?:\s*(?:[-+*/]|::|\|\|)\s*` + probeOperandAtom + `)|` +
		`(?:\s+(?i:IS)\s+(?i:NOT\s+)?(?:(?i:NULL)|(?i:TRUE)|(?i:FALSE)|(?i:UNKNOWN)|(?i:DISTINCT\s+FROM)\s+` + probeOperandAtom + `))|` +
		`(?:\s+(?i:NOT\s+)?(?i:BETWEEN)\s+(?:(?i:SYMMETRIC)|(?i:ASYMMETRIC))?\s*` + probeOperandAtom + `\s+(?i:AND)\s+` + probeOperandAtom + `)|` +
		`(?:\s+(?i:NOT\s+)?(?i:LIKE|ILIKE)\s+` + probeOperandAtom + `)|` +
		`(?:\s+(?i:NOT\s+)?(?i:SIMILAR)\s+(?i:TO)\s+` + probeOperandAtom + `)|` +
		`(?:\s+(?i:NOT\s+)?(?i:IN)\s*` + probeOperandAtom + `))*`
	// probeCompareTokenLeft covers every tile/zoom token in token-first
	// comparisons; the token-last forms below exclude !ZOOM! so right-side
	// zoom comparisons keep the richer full-range substitution.
	probeCompareTokenLeft  = `!(?:ZOOM|Z|X|Y)!`
	probeCompareTokenRight = `!(?:Z|X|Y)!`
)

// probeTokenLeftCompareRegexp matches token-first comparisons
// ("!ZOOM! >= 5", "!X! = !Y!"); probeTokenRightCompareRegexp matches
// token-last ones ("cx = !X! + 1", "5 <= !X!"). The leading boundary
// character is captured in group 1 and re-emitted with the replacement so
// the surrounding SQL is never cut apart; the right-side form deliberately
// excludes "(" from its boundary class so an expression like "f(x) >= !X!"
// is never truncated (it falls back to the safe bare-token substitution).
var (
	probeTokenLeftCompareRegexp  = regexp.MustCompile(`(^|[\s(,;])` + probeCompareTokenLeft + `\s*` + probeCompareOp + `\s*` + probeOperandAtom + probeExprContinue)
	probeTokenRightCompareRegexp = regexp.MustCompile(`(^|[\s,;])` + probeOperandAtom + probeExprContinue + `\s*` + probeCompareOp + `\s*` + probeCompareTokenRight + probeExprContinue)
)

// probeZoomCompareRegexp matches zoom-level comparisons with the token on
// the right ("z >= !ZOOM!" / "z>=!zoom!" / "z <= !zoom!"). The operator is
// swallowed together with the token so the comparison collapses onto the
// permissive IN list; any operator/keyword continuation is swallowed along
// with it so nothing dangles ("z = !ZOOM! + 1" must not become
// "z IN (0,...) + 1"). Token-first forms are handled by
// probeTokenLeftCompareRegexp above.
var probeZoomCompareRegexp = regexp.MustCompile(probeCompareOp + `\s*` + `(?i)!ZOOM!` + probeExprContinue)

// probeKnownTokens are the token spellings PrepareProbeSQL normalizes to
// their canonical uppercase form before substitution (the same token
// surface the providers' runtime replaceTokens understands).
var probeKnownTokens = []string{
	config.BboxToken, "!BOX!",
	config.ZoomToken, config.ZToken, config.XToken, config.YToken,
	config.ScaleDenominatorToken, config.PixelWidthToken, config.PixelHeightToken,
	config.IdFieldToken, config.GeomFieldToken, config.GeomTypeToken,
}

// PrepareProbeSQL neutralizes a custom-SQL query for the registration-time
// probe. The probe always executes the SQL without a spatial filter and
// with permissive tile placeholders; the documented substitution order is:
//
//  1. known !WORD! tokens are normalized to their canonical uppercase
//     spelling (case-insensitive token surface);
//  2. comparisons referencing a tile/zoom token collapse to the permissive
//     "1=1" predicate regardless of which side of the operator the token
//     is on ("!ZOOM! >= 5", "cx = !X! + 1", "!X! = !Y!"...). Right-side
//     zoom comparisons ("min_zoom <= !ZOOM!") instead collapse to the
//     permissive IN (0,...,24) range;
//  3. !BBOX!/!BOX! expand to "1=1" — the probe never applies a spatial
//     filter;
//  4. remaining bare !ZOOM!/!Z!/!X!/!Y! expand to "0" (projection
//     positions such as "!X! AS tile_x", or operand shapes outside the
//     comparison grammar — e.g. function-call operands — where the numeric
//     fallback keeps the SQL valid exactly as before);
//  5. !SCALE_DENOMINATOR!/!PIXEL_WIDTH!/!PIXEL_HEIGHT! expand to the z=0
//     Web Mercator reference values (identical to the runtime expansion at
//     tile 0/0/0); !ID_FIELD!/!GEOM_FIELD!/!GEOM_TYPE! expand to the
//     provided names.
//
// Unknown !token! values (custom query parameters) are left untouched:
// they are resolved by the parameter layer at tile time.
func PrepareProbeSQL(customSQL, geomField, idField, geomType string) string {
	sql := normalizeProbeTokens(customSQL)
	// collapse tile/zoom comparisons to the permissive predicate no matter
	// which side of the operator the token is on (audit P6-9). The
	// token-left pass runs first so token-to-token comparisons collapse in
	// one step instead of being cut into bare substitutions.
	sql = probeTokenLeftCompareRegexp.ReplaceAllString(sql, "${1}"+probePermissivePredicate)
	sql = probeTokenRightCompareRegexp.ReplaceAllString(sql, "${1}"+probePermissivePredicate)
	sql = probeZoomCompareRegexp.ReplaceAllString(sql, probeAllZooms)
	// the result is embedded into wrapping inspection queries
	// ("SELECT ... FROM (%s) ..."), so a trailing semicolon must go
	sql = strings.NewReplacer(
		config.BboxToken, probePermissivePredicate,
		"!BOX!", probePermissivePredicate,
		config.ZoomToken, "0",
		config.ZToken, "0",
		config.XToken, "0",
		config.YToken, "0",
		config.ScaleDenominatorToken, strconv.FormatFloat(probeScaleDenominator, 'f', 8, 64),
		config.PixelWidthToken, strconv.FormatFloat(probePixelWidth, 'f', 8, 64),
		config.PixelHeightToken, strconv.FormatFloat(probePixelHeight, 'f', 8, 64),
		config.IdFieldToken, idField,
		config.GeomFieldToken, geomField,
		config.GeomTypeToken, geomType,
	).Replace(sql)
	return strings.TrimSuffix(strings.TrimSpace(sql), ";")
}

// normalizeProbeTokens rewrites the known tokens to their canonical
// uppercase spelling so the probe substitution is case-insensitive.
func normalizeProbeTokens(sql string) string {
	for _, tok := range probeKnownTokens {
		sql = replaceFold(sql, tok, tok)
	}
	return sql
}

// replaceFold replaces every case-insensitive occurrence of old with
// replacement in s.
func replaceFold(s, old, replacement string) string {
	if old == "" {
		return s
	}
	var b strings.Builder
	for {
		i := indexFold(s, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(replacement)
		s = s[i+len(old):]
	}
}

// indexFold returns the index of the first case-insensitive occurrence of
// sub in s, or -1.
func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// WrapProbeSQL wraps a prepared custom-SQL query in the standard probe
// sample window: at most InspectionSampleLimit rows.
func WrapProbeSQL(sql string) string {
	return fmt.Sprintf("SELECT * FROM (%s) AS __tegola_bounds_probe LIMIT %d",
		strings.TrimSuffix(strings.TrimSpace(sql), ";"), InspectionSampleLimit)
}

// WrapProbeSQLTopStyle wraps a prepared custom-SQL query in the TOP-style
// probe sample window used by HANA: at most InspectionSampleLimit rows.
func WrapProbeSQLTopStyle(sql string) string {
	return fmt.Sprintf("SELECT TOP %d * FROM (%s) AS __tegola_bounds_probe",
		InspectionSampleLimit, strings.TrimSuffix(strings.TrimSpace(sql), ";"))
}

// RowDecode decodes one raw result-column value into a geometry. isMOS
// reports whether the value carried a MOS blob (as opposed to a
// native/WKB/WKT value); an error means the row cannot be counted (the
// probe skips it, e.g. SystemInfo rows or malformed blobs).
type RowDecode func(value interface{}) (geom.Geometry, bool, error)

// probeValueBytes normalizes the raw driver representations of a blob
// column into bytes. Besides []byte/string it handles driver LOB values
// carrying a *bytes.Buffer (e.g. the HANA driver's driver.Lob, matched
// structurally through its Writer() accessors).
func probeValueBytes(v interface{}) ([]byte, bool) {
	switch val := v.(type) {
	case []byte:
		return val, true
	case string:
		return []byte(val), true
	case *bytes.Buffer:
		if val == nil {
			return nil, false
		}
		return val.Bytes(), true
	case interface{ Writer() io.Writer }:
		if w, ok := val.Writer().(*bytes.Buffer); ok && w != nil {
			return w.Bytes(), true
		}
	case interface{ Reader() io.Reader }:
		if r, ok := val.Reader().(*bytes.Buffer); ok && r != nil {
			return r.Bytes(), true
		}
	}
	return nil, false
}

// decodeMOSValue decodes v as a MOS blob. The blob must first carry a
// positive MOS signature (the MOS header must validate as MOS) and then
// decode as a complete MOS geometry; SystemInfo/layerinfo rows are never
// applied, they are reported as errors so the probe skips them.
func decodeMOSValue(v interface{}, cfg MOSConfig) (geom.Geometry, error) {
	if IsSystemInfoValue(v) {
		return nil, fmt.Errorf("system info row skipped")
	}
	b, ok := probeValueBytes(v)
	if !ok {
		return nil, fmt.Errorf("unexpected geometry column type %T, expected MOS blob", v)
	}
	// Positive MOS signature required: a native/WKB/WKT value must never
	// count as a MOS row.
	if _, err := mos.DecodeHeader(b); err != nil {
		return nil, fmt.Errorf("no MOS signature: %v", err)
	}
	return DecodeMOS(v, cfg)
}

// MOSRowDecode returns the probe row decoder for an explicit MOS layer:
// rows count as MOS only when they carry a valid MOS signature and decode
// as MOS geometries.
func MOSRowDecode(cfg MOSConfig) RowDecode {
	return func(value interface{}) (geom.Geometry, bool, error) {
		g, err := decodeMOSValue(value, cfg)
		if err != nil {
			return nil, false, err
		}
		return g, true, nil
	}
}

// AutoRowDecode returns the probe row decoder used by the storage-format
// inference: native values are tried first and count as non-MOS rows;
// otherwise a positive MOS signature plus a decodable MOS geometry counts
// as a MOS row. A native/WKB/WKT-decodable row never counts as MOS.
func AutoRowDecode(native func(interface{}) (geom.Geometry, error), cfg MOSConfig) RowDecode {
	return func(value interface{}) (geom.Geometry, bool, error) {
		if IsSystemInfoValue(value) {
			return nil, false, fmt.Errorf("system info row skipped")
		}
		if native != nil {
			if g, err := native(value); err == nil && g != nil {
				return g, false, nil
			}
		}
		g, err := decodeMOSValue(value, cfg)
		if err != nil {
			return nil, false, err
		}
		return g, true, nil
	}
}

// ErrSQLGeometryContract is the fail-closed registration error raised when
// a bounds-backed custom SQL query violates the structural contract.
type ErrSQLGeometryContract struct {
	Layer  string
	Reason string
}

func (e ErrSQLGeometryContract) Error() string {
	return fmt.Sprintf("layer (%v) custom SQL geometry contract: %v", e.Layer, e.Reason)
}

// ValidateBoundsSQLContract validates the static and structural contract of
// a bounds-backed custom SQL query. It is the fail-closed registration
// gate: a missing !BBOX! token, a missing geometry column, or missing
// bounds columns are startup errors, never warnings.
//
//   - the SQL must contain a bbox token (!BBOX!, case-insensitive);
//   - when a geometry column is configured, the probe must have matched it
//     in the result columns;
//   - the probe must have found all four bounds columns.
//
// columnSummary is the comma-joined list of actual result column names, for
// error messages.
func ValidateBoundsSQLContract(layerName, sql, geometryField string, contract SQLGeometryContract) error {
	upper := strings.ToUpper(sql)
	if !strings.Contains(upper, strings.ToUpper(config.BboxToken)) &&
		!strings.Contains(upper, "!BOX!") {
		return ErrSQLGeometryContract{Layer: layerName,
			Reason: fmt.Sprintf("missing %v token; bounds-backed custom SQL must carry a bounds predicate", config.BboxToken)}
	}
	if geometryField != "" && contract.GeometryField == "" {
		return ErrSQLGeometryContract{Layer: layerName,
			Reason: fmt.Sprintf("geometry column %q not present in the result columns", geometryField)}
	}
	if !contract.HasBounds {
		return ErrSQLGeometryContract{Layer: layerName,
			Reason: fmt.Sprintf("bounds columns (%v) not all present in the result columns", contract.BoundsFields)}
	}
	return nil
}

// ValidateMOSSQLExplicitConfig enforces the CRS policy for MOS custom SQL:
// MOS carries no CRS metadata, so the layer configuration must state the
// source CRS explicitly (srid or crs_defn). mos_precision/mos_units stay
// optional with their paired defaults (DefaultMOSPrecisionForUnits /
// DefaultMOSConfig).
func ValidateMOSSQLExplicitConfig(layerName string, crsExplicit bool) error {
	if !crsExplicit {
		return fmt.Errorf("layer (%v): source CRS unresolved; MOS columns carry no CRS metadata, specify srid or crs_defn in the layer configuration", layerName)
	}
	return nil
}

// degreesCRS lists the common geographic (degrees) CRS codes whose units
// are not metric. This is the warn-only 1.3 policy for the Web-Mercator-
// metres scale tokens.
var degreesCRS = map[int]struct{}{
	4326: {}, 4269: {}, 4258: {}, 4490: {}, 4214: {}, 4674: {}, 4230: {}, 4267: {},
}

// crsDefnConfigKey mirrors crsconfig.KeyCRSDefn ("crs_defn"), referenced by
// literal to keep this package independent of crsconfig.
const crsDefnConfigKey = "crs_defn"

// WarnNonMetricScaleTokens warns (never errors) when custom SQL uses the
// scale-denominator / pixel-size tokens and the layer CRS is non-metric
// (degrees): those tokens are always computed as Web Mercator metres
// regardless of the layer CRS. Token values are left unchanged. The
// crs_defn text (layer over provider) is used as a hint for synthetic CRSs.
// Either cfg may be nil. Returns true when the warning was logged (for
// testability); callers may ignore the result.
func WarnNonMetricScaleTokens(layerName, sql string, srid uint32, providerCfg, layerCfg dict.Dicter) bool {
	upper := strings.ToUpper(sql)
	uses := strings.Contains(upper, strings.ToUpper(config.ScaleDenominatorToken)) ||
		strings.Contains(upper, strings.ToUpper(config.PixelWidthToken)) ||
		strings.Contains(upper, strings.ToUpper(config.PixelHeightToken))
	if !uses {
		return false
	}
	var crsDefn string
	for _, d := range []dict.Dicter{layerCfg, providerCfg} {
		if d == nil {
			continue
		}
		if v, err := d.String(crsDefnConfigKey, nil); err == nil {
			crsDefn = v
			break
		}
	}
	_, degrees := degreesCRS[int(srid)]
	if !degrees && basic.IsSyntheticSRID(uint64(srid)) {
		// synthetic CRS (crs_defn): warn only when the definition mentions
		// degrees, since its units are otherwise unknown.
		degrees = strings.Contains(strings.ToUpper(crsDefn), "DEGREE")
	}
	if !degrees {
		return false
	}
	log.Warnf("layer (%v): custom SQL uses %v/%v/%v tokens with a non-metric (degrees) CRS; "+
		"those tokens are always computed as Web Mercator metres regardless of the layer CRS",
		layerName, config.ScaleDenominatorToken, config.PixelWidthToken, config.PixelHeightToken)
	return true
}
