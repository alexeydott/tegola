// bounds.go implements the common bounds-backed SQL contract shared by all
// standard storage providers (gpkg, postgis, mysql, hana): configurable
// bounds-field names, the DB-independent !BBOX! predicate builder for raw
// bounds columns, and the registration-time SQL MapplGIS inspection probe
// that detects bounds-backed MOS custom SQL without a live MapplGIS table
// contract.
package geometrycodec

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/cmp"
	"github.com/go-spatial/tegola/dict"
)

// BBox field-name configuration keys. These name the result columns/aliases
// that carry the raw bounds of each feature for bounds-backed MOS SQL.
const (
	ConfigKeyBBoxMinXField = "bbox_minx_fieldname"
	ConfigKeyBBoxMaxXField = "bbox_maxx_fieldname"
	ConfigKeyBBoxMinYField = "bbox_miny_fieldname"
	ConfigKeyBBoxMaxYField = "bbox_maxy_fieldname"
)

// Default bounds field names, matching the MapplGIS EGKO convention.
const (
	BBoxMinXDefault = "MINX"
	BBoxMaxXDefault = "MAXX"
	BBoxMinYDefault = "MINY"
	BBoxMaxYDefault = "MAXY"
)

// MapplGISSource distinguishes how a layer was detected as MapplGIS. Table
// canonical detection guarantees SystemInfo; SQL sample detection does not.
type MapplGISSource int

const (
	MapplGISNone MapplGISSource = iota
	// MapplGISTableCanonical: DDL + PK + indexes + OKEY=1 SystemInfo probe.
	MapplGISTableCanonical
	// MapplGISSQLSample: bounds columns + geometry + >=3 valid MOS rows.
	MapplGISSQLSample
)

// String returns a human-readable source name for logs and errors.
func (s MapplGISSource) String() string {
	switch s {
	case MapplGISTableCanonical:
		return "table-canonical"
	case MapplGISSQLSample:
		return "sql-sample"
	}
	return "none"
}

// DefaultBBoxFields returns the default bounds field names as a BBoxFields
// value (MINX, MAXX, MINY, MAXY), handy for constructing test layers.
func DefaultBBoxFields() BBoxFields {
	return BBoxFields{BBoxMinXDefault, BBoxMaxXDefault, BBoxMinYDefault, BBoxMaxYDefault}
}

// BBoxFields holds the resolved bounds field names for a layer, following
// the layer > provider > defaults precedence of every other common key.
type BBoxFields [4]string

// Field index constants for BBoxFields.
const (
	BBoxMinXIdx = iota
	BBoxMaxXIdx
	BBoxMinYIdx
	BBoxMaxYIdx
)

// ResolveBBoxFields resolves the four bounds field names from the provider
// and layer configs. Resolution is per-field: a layer-level value overrides
// the provider-level value for that field only, so partial overrides are
// supported. Unset at both levels falls back to the documented defaults.
// layer may be nil; layerName is used for error context.
func ResolveBBoxFields(provider, layer dict.Dicter, layerName string) (BBoxFields, error) {
	keys := []struct {
		key string
		def string
		idx int
	}{
		{ConfigKeyBBoxMinXField, BBoxMinXDefault, BBoxMinXIdx},
		{ConfigKeyBBoxMaxXField, BBoxMaxXDefault, BBoxMaxXIdx},
		{ConfigKeyBBoxMinYField, BBoxMinYDefault, BBoxMinYIdx},
		{ConfigKeyBBoxMaxYField, BBoxMaxYDefault, BBoxMaxYIdx},
	}

	var fields BBoxFields
	for _, k := range keys {
		fields[k.idx] = k.def

		if provider != nil {
			if _, explicit := provider.Interface(k.key); explicit {
				v, err := provider.String(k.key, nil)
				if err != nil {
					return fields, fmt.Errorf("for layer (%v) invalid %v: %v", layerName, k.key, err)
				}
				if strings.TrimSpace(v) == "" {
					return fields, fmt.Errorf("for layer (%v) invalid %v: empty value; provide a non-empty column name or omit the key to use the default %v", layerName, k.key, k.def)
				}
				fields[k.idx] = v
			}
		}
		if layer != nil {
			if _, explicit := layer.Interface(k.key); explicit {
				v, err := layer.String(k.key, nil)
				if err != nil {
					return fields, fmt.Errorf("for layer (%v) invalid %v: %v", layerName, k.key, err)
				}
				if strings.TrimSpace(v) == "" {
					return fields, fmt.Errorf("for layer (%v) invalid %v: empty value; provide a non-empty column name or omit the key to use the default %v", layerName, k.key, k.def)
				}
				fields[k.idx] = v
			}
		}
	}
	return fields, nil
}

// IsBBoxField reports whether name matches any resolved bounds field name
// (case-insensitive, mirroring SQL column-name semantics). Providers use it
// to keep operational bounds columns out of feature tags.
func (f BBoxFields) IsBBoxField(name string) bool {
	for _, field := range f {
		if strings.EqualFold(strings.TrimSpace(name), field) {
			return true
		}
	}
	return false
}

// BoundsPredicateMode selects how the source extent is translated into the
// bounds-column predicate.
type BoundsPredicateMode int

const (
	// BoundsSourceCRS compares the bounds columns against the source extent
	// expressed in the layer CRS coordinates (GPKG custom `gpkg` binary).
	BoundsSourceCRS BoundsPredicateMode = iota
	// BoundsMOSRaw first scales the source extent from layer CRS units into
	// quantized MOS units (10^precision / unit factor) before comparison.
	BoundsMOSRaw
)

// BuildBoundsPredicate builds the DB-independent bounds overlap predicate
// used to replace !BBOX! for bounds-backed custom SQL:
//
//	<maxx_field> >= tile_minx AND <minx_field> <= tile_maxx
//	AND <maxy_field> >= tile_miny AND <miny_field> <= tile_maxy
//
// mode=BoundsMOSRaw scales the tile extent into quantized MOS units first
// (floor/ceil rounding, mirroring the MySQL/GPKG raw bounds filters). An
// invalid scale (non-finite, non-positive) is an error, never a silent 1=1.
// quote wraps each field name; pass a no-op wrapper for unquoted names.
func BuildBoundsPredicate(fields BBoxFields, extent *geom.Extent, mode BoundsPredicateMode, mosConfig MOSConfig, quote func(string) string) (string, error) {
	if extent == nil {
		return "", fmt.Errorf("bounds predicate: nil extent")
	}
	if quote == nil {
		quote = func(s string) string { return s }
	}

	minX, maxX, minY, maxY := extent.MinX(), extent.MaxX(), extent.MinY(), extent.MaxY()
	if mode == BoundsMOSRaw {
		precisionScale := math.Pow(10, mosConfig.Precision)
		unitFactor := mosConfig.UnitFactor
		if math.IsNaN(precisionScale) || math.IsInf(precisionScale, 0) || precisionScale <= 0 ||
			math.IsNaN(unitFactor) || math.IsInf(unitFactor, 0) || unitFactor <= 0 {
			return "", fmt.Errorf("bounds predicate: invalid MOS quantization (precision=%v, unit factor=%v)", mosConfig.Precision, mosConfig.UnitFactor)
		}
		rawScale := precisionScale / unitFactor
		if math.IsNaN(rawScale) || math.IsInf(rawScale, 0) || rawScale <= 0 {
			return "", fmt.Errorf("bounds predicate: invalid MOS raw scale %v", rawScale)
		}
		minX, maxX = math.Floor(minX*rawScale), math.Ceil(maxX*rawScale)
		minY, maxY = math.Floor(minY*rawScale), math.Ceil(maxY*rawScale)
	}

	values := []float64{minX, maxX, minY, maxY}
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("bounds predicate: non-finite tile bound")
		}
	}

	format := func(v float64) string {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprintf(
		"%v >= %v AND %v <= %v AND %v >= %v AND %v <= %v",
		quote(fields[BBoxMaxXIdx]), format(minX),
		quote(fields[BBoxMinXIdx]), format(maxX),
		quote(fields[BBoxMaxYIdx]), format(minY),
		quote(fields[BBoxMinYIdx]), format(maxY),
	), nil
}

// SQLGeometryContract is the outcome of the registration-time
// InspectSQLGeometryContract probe for a bounds-backed MOS custom-SQL layer.
type SQLGeometryContract struct {
	// BoundsFields are the actual (database-reported) bounds column names
	// discovered in the sample, resolved case-insensitively against the
	// configured names. Empty when no bounds columns were found.
	BoundsFields BBoxFields
	// ValidMOSRows counts sample rows carrying a decodable MOS geometry.
	ValidMOSRows int
	// HasBounds reports whether all four bounds columns were found.
	HasBounds bool
}

// MinValidMOSRows is the minimum number of decodable MOS rows the SQL sample
// probe requires before a custom-SQL layer is treated as bounds-backed
// MapplGIS SQL.
const MinValidMOSRows = 3

// InspectSQLGeometryContract runs a registration-time probe over a sample of
// the layer's custom SQL result set and reports the bounds-backed MOS
// contract: presence of the four bounds columns and at least MinValidMOSRows
// decodable MOS geometries carrying at least one coordinate point.
// sampleQuery must return the bounds fields plus the geometry column and be
// already token-expanded (all-zoom / no spatial filter). decode decodes a
// raw geometry value (returning an error for non-decodable values); callers
// pass their provider's decodeGeometryValue. findBoundsColumns maps the
// sample's column names to the [4]string bounds order (minx, maxx, miny,
// maxy); nil when they are absent.
//
// The probe reads at most InspectionSampleLimit rows (with an early stop
// once enough valid MOS rows are counted), verifies that the geometry field
// is present in the result set, and is deliberately provider-independent: it
// only requires a rows-like iterator, so each provider feeds it from its
// own driver.
func InspectSQLGeometryContract(
	rows func(scan func(dest ...interface{}) error) (bool, error),
	columnNames []string,
	geometryField string,
	bboxFields BBoxFields,
	decode func(value interface{}) (geom.Geometry, error),
) (SQLGeometryContract, error) {
	var contract SQLGeometryContract

	boundsIdx, boundsCols := findBoundsColumnsIn(columnNames, bboxFields)
	if boundsIdx != nil {
		contract.BoundsFields = *boundsCols
		contract.HasBounds = true
	}

	// geometry column index; -1 when the geometry field is absent from the
	// result set (a bounds-backed contract cannot be verified without it).
	geomIdx := -1
	if geometryField == "" {
		// geometry assumed to be the last selected column
		geomIdx = len(columnNames) - 1
	} else {
		for i, name := range columnNames {
			if strings.EqualFold(name, geometryField) {
				geomIdx = i
				break
			}
		}
	}

	for {
		more, err := rows(func(dest ...interface{}) error {
			if geomIdx < 0 {
				return nil
			}
			geomValue := dest[geomIdx]
			// driver-backed iterators scan into *interface{} placeholders;
			// unwrap so decode receives the actual value, mirroring the
			// value-per-row contract of the unit-test iterators.
			if p, ok := geomValue.(*interface{}); ok {
				geomValue = *p
			}
			if geomValue == nil {
				return nil
			}
			geo, derr := decode(geomValue)
			if derr != nil || geo == nil {
				return nil
			}
			if !hasGeometryCoordinates(geo) {
				return nil
			}
			contract.ValidMOSRows++
			return nil
		})
		if err != nil {
			return contract, err
		}
		if !more {
			break
		}
		if contract.ValidMOSRows >= MinValidMOSRows {
			// enough evidence for the bounds-backed contract; stop early so
			// the sample window stays bounded even for huge result sets
			break
		}
	}
	return contract, nil
}

// hasGeometryCoordinates reports whether the geometry carries at least one
// coordinate point. Empty geometries (e.g. an empty line string) do not
// satisfy the probe contract. Unknown geometry types are treated as
// carrying coordinates so the probe stays permissive for future types.
func hasGeometryCoordinates(geo geom.Geometry) bool {
	if geo == nil || geom.IsNil(geo) {
		return false
	}
	if cmp.IsEmptyGeo(geo) {
		return false
	}
	return true
}

// findBoundsColumnsIn maps the sample's column names case-insensitively onto
// the configured bounds field names and returns the resolved [4]string in
// minx/maxx/miny/maxy order plus their sample column indices. Returns nil
// indices when any of the four bounds fields is missing from the columns.
func findBoundsColumnsIn(columns []string, bboxFields BBoxFields) (*[4]int, *BBoxFields) {
	lookup := make(map[string]int, len(columns))
	for i, name := range columns {
		lookup[strings.ToLower(strings.TrimSpace(name))] = i
	}
	var idx [4]int
	var resolved BBoxFields
	for i, field := range bboxFields {
		j, ok := lookup[strings.ToLower(strings.TrimSpace(field))]
		if !ok {
			return nil, nil
		}
		idx[i] = j
		resolved[i] = columns[j]
	}
	return &idx, &resolved
}
