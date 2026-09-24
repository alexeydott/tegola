//go:build cgo
// +build cgo

package gpkg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/internal/log"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider"
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

type Provider struct {
	// path to the geopackage file
	Filepath string
	// map of layer name and corresponding sql
	layers map[string]Layer
	// reference to the database connection
	db *sql.DB
	// default SRID for the provider
	srid uint64
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
			// tables without a GeoPackage binary header or RTree index, so
			// filtering happens in memory after decoding.
			selectClause := fmt.Sprintf("SELECT l.`%v`, l.`%v`", pLayer.idFieldname, pLayer.geomFieldname)

			for _, tf := range pLayer.tagFieldnames {
				selectClause += fmt.Sprintf(", l.`%v`", tf)
			}

			qtext = fmt.Sprintf("%v FROM `%v` l WHERE l.`%v` IS NOT NULL ORDER BY l.`%v`", selectClause, pLayer.tablename, pLayer.geomFieldname, pLayer.idFieldname)
		} else {
			// If layer was specified via "tablename" in config, construct query.
			rtreeTablename := fmt.Sprintf("rtree_%v_%s", pLayer.tablename, pLayer.geomFieldname)

			selectClause := fmt.Sprintf("SELECT l.`%v`, l.`%v`", pLayer.idFieldname, pLayer.geomFieldname)

			for _, tf := range pLayer.tagFieldnames {
				selectClause += fmt.Sprintf(", l.`%v`", tf)
			}

			// l - layer table, si - spatial index
			qtext = fmt.Sprintf("%v FROM `%v` l JOIN `%v` si ON l.`%v` = si.id WHERE l.`%v` IS NOT NULL AND !BBOX! ORDER BY l.`%v`", selectClause, pLayer.tablename, rtreeTablename, pLayer.idFieldname, pLayer.geomFieldname, pLayer.idFieldname)

			qtext = replaceTokens(qtext, &pLayer, tile, tileBBox)
		}
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
		skipRow := false

		for i := range cols {
			// check if the context cancelled or timed out
			if ctx.Err() != nil {
				return ctx.Err()
			}
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
				// The MOS layer self-description blob (TLayerSystemInfoRec)
				// is metadata, never a feature: apply it to the MOS config
				// and skip the row.
				if pLayer.geometryFormat == codec.FormatMOS && codec.IsSystemInfoValue(vals[i]) {
					sysInfo, serr := codec.ParseSystemInfoValue(vals[i])
					if serr != nil {
						log.Errorf("error parsing MOS system info: %v", serr)
						return serr
					}
					if aerr := pLayer.mosConfig.ApplySystemInfo(&sysInfo); aerr != nil {
						log.Errorf("error applying MOS system info: %v", aerr)
						return aerr
					}
					skipRow = true
					continue
				}

				h, geo, err := decodeGeometryValue(vals[i], pLayer.geometryFormat, pLayer.mosConfig)
				if err != nil {
					log.Errorf("error decoding geometry: %v", err)
					return err
				}

				if pLayer.srid != 0 {
					feature.SRID = pLayer.srid
				} else if h != nil && h.SRSId() > 0 {
					feature.SRID = uint64(h.SRSId())
				} else if p.srid != 0 {
					feature.SRID = p.srid
				} else {
					feature.SRID = DefaultSRID
				}
				feature.Geometry = geo
			case "minx", "miny", "maxx", "maxy", "min_zoom", "max_zoom":
				// Skip these columns used for bounding box and zoom filtering
				continue

			default:
				// Grab any non-nil, non-id, non-bounding box, & non-geometry column as a tag
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

				default:
					// TODO(arolek): return this error?
					log.Errorf("unexpected type for sqlite column data: %v: %T", cols[i], v)
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

// Close will close the Provider's database connection
func (p *Provider) Close() error {
	return p.db.Close()
}

type GeomTableDetails struct {
	geomFieldname string
	geomType      geom.Geometry
	srid          uint64
	bbox          geom.Extent
}

type GeomColumn struct {
	name         string
	geometryType string
	geom         geom.Geometry // to populate Layer.geomType
	srsId        int
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
