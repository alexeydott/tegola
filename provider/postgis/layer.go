package postgis

import (
	"github.com/go-spatial/geom"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
)

// layer holds information about a query.
type Layer struct {
	// The Name of the layer
	name string
	// The SQL to use when querying PostGIS for this layer
	sql string
	// The ID field name, this will default to 'gid' if not set to something other then empty string.
	idField string
	// The Geometery field name, this will default to 'geom' if not set to something other then empty string.
	geomField string
	// GeomType is the the type of geometry returned from the SQL
	geomType geom.Geometry
	// The SRID that the data in the table is stored in. This will default to WebMercator
	srid uint64
	// geometryFormat selects how the geometry column is decoded:
	// "" (default; PostGIS native geometry returned via ST_AsBinary),
	// "wkb" (plain WKB column read raw, no ST_AsBinary), "wkt" (WKT text)
	// or "mos" (opaque MapplBase binary blob read raw).
	geometryFormat string
	// mosConfig holds the resolved MOS quantization settings used with
	// geometry_format = "mos".
	mosConfig codec.MOSConfig
}

func (l Layer) Name() string {
	return l.name
}

func (l Layer) GeomType() geom.Geometry {
	return l.geomType
}

func (l Layer) SRID() uint64 {
	return l.srid
}

func (l Layer) GeomFieldName() string {
	return l.geomField
}

func (l Layer) IDFieldName() string {
	return l.idField
}
