//go:build cgo

package gpkg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/provider"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
)

const (
	Name                 = "gpkg"
	DefaultSRID          = tegola.WebMercator
	DefaultIDFieldName   = "fid"
	DefaultGeomFieldName = "geom"
)

// config keys
const (
	ConfigKeyFilePath    = "filepath"
	ConfigKeySRID        = "srid"
	ConfigKeyCRSDefn     = "crs_defn"
	ConfigKeyLayers      = "layers"
	ConfigKeyLayerName   = "name"
	ConfigKeyTableName   = "tablename"
	ConfigKeySQL         = "sql"
	ConfigKeyGeomIDField = "id_fieldname"
	ConfigKeyGeomField   = "geometry_fieldname"
	ConfigKeyFields      = "fields"
)

// Geometry format values accepted by the gpkg provider's geometry_format
// setting. "" and "gpkg" select the native GeoPackage binary layout.
const (
	GeometryFormatGPKG = "gpkg"
	GeometryFormatWKB  = codec.FormatWKB
	GeometryFormatWKT  = codec.FormatWKT
	GeometryFormatMOS  = codec.FormatMOS
)

// resolveGeometryFormat validates the geometry_format value.
func resolveGeometryFormat(v string) (string, error) {
	switch v {
	case "", GeometryFormatGPKG, GeometryFormatWKB, GeometryFormatWKT, GeometryFormatMOS:
		return v, nil
	default:
		return "", fmt.Errorf("invalid %v: %q (expected one of %q, %q, %q, %q)",
			codec.ConfigKeyGeometryFormat, v,
			GeometryFormatGPKG, GeometryFormatWKB, GeometryFormatWKT, GeometryFormatMOS)
	}
}

// gpkgGeometryFormats is the set of geometry_format values accepted at the
// layer level (an empty value falls back to the provider-level value).
var gpkgGeometryFormats = map[string]struct{}{
	GeometryFormatGPKG: {},
	GeometryFormatWKB:  {},
	GeometryFormatWKT:  {},
	GeometryFormatMOS:  {},
}

func decodeGeometry(bytes []byte) (*BinaryHeader, geom.Geometry, error) {
	h, err := NewBinaryHeader(bytes)
	if err != nil {
		log.Errorf("error decoding geometry header: %v", err)
		return h, nil, err
	}

	geo, err := wkb.DecodeBytes(bytes[h.Size():])
	if err != nil {
		log.Errorf("error decoding geometry: %v", err)
		return h, nil, err
	}

	return h, geo, nil
}

// decodeGeometryValue decodes a geometry column value according to the
// layer's geometry format. Only the default GeoPackage binary layout carries
// a GeoPackageBinaryHeader; wkb/wkt/mos values are passed through the shared
// codec, which returns no header.
func decodeGeometryValue(v interface{}, format string, mosCfg codec.MOSConfig) (*BinaryHeader, geom.Geometry, error) {
	if format == "" || format == GeometryFormatGPKG {
		geomData, ok := v.([]byte)
		if !ok {
			return nil, nil, errors.New("unexpected column type for geom field. expected blob")
		}
		return decodeGeometry(geomData)
	}

	switch format {
	case GeometryFormatWKB:
		geo, err := codec.DecodeWKB(v)
		if err != nil {
			return nil, nil, err
		}
		return nil, geo, nil
	case GeometryFormatWKT:
		geo, err := codec.DecodeWKT(v)
		if err != nil {
			return nil, nil, err
		}
		return nil, geo, nil
	case GeometryFormatMOS:
		geo, err := codec.DecodeMOS(v, mosCfg)
		if err != nil {
			return nil, nil, err
		}
		return nil, geo, nil
	default:
		return nil, nil, fmt.Errorf("unknown geometry_format: %v", format)
	}
}

// quoteIdent quotes a SQLite identifier, escaping embedded backticks so a
// crafted config value cannot break out of the quoted name.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// rawBoundsSQL builds a coarse SQL bounds filter from the raw bounds columns
// detected at registration for a non-GPKG geometry format, mirroring the
// MySQL provider's MOS bounds filter. The stored values use the layer's
// stored coordinate units: quantized MOS units for the MOS format (scaled by
// 10^precision / unit factor), otherwise the layer CRS coordinates that the
// source extent is already expressed in. An empty result means the filter
// cannot be applied and filtering must happen in memory after decoding.
func rawBoundsSQL(l *Layer, extent *geom.Extent) string {
	if l.boundFieldnames == nil || extent == nil {
		return ""
	}
	minX, maxX, minY, maxY := extent.MinX(), extent.MaxX(), extent.MinY(), extent.MaxY()
	if l.geometryFormat == codec.FormatMOS {
		precisionScale := math.Pow(10, l.mosConfig.Precision)
		unitFactor := l.mosConfig.UnitFactor
		if math.IsNaN(precisionScale) || math.IsInf(precisionScale, 0) || precisionScale <= 0 ||
			math.IsNaN(unitFactor) || math.IsInf(unitFactor, 0) || unitFactor <= 0 {
			return ""
		}
		rawScale := precisionScale / unitFactor
		if math.IsNaN(rawScale) || math.IsInf(rawScale, 0) || rawScale <= 0 {
			return ""
		}
		minX, maxX = math.Floor(minX*rawScale), math.Ceil(maxX*rawScale)
		minY, maxY = math.Floor(minY*rawScale), math.Ceil(maxY*rawScale)
	}
	format := func(value float64) string {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	for _, value := range []float64{minX, maxX, minY, maxY} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return ""
		}
	}
	return fmt.Sprintf(
		"l.%v <= %v AND l.%v >= %v AND l.%v <= %v AND l.%v >= %v",
		quoteIdent(l.boundFieldnames[0]), format(maxX),
		quoteIdent(l.boundFieldnames[1]), format(minX),
		quoteIdent(l.boundFieldnames[2]), format(maxY),
		quoteIdent(l.boundFieldnames[3]), format(minY),
	)
}

type Provider struct {
	// path to the geopackage file
	Filepath string
	// layers maps layer names to their definitions. Layers are stored as
	// pointers so runtime discoveries (MOS system-info application) are
	// visible to concurrent tile requests; the map itself is populated
	// only by NewTileProvider and is read-only afterwards.
	layers map[string]*Layer
	// reference to the database connection
	db *sql.DB
	// default SRID for the provider
	srid uint64
	// mu guards the runtime mutable layer state (system-info application)
	// and the warn-once bookkeeping below; tile requests run concurrently.
	mu sync.Mutex
	// warned tracks warn-once keys (unexpected column types, late
	// system-info rows) so a warning is emitted once instead of per row.
	warned map[string]struct{}
}

// ErrUnknownLayer denotes a layer name that is not registered on the provider.
type ErrUnknownLayer struct {
	Name string
}

func (e ErrUnknownLayer) Error() string {
	return fmt.Sprintf("layer not registered with provider: %v", e.Name)
}

func (p *Provider) Layers() ([]provider.LayerInfo, error) {
	log.Debug("attempting gpkg.Layers()")

	ls := make([]provider.LayerInfo, len(p.layers))

	var i int
	for _, player := range p.layers {
		ls[i] = player
		i++
	}

	log.Debugf("returning LayerInfo array: %v", ls)

	return ls, nil
}

func (p *Provider) TileFeatures(ctx context.Context, layer string, tile provider.Tile, queryParams provider.Params, fn func(f *provider.Feature) error) error {
	log.Debugf("fetching layer %v", layer)

	pLayer, ok := p.layers[layer]
	if !ok {
		return ErrUnknownLayer{layer}
	}

	// Resolve pending MOS system-info metadata before the tile extent and
	// the spatial filter are built, so decode parameters and the layer
	// SRID are final for the whole row stream (see ensureSystemInfo).
	p.ensureSystemInfo(pLayer, tile)

	// read the tile extent
	tileBBox, tileSRID := tile.BufferedExtent()

	// tileSRID is assumed to always be WebMercator. Build a conservative
	// source-CRS extent before applying the provider's spatial filter.
	if pLayer.srid != tileSRID {
		sourceBBox, err := basic.FromWebMercatorExtent(pLayer.srid, tileBBox)
		if err != nil {
			return fmt.Errorf("error converting tile extent: %v ", err)
		}
		tileBBox = sourceBBox
	}

	var qtext string
	args := make([]interface{}, 0)

	if pLayer.tablename != "" {
		if pLayer.geometryFormat != "" && pLayer.geometryFormat != GeometryFormatGPKG {
			// Non-native geometry formats (wkb/wkt/mos) live in plain
			// tables without a GeoPackage binary header or RTree index.
			// When the table carries raw bounds columns (minx/maxx/miny/
			// maxy, detected at registration) they act as a coarse SQL
			// filter; otherwise the whole geometry column is scanned and
			// the exact in-memory filter below is the only one.
			selectClause := fmt.Sprintf("SELECT l.%v, l.%v", quoteIdent(pLayer.idFieldname), quoteIdent(pLayer.geomFieldname))

			for _, tf := range pLayer.tagFieldnames {
				selectClause += fmt.Sprintf(", l.%v", quoteIdent(tf))
			}

			where := fmt.Sprintf("l.%v IS NOT NULL", quoteIdent(pLayer.geomFieldname))
			if bboxSQL := rawBoundsSQL(pLayer, tileBBox); bboxSQL != "" {
				where += " AND " + bboxSQL
			}
			qtext = fmt.Sprintf("%v FROM %v l WHERE %v", selectClause, quoteIdent(pLayer.tablename), where)
		} else {
			// If layer was specified via "tablename" in config, construct query.
			rtreeTablename := fmt.Sprintf("rtree_%v_%v", pLayer.tablename, pLayer.geomFieldname)

			selectClause := fmt.Sprintf("SELECT l.%v, l.%v", quoteIdent(pLayer.idFieldname), quoteIdent(pLayer.geomFieldname))

			for _, tf := range pLayer.tagFieldnames {
				selectClause += fmt.Sprintf(", l.%v", quoteIdent(tf))
			}

			// l - layer table, si - spatial index; ORDER BY keeps row
			// order deterministic so concurrent requests stream identical
			// rows (stable MVT output).
			qtext = fmt.Sprintf("%v FROM %v l JOIN %v si ON l.%v = si.id WHERE l.%v IS NOT NULL AND !BBOX! ORDER BY l.%v", selectClause, quoteIdent(pLayer.tablename), quoteIdent(rtreeTablename), quoteIdent(pLayer.idFieldname), quoteIdent(pLayer.geomFieldname), quoteIdent(pLayer.idFieldname))

			qtext = replaceTokens(qtext, pLayer, tile, tileBBox)
		}
	} else {
		// If layer was specified via "sql" in config, collect it
		qtext = replaceTokens(pLayer.sql, pLayer, tile, tileBBox)
		qtext = queryParams.ReplaceParams(qtext, &args)
	}

	log.Debugf("qtext: %v", qtext)

	// QueryContext stops the SQLite query when the request context is
	// cancelled instead of running it to completion.
	rows, err := p.db.QueryContext(ctx, qtext, args...)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		log.Errorf("err during query: %v - %v", qtext, err)
		return err
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	for rows.Next() {
		// check if the context cancelled or timed out
		if ctx.Err() != nil {
			return ctx.Err()
		}

		vals := make([]interface{}, len(cols))
		valPtrs := make([]interface{}, len(cols))
		for i := 0; i < len(cols); i++ {
			valPtrs[i] = &vals[i]
		}

		if err = rows.Scan(valPtrs...); err != nil {
			log.Errorf("err reading row values: %v", err)
			return err
		}

		feature := provider.Feature{
			Tags: map[string]interface{}{},
		}
		skipRow := false

		for i := range cols {
			if vals[i] == nil {
				if cols[i] == pLayer.geomFieldname {
					skipRow = true
				}
				continue
			}

			switch cols[i] {
			case pLayer.idFieldname:
				feature.ID, err = provider.ConvertFeatureID(vals[i])
				if err != nil {
					return err
				}

			case pLayer.geomFieldname:
				// The MOS layer self-description blob (MapplGIS LayerInfo)
				// is metadata, never a feature. System-info parameters are
				// finalized before the query runs (registration sampling or
				// the runtime pre-query in ensureSystemInfo); applying them
				// mid-stream would decode earlier rows with different
				// precision/units and invalidate the already-built SQL
				// bounds filter, so late blobs are skipped (R6).
				if pLayer.geometryFormat == codec.FormatMOS && codec.IsSystemInfoValue(vals[i]) {
					p.warnOnce("late-system-info:"+pLayer.name,
						"layer '%v': MOS system-info row encountered after the query was built; skipping",
						pLayer.name)
					skipRow = true
					continue
				}

				_, geo, err := decodeGeometryValue(vals[i], pLayer.geometryFormat, pLayer.mosConfig)
				if err != nil {
					log.Errorf("error decoding geometry: %v", err)
					return err
				}

				// The layer SRID is resolved at registration time from the
				// CRS contract (explicit config > gpkg_contents.srs_id >
				// provider default, with MOS system-info projection as a
				// last step), so the per-row WKB header is not consulted
				// here.
				feature.SRID = pLayer.srid
				if feature.SRID == 0 {
					feature.SRID = DefaultSRID
				}

				// mixed-content policy for explicit geometry_type: permit
				// the feature but warn once per layer.
				if pLayer.geomTypeExplicit {
					codec.WarnOnceGeometryTypeMismatch(pLayer.Name(), pLayer.geomType, geo)
				}
				feature.Geometry = geo
			case "minx", "miny", "maxx", "maxy", "min_zoom", "max_zoom":
				// Skip these columns used for bounding box and zoom filtering
				continue

			default:
				// Grab any non-nil, non-id, non-bounding box, & non-geometry column as a tag
				switch v := vals[i].(type) {
				case []uint8:
					feature.Tags[cols[i]] = string(v)
				case string:
					feature.Tags[cols[i]] = v
				case int64:
					feature.Tags[cols[i]] = v
				case float64:
					feature.Tags[cols[i]] = v
				case bool:
					feature.Tags[cols[i]] = v
				case time.Time:
					feature.Tags[cols[i]] = v.Format(time.RFC3339)
				default:
					// Emit a warning once per column: the same unexpected
					// type recurs for every row of the stream.
					p.warnOnce("unexpected-column:"+cols[i],
						"unexpected type for sqlite column data: %v: %T", cols[i], v)
				}
			}
		}

		if skipRow || feature.Geometry == nil {
			continue
		}

		// Exact in-memory filter. Mandatory for wkb/wkt/mos formats whose
		// queries cannot use the RTree join; harmless for native GPKG
		// geometry that was already filtered via !BBOX!.
		if !codec.GeometryIntersectsExtent(feature.Geometry, tileBBox) {
			continue
		}

		// pass the feature to the provided call back
		if err = fn(&feature); err != nil {
			return err
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// ensureSystemInfo resolves pending MOS system-info metadata before the tile
// query runs. Registration normally applies it while sampling the layer; for
// tile-dependent custom SQL (deferred inspection) a dedicated pre-query runs
// here, so decode parameters and the layer SRID are final before the spatial
// filter is built. Previously system-info could be applied mid-stream, which
// decoded earlier rows with different precision/units and invalidated the
// already-built SQL bounds filter. The pre-query runs at most once per layer.
func (p *Provider) ensureSystemInfo(layer *Layer, tile provider.Tile) {
	if layer.geometryFormat != codec.FormatMOS {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if layer.systemInfoApplied || layer.systemInfoChecked {
		return
	}
	if layer.deferredInspection && layer.sql != "" {
		// project just the geometry column like the startup inspection
		// does: the custom SQL may select additional columns that would
		// break inspectCustomSQLSample's single-value Scan
		qgeom := quoteIdent(layer.geomFieldname)
		qtext := fmt.Sprintf("SELECT %[1]v FROM (%[2]v) WHERE %[1]v IS NOT NULL LIMIT %[3]v;",
			qgeom, buildDeferredInspectionSQL(layer, tile), codec.InspectionSampleLimit)
		if _, _, _, err := inspectCustomSQLSample(p.db, layer, qtext); err != nil {
			log.Warnf("layer '%v' system-info pre-query failed: %v", layer.Name(), err)
		}
	}
	layer.systemInfoChecked = true
}

// warnOnce logs a warning only the first time it is called with a given key.
// Guarded by mu because tile requests run concurrently.
func (p *Provider) warnOnce(key string, format string, args ...interface{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warned == nil {
		p.warned = make(map[string]struct{})
	}
	if _, ok := p.warned[key]; ok {
		return
	}
	p.warned[key] = struct{}{}
	log.Warnf(format, args...)
}

// Close will close the Provider's database connection
func (p *Provider) Close() error {
	return p.db.Close()
}

func geomNameToGeom(name string) (geom.Geometry, error) {
	switch name {
	case "POINT":
		return geom.Point{}, nil
	case "LINESTRING":
		return geom.LineString{}, nil
	case "POLYGON":
		return geom.Polygon{}, nil
	case "MULTIPOINT":
		return geom.MultiPoint{}, nil
	case "MULTILINESTRING":
		return geom.MultiLineString{}, nil
	case "MULTIPOLYGON":
		return geom.MultiPolygon{}, nil
	case "GEOMETRYCOLLECTION":
		return geom.Collection{}, nil
	case "GEOMETRY":
		return nil, nil
	}

	return nil, fmt.Errorf("unsupported geometry type: %v", name)
}
