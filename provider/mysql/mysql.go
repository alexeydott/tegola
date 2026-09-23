package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/geom/encoding/wkt"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/mos"
	"github.com/go-spatial/tegola/provider"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

const (
	Name                 = "mysql"
	DefaultSRID          = tegola.WebMercator
	DefaultPort          = 3306
	DefaultIDFieldName   = "fid"
	DefaultGeomFieldName = "geom"
)

const (
	mysqlQueryMaxAttempts = 3
	mysqlRetryBaseDelay   = 100 * time.Millisecond
	mysqlConnMaxIdleTime  = 5 * time.Minute
	mysqlConnMaxLifetime  = 30 * time.Minute
)

// geometry column encoding formats. "auto" detects the server flavor
// (MySQL vs MariaDB) at provider startup via SELECT VERSION() and uses the
// matching native layout. "wkb" expects plain WKB (e.g. selected via
// ST_AsBinary(geom)) with no internal header. "wkt" expects WKT text
// (e.g. CHAR/VARCHAR/TEXT columns holding LINESTRING(...), or a
// ST_AsText(...) expression selected in custom SQL).
const (
	GeometryFormatAuto    = "auto"
	GeometryFormatMySQL   = "mysql"
	GeometryFormatMariaDB = "mariadb"
	GeometryFormatWKB     = "wkb"
	GeometryFormatWKT     = "wkt"
	GeometryFormatMOS     = "mos"
)

// config keys
const (
	ConfigKeyHost           = "host"
	ConfigKeyPort           = "port"
	ConfigKeyDatabase       = "database"
	ConfigKeyUser           = "user"
	ConfigKeyPassword       = "password"
	ConfigKeySRID           = "srid"
	ConfigKeyCRSDefn        = "crs_defn"
	ConfigKeyMaxConn        = "max_connections"
	ConfigKeyGeometryFormat = "geometry_format"
	ConfigKeyMOSPrecision   = "mos_precision"
	ConfigKeyMOSUnits       = "mos_units"
	ConfigKeyProj4          = "proj4"
	ConfigKeyLayers         = "layers"
	ConfigKeyLayerName      = "name"
	ConfigKeyTableName      = "tablename"
	ConfigKeySQL            = "sql"
	ConfigKeyGeomIDField    = "id_fieldname"
	ConfigKeyGeomField      = "geometry_fieldname"
	ConfigKeyFields         = "fields"
)

// MariaDB 10.7+ stores axis-order flags for reference geometries in the
// three top bits of the SRID field. Mask them off to recover the actual SRID.
const mariaDBSRIDMask = 0x1FFFFFFF

// ErrUnknownLayer denotes a layer name that is not registered on the provider
type ErrUnknownLayer struct {
	Name string
}

func (e ErrUnknownLayer) Error() string {
	return fmt.Sprintf("layer not registered with provider: %v", e.Name)
}

// decodeMySQLFormat parses the MySQL native internal geometry layout:
// [4 bytes SRID (little-endian)][1 byte byte-order][WKB (type + body)]
func decodeMySQLFormat(b []byte) (srid uint64, g geom.Geometry, err error) {
	if len(b) < 9 {
		return 0, nil, fmt.Errorf("geometry blob too short (%v bytes) for MySQL native format", len(b))
	}
	bom := b[4]
	if bom != 0 && bom != 1 {
		return 0, nil, fmt.Errorf("invalid byte-order marker (%v) for MySQL native format", bom)
	}
	srid = uint64(binary.LittleEndian.Uint32(b[0:4]))
	g, err = wkb.DecodeBytes(b[5:])
	if err != nil {
		return 0, nil, fmt.Errorf("error decoding WKB body of MySQL native geometry: %v", err)
	}
	return srid, g, nil
}

// decodeMariaDBFormat parses the MariaDB native internal geometry layout:
// [1 byte byte-order][4 bytes SRID (stored in that byte order)][WKB (type + body)]
// Note the SRID comes *after* the byte-order marker, unlike MySQL where it
// precedes it. In MariaDB 10.7+ the top three SRID bits carry axis-order
// flags for reference geometries and are masked off here.
func decodeMariaDBFormat(b []byte) (srid uint64, g geom.Geometry, err error) {
	if len(b) < 9 {
		return 0, nil, fmt.Errorf("geometry blob too short (%v bytes) for MariaDB native format", len(b))
	}
	bom := b[0]
	if bom != 0 && bom != 1 {
		return 0, nil, fmt.Errorf("invalid byte-order marker (%v) for MariaDB native format", bom)
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if bom == 0 {
		bo = binary.BigEndian
	}
	srid = uint64(bo.Uint32(b[1:5]) & mariaDBSRIDMask)
	g, err = wkb.DecodeBytes(b[5:])
	if err != nil {
		return 0, nil, fmt.Errorf("error decoding WKB body of MariaDB native geometry: %v", err)
	}
	return srid, g, nil
}

// decodeGeometry decodes a geometry value read from the database according
// to the configured format: "mysql", "mariadb", "wkb", "wkt", "mos" or
// "auto". With "auto" the server flavor detected at startup (serverFlavor)
// selects the native layout. Plain WKB is accepted as a last resort, which
// covers values already converted with ST_AsBinary() in custom SQL.
// For the "mos" format an optional Options value overrides the default
// quantization precision/offset (layer-level mos_precision).
func decodeGeometry(v interface{}, format string, serverFlavor string, mosOpts ...mos.Options) (srid uint64, g geom.Geometry, err error) {
	switch format {
	case GeometryFormatWKT:
		return decodeWKT(v)
	case GeometryFormatMOS:
		var opts = mos.Options{Precision: mosPrecisionDefault, OffsetX: mosOffsetDefault, OffsetY: mosOffsetDefault}
		if len(mosOpts) > 0 {
			opts = mosOpts[0]
		}
		return decodeMOS(v, opts)
	}

	// all remaining formats operate on binary blobs
	b, ok := v.([]byte)
	if !ok {
		// a string may still arrive for binary formats depending on driver
		// column typing; convert before rejecting.
		if s, isStr := v.(string); isStr {
			b = []byte(s)
		} else {
			return 0, nil, fmt.Errorf("unexpected geometry column type %T, expected blob", v)
		}
	}

	switch format {
	case GeometryFormatMySQL:
		return decodeMySQLFormat(b)
	case GeometryFormatMariaDB:
		return decodeMariaDBFormat(b)
	case GeometryFormatWKB:
		g, err = wkb.DecodeBytes(b)
		return 0, g, err
	case GeometryFormatAuto, "":
		if serverFlavor == GeometryFormatMariaDB {
			if srid, g, err = decodeMariaDBFormat(b); err == nil {
				return srid, g, nil
			}
			// fall through to plain WKB as a last resort
			if g, wkbErr := wkb.DecodeBytes(b); wkbErr == nil {
				return 0, g, nil
			}
			// last resort: the blob may actually be WKT text (e.g. a
			// CHAR/TEXT geometry column stored without a native type)
			if g, wktErr := wkt.DecodeBytes(b); wktErr == nil {
				return 0, g, nil
			}
			return 0, nil, err
		}
		if srid, g, err = decodeMySQLFormat(b); err == nil {
			return srid, g, nil
		}
		if g, wkbErr := wkb.DecodeBytes(b); wkbErr == nil {
			return 0, g, nil
		}
		if g, wktErr := wkt.DecodeBytes(b); wktErr == nil {
			return 0, g, nil
		}
		return 0, nil, err
	default:
		return 0, nil, fmt.Errorf("unknown geometry_format: %v", format)
	}
}

// default MOS quantization: integer units with no offset. Configured per
// provider/layer via mos_precision (decimal digits) and mos_units, overridden
// by layer-level settings when present.
const (
	mosPrecisionDefault   = 0.0
	mosOffsetDefault      = 0.0
	mosUnitsFactorDefault = 1.0
)

// decodeMOS decodes a MapplBase MOS blob (the proprietary binary geometry
// format written by TMapObjectStructureBase) using the mos package. The
// quantized integer coordinates are dequantized with the configured
// precision (decimal digits). MOS carries no SRID, so 0 is returned and
// the configured provider/layer SRID applies.
func decodeMOS(v interface{}, opts mos.Options) (uint64, geom.Geometry, error) {
	var b []byte
	switch val := v.(type) {
	case []byte:
		b = val
	case string:
		b = []byte(val)
	default:
		return 0, nil, fmt.Errorf("unexpected MOS geometry column type %T, expected blob", v)
	}
	g, err := mos.Decode(b, opts)
	if err != nil {
		return 0, nil, fmt.Errorf("error decoding MOS geometry: %v", err)
	}
	return 0, g, nil
}

// geometryIntersectsExtent reports whether a geometry's bounding box
// intersects the given extent. It is used for the MOS geometry format,
// where the spatial filter cannot be pushed into SQL. If a bbox cannot be
// computed the geometry is kept (conservative).
func geometryIntersectsExtent(g geom.Geometry, e *geom.Extent) bool {
	if g == nil || e == nil {
		return true
	}
	gb, err := geom.NewExtentFromGeometry(g)
	if err != nil || gb == nil {
		return true
	}
	if _, ok := e.Intersect(gb); ok {
		return true
	}
	return false
}

func isRetryableConnectionError(err error) bool {
	return errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysqlDriver.ErrInvalidConn)
}

func waitForMySQLRetry(ctx context.Context, attempt int) error {
	delay := mysqlRetryBaseDelay * time.Duration(1<<attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// decodeWKT parses a WKT string (e.g. "LINESTRING(1 2, 3 4)") into a
// geometry. WKT carries no SRID, so 0 is returned and the configured
// provider/layer SRID applies.
func decodeWKT(v interface{}) (uint64, geom.Geometry, error) {
	switch s := v.(type) {
	case string:
		g, err := wkt.DecodeString(s)
		if err != nil {
			return 0, nil, fmt.Errorf("error decoding WKT geometry: %v", err)
		}
		return 0, g, nil
	case []byte:
		g, err := wkt.DecodeBytes(s)
		if err != nil {
			return 0, nil, fmt.Errorf("error decoding WKT geometry: %v", err)
		}
		return 0, g, nil
	default:
		return 0, nil, fmt.Errorf("unexpected WKT geometry column type %T, expected text", v)
	}
}

// geomTypeName returns the OGC-style name ("POINT", "MULTIPOLYGON", ...)
// of a decoded geometry, used for the !GEOM_TYPE! token.
func geomTypeName(g geom.Geometry) string {
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

type Provider struct {
	// database host
	Host string
	// database port
	Port int
	// database name
	Database string
	// map of layer name and corresponding layer
	layers map[string]Layer
	// reference to the database connection
	db *sql.DB
	// default SRID for the provider
	srid uint64
	// geometry column encoding: auto, mysql, mariadb or wkb
	geometryFormat string
	// detected server flavor ("mysql" or "mariadb"), used by the auto format
	serverFlavor string
}

func (p *Provider) Layers() ([]provider.LayerInfo, error) {
	log.Debug("attempting mysql.Layers()")

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
	var err error
	for attempt := 0; attempt < mysqlQueryMaxAttempts; attempt++ {
		err = p.tileFeaturesAttempt(ctx, layer, tile, queryParams, fn)
		if err == nil || !isRetryableConnectionError(err) || attempt == mysqlQueryMaxAttempts-1 {
			return err
		}

		log.Warnf("mysql provider: retrying tile query after broken connection (attempt %d/%d): %v",
			attempt+1, mysqlQueryMaxAttempts, err)
		if err := waitForMySQLRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return err
}

func (p *Provider) tileFeaturesAttempt(ctx context.Context, layer string, tile provider.Tile, queryParams provider.Params, fn func(f *provider.Feature) error) error {
	log.Debugf("fetching layer %v", layer)

	pLayer, ok := p.layers[layer]
	if !ok {
		return ErrUnknownLayer{layer}
	}

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
		// If layer was specified via "tablename" in config, construct query.
		selectClause := fmt.Sprintf("SELECT %v, %v", quoteIdentifier(pLayer.idFieldname), quoteIdentifier(pLayer.geomFieldname))

		for _, tf := range pLayer.tagFieldnames {
			selectClause += fmt.Sprintf(", %v", quoteIdentifier(tf))
		}

		qtext = fmt.Sprintf("%v FROM %v WHERE %v IS NOT NULL AND !BBOX!", selectClause, quoteIdentifier(pLayer.tablename), quoteIdentifier(pLayer.geomFieldname))

		qtext = replaceTokens(qtext, &pLayer, tile, tileBBox)
	} else {
		// If layer was specified via "sql" in config, collect it
		qtext = replaceTokens(pLayer.sql, &pLayer, tile, tileBBox)
		qtext = queryParams.ReplaceParams(qtext, &args)
	}

	log.Debugf("qtext: %v", qtext)

	rows, err := p.db.QueryContext(ctx, qtext, args...)
	if err != nil {
		log.Errorf("err during query: %v - %v", qtext, err)
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	// declared column types let us distinguish numeric columns the driver
	// returns as []byte (textual form) from genuine text/blob columns
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return err
	}

	var geomErr error
	features := make([]provider.Feature, 0)
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

		// set when the row's geometry is undecodable or outside the tile:
		// the row is then skipped entirely (no feature emitted) rather than
		// being emitted with a nil geometry and SRID 0, which would fail
		// downstream reprojection and kill the whole tile.
		skipRow := false

		for i := range cols {
			// check if the context cancelled or timed out
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if vals[i] == nil {
				continue
			}

			switch cols[i] {
			case pLayer.idFieldname:
				feature.ID, err = provider.ConvertFeatureID(vals[i])
				if err != nil {
					return err
				}

			case pLayer.geomFieldname:
				// a layer system info blob (MapplBase layer self-description)
				// is metadata, not geometry; skip it silently.
				if blob, ok := vals[i].([]byte); ok && mos.IsSystemInfoBlob(blob) {
					skipRow = true
					break
				}
				srid, geo, err := decodeGeometry(vals[i], pLayer.geometryFormat, p.serverFlavor, mos.Options{Precision: pLayer.mosPrecision, UnitFactor: pLayer.mosUnitsFactor})
				if err != nil {
					// a single undecodable row (e.g. a version-prefixed or
					// otherwise non-MOS blob) must not kill the whole tile;
					// log it and skip
					log.Warnf("mysql provider: skipping undecodable geometry in layer %v (id %v): %v", pLayer.Name(), feature.ID, err)
					geomErr = err
					skipRow = true
					break
				}

				// MOS blobs are opaque binaries, so the spatial filter cannot
				// be pushed into SQL (!BBOX! degrades to 1=1). Drop rows whose
				// decoded geometry cannot intersect the tile's buffered extent
				// (already transformed into the layer's source SRID).
				if pLayer.geometryFormat == GeometryFormatMOS && !geometryIntersectsExtent(geo, tileBBox) {
					skipRow = true
					break
				}

				// an explicitly configured layer/provider SRID wins over the value
				// decoded from the geometry header, which can be 0 or carry
				// MariaDB axis-order flags.
				if pLayer.srid != 0 {
					feature.SRID = pLayer.srid
				} else if srid > 0 {
					feature.SRID = srid
				} else if p.srid != 0 {
					feature.SRID = p.srid
				} else {
					feature.SRID = DefaultSRID
				}
				feature.Geometry = geo

			default:
				// Grab any non-nil, non-id, non-geometry column as a tag,
				// converting numeric columns the driver returns as []byte
				switch v := vals[i].(type) {
				case []uint8:
					tagVal, cerr := tagValueFromColumn(colTypes[i], v)
					if cerr != nil {
						log.Errorf("unable to convert mysql column data: %v: %v", cols[i], cerr)
						continue
					}
					if tagVal != nil {
						feature.Tags[cols[i]] = tagVal
					}
				case int64:
					feature.Tags[cols[i]] = v
				case string:
					feature.Tags[cols[i]] = v
				case float64:
					feature.Tags[cols[i]] = v
				case time.Time:
					feature.Tags[cols[i]] = v.Format(time.RFC3339)
				default:
					log.Errorf("unexpected type for mysql column data: %v: %T", cols[i], v)
				}
			}
		}

		// drop rows whose geometry was undecodable or outside the tile
		if skipRow {
			continue
		}

		features = append(features, feature)
	}

	if err := rows.Err(); err != nil {
		return err
	}
	// if every row in this tile had an undecodable geometry the layer is
	// effectively broken (misconfigured mos_precision or foreign blob
	// format) — surface it instead of silently rendering an empty tile
	if geomErr != nil && len(features) == 0 {
		return fmt.Errorf("no decodable MOS geometries in layer %v: %v", pLayer.Name(), geomErr)
	}

	// Do not emit features until the complete result set has been consumed.
	// If the server drops the connection while rows are being read, the caller
	// can safely retry the read without duplicating partially emitted features.
	for i := range features {
		if err := fn(&features[i]); err != nil {
			return err
		}
	}
	return nil
}

// Close will close the Provider's database connection
func (p *Provider) Close() error {
	return p.db.Close()
}

// quoteIdentifier wraps an identifier in backticks, escaping embedded ones.
func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// wktPolygon formats an extent as a WKT POLYGON string for use with
// ST_GeomFromText in the !BBOX! replacement.
func wktPolygon(ext *geom.Extent) string {
	f := func(v float64) string {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprintf("POLYGON((%v %v, %v %v, %v %v, %v %v, %v %v))",
		f(ext.MinX()), f(ext.MinY()),
		f(ext.MaxX()), f(ext.MinY()),
		f(ext.MaxX()), f(ext.MaxY()),
		f(ext.MinX()), f(ext.MaxY()),
		f(ext.MinX()), f(ext.MinY()),
	)
}

// reference to all instantiated providers
var providers []Provider
var providersMu sync.Mutex

// Cleanup will close all database connections and destroy all previously instantiated Provider instances
func Cleanup() {
	providersMu.Lock()
	defer providersMu.Unlock()

	if len(providers) > 0 {
		log.Infof("cleaning up mysql providers")
	}

	for i := range providers {
		if err := providers[i].Close(); err != nil {
			log.Errorf("err closing connection: %v", err)
		}
	}

	providers = make([]Provider, 0)
}
