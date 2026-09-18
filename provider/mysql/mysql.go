package mysql

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/provider"
)

const (
	Name                 = "mysql"
	DefaultSRID          = tegola.WebMercator
	DefaultPort          = 3306
	DefaultIDFieldName   = "fid"
	DefaultGeomFieldName = "geom"
)

// geometry column encoding formats. "auto" detects the server flavor
// (MySQL vs MariaDB) at provider startup via SELECT VERSION() and uses the
// matching native layout. "wkb" expects plain WKB (e.g. selected via
// ST_AsBinary(geom)) with no internal header.
const (
	GeometryFormatAuto    = "auto"
	GeometryFormatMySQL   = "mysql"
	GeometryFormatMariaDB = "mariadb"
	GeometryFormatWKB     = "wkb"
)

// config keys
const (
	ConfigKeyHost           = "host"
	ConfigKeyPort           = "port"
	ConfigKeyDatabase       = "database"
	ConfigKeyUser           = "user"
	ConfigKeyPassword       = "password"
	ConfigKeySRID           = "srid"
	ConfigKeyMaxConn        = "max_connections"
	ConfigKeyGeometryFormat = "geometry_format"
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
// to the configured format: "mysql", "mariadb", "wkb" or "auto".
// With "auto" the server flavor detected at startup (serverFlavor) selects
// the native layout. Plain WKB is accepted as a last resort, which covers
// values already converted with ST_AsBinary() in custom SQL.
func decodeGeometry(b []byte, format string, serverFlavor string) (srid uint64, g geom.Geometry, err error) {
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
			return 0, nil, err
		}
		if srid, g, err = decodeMySQLFormat(b); err == nil {
			return srid, g, nil
		}
		if g, wkbErr := wkb.DecodeBytes(b); wkbErr == nil {
			return 0, g, nil
		}
		return 0, nil, err
	default:
		return 0, nil, fmt.Errorf("unknown geometry_format: %v", format)
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
	log.Debugf("fetching layer %v", layer)

	pLayer, ok := p.layers[layer]
	if !ok {
		return ErrUnknownLayer{layer}
	}

	// read the tile extent
	tileBBox, tileSRID := tile.BufferedExtent()

	// check if the SRID of the layer differs from that of the tile. tileSRID is assumed to always be WebMercator
	if pLayer.srid != tileSRID {
		minGeo, err := basic.FromWebMercator(pLayer.srid, geom.Point{tileBBox.MinX(), tileBBox.MinY()})
		if err != nil {
			return fmt.Errorf("error converting point: %v ", err)
		}

		maxGeo, err := basic.FromWebMercator(pLayer.srid, geom.Point{tileBBox.MaxX(), tileBBox.MaxY()})
		if err != nil {
			return fmt.Errorf("error converting point: %v ", err)
		}

		tileBBox = geom.NewExtent(minGeo.(geom.Point), maxGeo.(geom.Point))
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

	rows, err := p.db.Query(qtext, args...)
	if err != nil {
		log.Errorf("err during query: %v - %v", qtext, err)
		return err
	}
	defer rows.Close()

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
				geomData, ok := vals[i].([]byte)
				if !ok {
					log.Errorf("unexpected column type for geom field. got %t", vals[i])
					return errors.New("unexpected column type for geom field. expected blob")
				}

				hSrid, geo, err := decodeGeometry(geomData, p.geometryFormat, p.serverFlavor)
				if err != nil {
					return err
				}

				// an explicitly configured layer/provider SRID wins over the value
				// decoded from the geometry header, which can be 0 or carry
				// MariaDB axis-order flags.
				if pLayer.srid != 0 {
					feature.SRID = pLayer.srid
				} else if hSrid > 0 {
					feature.SRID = hSrid
				} else if p.srid != 0 {
					feature.SRID = p.srid
				} else {
					feature.SRID = DefaultSRID
				}
				feature.Geometry = geo

			default:
				// Grab any non-nil, non-id, non-geometry column as a tag
				switch v := vals[i].(type) {
				case []uint8:
					asBytes := make([]byte, len(v))
					for j := 0; j < len(v); j++ {
						asBytes[j] = v[j]
					}

					feature.Tags[cols[i]] = string(asBytes)
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

		// pass the feature to the provided call back
		if err = fn(&feature); err != nil {
			return err
		}
	}

	return rows.Err()
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

// Cleanup will close all database connections and destroy all previously instantiated Provider instances
func Cleanup() {
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
