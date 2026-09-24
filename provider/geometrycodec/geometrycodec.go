// Package geometrycodec implements the provider-independent geometry format
// contract shared by all standard storage providers (gpkg, postgis, mysql,
// hana). It hosts the common configuration parsing for `geometry_format`,
// `mos_precision` and `mos_units`, the MOS decode adapter, MapplGIS LayerInfo
// (layer self-description) inspection/application and the exact in-memory
// geometry-vs-extent filter used when a spatial predicate cannot be pushed
// into SQL.
//
// MVT passthrough providers (mvt_postgis, mvt_hana) do not decode raw
// geometry and are intentionally outside this contract.
package geometrycodec

import (
	"fmt"
	"math"
	"strings"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/geom/encoding/wkt"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/mos"
)

// Common configuration keys. Providers may expose additional
// provider-specific format values (e.g. "mysql", "mariadb", "auto") but MUST
// support the common ones below.
const (
	ConfigKeyGeometryFormat = "geometry_format"
	ConfigKeyMOSPrecision   = "mos_precision"
	ConfigKeyMOSUnits       = "mos_units"
	ConfigKeyGeometryType   = "geometry_type"
	ConfigKeyGeomField      = "geometry_fieldname"
	ConfigKeyIDField        = "id_fieldname"
)

// Common geometry format values understood by every standard provider.
const (
	// FormatMOS selects the opaque MapplGIS MOS binary blob format.
	FormatMOS = "mos"
	// FormatWKB selects plain OGC WKB (e.g. selected via ST_AsBinary).
	FormatWKB = "wkb"
	// FormatWKT selects WKT text (e.g. selected via ST_AsText).
	FormatWKT = "wkt"
)

// IsRawFormat reports whether the given geometry format value selects a raw
// (non-native) geometry representation that the provider reads directly from
// the database without database-side conversion helpers (e.g. ST_AsBinary).
// Both WKB and WKT columns are raw formats, as is MOS. Raw formats share a
// common behavior across providers: the geometry column cannot be used in
// server-side geometry predicates (hence "IS NOT NULL" SQL guards and
// in-memory bbox filtering) and is excluded from MVT providers.
func IsRawFormat(format string) bool {
	switch format {
	case FormatMOS, FormatWKB, FormatWKT:
		return true
	}
	return false
}

// ValidateMVTGeometryFormat rejects raw geometry formats on MVT passthrough
// providers (mvt_postgis, mvt_hana): their geometry is MVT bytes produced by
// the database, not a raw feature geometry that can be decoded client-side.
// An empty format means the provider-native default and is always valid.
func ValidateMVTGeometryFormat(format string) error {
	if IsRawFormat(format) {
		return fmt.Errorf(
			"%v = %q is not supported for MVT providers; use a standard provider (postgis/hana) instead",
			ConfigKeyGeometryFormat, format,
		)
	}
	return nil
}

// Default MOS quantization settings: integer units with no offset.
const (
	MOSPrecisionDefault   = 0.0
	MOSUnitsFactorDefault = 1.0
)

// MaxMOSPrecision bounds the decimal digit count a float64 can represent
// exactly in MOS dequantization.
const MaxMOSPrecision = 308

// ValidateMOSPrecision reports whether p is a usable decimal digit count for
// MOS quantized coordinates.
func ValidateMOSPrecision(p float64) error {
	if math.IsNaN(p) || math.IsInf(p, 0) ||
		p < 0 || math.Trunc(p) != p ||
		p > MaxMOSPrecision {
		return fmt.Errorf("must be a finite non-negative integer no greater than %d, got %v", MaxMOSPrecision, p)
	}
	return nil
}

// MOSConfig holds the resolved MOS quantization settings for a layer together
// with flags marking which values were set explicitly. Explicit settings
// always win over MapplGIS LayerInfo self-description.
type MOSConfig struct {
	// Precision is the number of decimal digits the quantized integer
	// coordinates carry (mos_precision config or MapplGIS LayerInfo precision).
	Precision float64
	// UnitFactor scales the dequantized coordinates from the configured map
	// units to metres of the projected CRS (mos_units config or
	// MapplGIS LayerInfo map units); defaults to 1.
	UnitFactor float64
	// PrecisionSet marks an explicit provider- or layer-level mos_precision.
	PrecisionSet bool
	// UnitsSet marks an explicit provider- or layer-level mos_units.
	UnitsSet bool
}

// DefaultMOSConfig returns the default MOS quantization configuration.
func DefaultMOSConfig() MOSConfig {
	return MOSConfig{
		Precision:  MOSPrecisionDefault,
		UnitFactor: MOSUnitsFactorDefault,
	}
}

// ResolveMOSConfig parses mos_precision and mos_units from the provider and
// layer configuration dictionaries. Layer-level values override provider
// level values. A nil layer dict is allowed. Errors are prefixed with the
// layer name when one is provided.
func ResolveMOSConfig(provider, layer dict.Dicter, layerName string) (MOSConfig, error) {
	cfg := DefaultMOSConfig()

	prefix := ""
	if layerName != "" {
		prefix = fmt.Sprintf("for layer (%v) ", layerName)
	}

	precision, precisionSet, err := resolveMOSPrecision(provider, prefix)
	if err != nil {
		return cfg, err
	}
	cfg.Precision, cfg.PrecisionSet = precision, precisionSet

	unitsFactor, unitsSet, err := resolveMOSUnits(provider, prefix)
	if err != nil {
		return cfg, err
	}
	cfg.UnitFactor, cfg.UnitsSet = unitsFactor, unitsSet

	if layer == nil {
		return cfg, nil
	}

	precision, precisionSet, err = resolveMOSPrecision(layer, prefix)
	if err != nil {
		return cfg, err
	}
	if precisionSet {
		cfg.Precision, cfg.PrecisionSet = precision, true
	}

	unitsFactor, unitsSet, err = resolveMOSUnits(layer, prefix)
	if err != nil {
		return cfg, err
	}
	if unitsSet {
		cfg.UnitFactor, cfg.UnitsSet = unitsFactor, true
	}

	return cfg, nil
}

func resolveMOSPrecision(cfg dict.Dicter, errPrefix string) (float64, bool, error) {
	if cfg == nil {
		return MOSPrecisionDefault, false, nil
	}
	if _, explicit := cfg.Interface(ConfigKeyMOSPrecision); !explicit {
		return MOSPrecisionDefault, false, nil
	}
	value := MOSPrecisionDefault
	v, err := cfg.Float(ConfigKeyMOSPrecision, &value)
	if err != nil {
		return 0, false, fmt.Errorf("%sinvalid %v: %v", errPrefix, ConfigKeyMOSPrecision, err)
	}
	if err := ValidateMOSPrecision(v); err != nil {
		return 0, false, fmt.Errorf("%sinvalid %v: %v", errPrefix, ConfigKeyMOSPrecision, err)
	}
	return v, true, nil
}

func resolveMOSUnits(cfg dict.Dicter, errPrefix string) (float64, bool, error) {
	if cfg == nil {
		return MOSUnitsFactorDefault, false, nil
	}
	def := ""
	name, err := cfg.String(ConfigKeyMOSUnits, &def)
	if err != nil {
		return 0, false, fmt.Errorf("%sinvalid %v: %v", errPrefix, ConfigKeyMOSUnits, err)
	}
	if strings.TrimSpace(name) == "" {
		return MOSUnitsFactorDefault, false, nil
	}
	units, uerr := mos.ParseMapUnits(name)
	if uerr != nil {
		return 0, false, fmt.Errorf("%sinvalid %v: %v", errPrefix, ConfigKeyMOSUnits, uerr)
	}
	factor, uerr := units.ToMetres()
	if uerr != nil {
		return 0, false, fmt.Errorf("%sinvalid %v: %v", errPrefix, ConfigKeyMOSUnits, uerr)
	}
	return factor, true, nil
}

// ApplySystemInfo fills in the unset values from a parsed
// MapplGIS LayerInfo layer self-description. Explicit configuration always
// wins. The receiver is updated in place so both startup registration and
// per-tile runtime application share one implementation. nil is a no-op.
func (c *MOSConfig) ApplySystemInfo(si *mos.SystemInfo) error {
	if si == nil {
		return nil
	}
	if err := ValidateMOSPrecision(float64(si.Precision)); err != nil {
		return fmt.Errorf("invalid system-info MOS precision: %v", err)
	}
	if !c.PrecisionSet {
		c.Precision = float64(si.Precision)
	}
	if !c.UnitsSet && si.MapUnitsDefined {
		factor, err := si.ScaleToMetres()
		if err != nil {
			return fmt.Errorf("unable to convert MOS map units %v to metres: %v", si.MapUnits, err)
		}
		c.UnitFactor = factor
	}
	return nil
}

// Options builds the low-level mos decode options for this configuration.
func (c MOSConfig) Options() mos.Options {
	return mos.Options{
		Precision:  c.Precision,
		UnitFactor: c.UnitFactor,
	}
}

// blobValue normalizes driver values that may arrive as []byte or string.
func blobValue(v interface{}) ([]byte, bool) {
	switch val := v.(type) {
	case []byte:
		return val, true
	case string:
		return []byte(val), true
	}
	return nil, false
}

// IsSystemInfoValue reports whether v looks like a MapplGIS LayerInfo
// version wrapper blob rather than a regular MOS geometry.
func IsSystemInfoValue(v interface{}) bool {
	b, ok := blobValue(v)
	if !ok {
		return false
	}
	return mos.IsSystemInfoBlob(b)
}

// ParseSystemInfoValue parses v as a MapplGIS LayerInfo blob.
func ParseSystemInfoValue(v interface{}) (mos.SystemInfo, error) {
	b, ok := blobValue(v)
	if !ok {
		return mos.SystemInfo{}, fmt.Errorf("unexpected system info column type %T, expected blob", v)
	}
	return mos.ParseSystemInfo(b)
}

// DecodeMOS decodes a MOS blob using the resolved quantization settings.
// MOS carries no SRID, so callers must apply their configured CRS.
func DecodeMOS(v interface{}, cfg MOSConfig) (geom.Geometry, error) {
	b, ok := blobValue(v)
	if !ok {
		return nil, fmt.Errorf("unexpected MOS geometry column type %T, expected blob", v)
	}
	g, err := mos.Decode(b, cfg.Options())
	if err != nil {
		return nil, fmt.Errorf("error decoding MOS geometry: %v", err)
	}
	return g, nil
}

// DecodeWKB decodes a plain OGC WKB value ([]byte or string).
func DecodeWKB(v interface{}) (geom.Geometry, error) {
	b, ok := blobValue(v)
	if !ok {
		return nil, fmt.Errorf("unexpected geometry column type %T, expected blob", v)
	}
	g, err := wkb.DecodeBytes(b)
	if err != nil {
		// %w keeps wkb.ErrUnknownGeometryType intact so providers can
		// detect it with errors.As and skip unsupported 3D geometries.
		return nil, fmt.Errorf("error decoding WKB geometry: %w", err)
	}
	return g, nil
}

// DecodeWKT decodes a WKT text value (string or []byte).
func DecodeWKT(v interface{}) (geom.Geometry, error) {
	switch s := v.(type) {
	case string:
		g, err := wkt.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("error decoding WKT geometry: %v", err)
		}
		return g, nil
	case []byte:
		g, err := wkt.DecodeBytes(s)
		if err != nil {
			return nil, fmt.Errorf("error decoding WKT geometry: %v", err)
		}
		return g, nil
	default:
		return nil, fmt.Errorf("unexpected geometry column type %T, expected text", v)
	}
}

// GeometryIntersectsExtent reports whether a geometry's bounding box
// intersects the given extent. It backs the exact in-memory spatial filter
// for formats whose blobs cannot be filtered by the database (e.g. MOS).
// Undecodable bounds keep the geometry (conservative).
func GeometryIntersectsExtent(g geom.Geometry, e *geom.Extent) bool {
	if g == nil || e == nil {
		return true
	}
	gb, err := geom.NewExtentFromGeometry(g)
	if err != nil || gb == nil {
		return true
	}
	// geom.Extent.Intersect rejects zero-width/height extents. That is
	// correct for area intersections but drops valid Point and degenerate
	// geometry features before they reach MVT encoding. Bounds overlap is
	// inclusive here because touching the tile boundary still intersects it.
	return gb.MinX() <= e.MaxX() &&
		gb.MaxX() >= e.MinX() &&
		gb.MinY() <= e.MaxY() &&
		gb.MaxY() >= e.MinY()
}

// GeomTypeName returns the OGC-style name ("POINT", "MULTIPOLYGON", ...)
// of a decoded geometry, used for the !GEOM_TYPE! token.
func GeomTypeName(g geom.Geometry) string {
	switch g.(type) {
	case geom.Point:
		return "POINT"
	case geom.MultiPoint:
		return "MULTIPOINT"
	case geom.LineString:
		return "LINESTRING"
	case geom.MultiLineString:
		return "MULTILINESTRING"
	case geom.Polygon:
		return "POLYGON"
	case geom.MultiPolygon:
		return "MULTIPOLYGON"
	case geom.Collection:
		return "GEOMETRYCOLLECTION"
	}
	return ""
}
