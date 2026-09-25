package gpkg

import (
	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/mos"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
)

type Layer struct {
	name          string
	tablename     string
	tagFieldnames []string
	idFieldname   string
	geomFieldname string
	geomType      geom.Geometry
	// geomTypeExplicit marks that geometry_type was set explicitly in the
	// layer config: the declared type wins over sampled/metadata inference
	// and runtime features of a different type are permitted with a
	// one-time warning.
	geomTypeExplicit bool
	srid             uint64
	bbox             geom.Extent
	sql              string
	// geometryFormat selects how the geometry column is decoded:
	// "" / "gpkg" (default; GeoPackage binary header + WKB), "wkb" (plain
	// WKB without the GeoPackage header), "wkt" (WKT text) or "mos"
	// (opaque MapplGIS binary blob).
	geometryFormat string
	// mosConfig holds the resolved MOS quantization settings used with
	// geometry_format = "mos".
	mosConfig codec.MOSConfig
	// boundFieldnames holds the raw bounds columns (minx/maxx/miny/maxy,
	// in that order) detected on a non-GPKG format table at registration.
	// They act as a coarse SQL filter mirroring the MySQL provider's MOS
	// bounds filter; nil when the table does not carry them.
	boundFieldnames *[4]string
	// isMapplGIS records that the table satisfied the canonical MapplGIS
	// contract (DDL + PK + indexes + OKEY=1 blob) at registration. After
	// NewTileProvider this state is final; tile requests never re-detect.
	isMapplGIS bool
	// mapplSource distinguishes how the layer was detected as MapplGIS:
	// canonical table detection (SystemInfo guaranteed) or the SQL sample
	// probe (no SystemInfo; explicit CRS config required).
	mapplSource codec.MapplGISSource
	// mapplSysInfo is the parsed layer self-description; valid only when
	// mapplSource is MapplGISTableCanonical.
	mapplSysInfo mos.SystemInfo
	// bboxFields holds the resolved bounds field names (layer > provider >
	// defaults) used by the bounds-backed custom-SQL !BBOX! predicate and
	// excluded from feature tags. For tablename layers with detected bounds
	// columns boundFieldnames stays authoritative; bboxFields mirrors it so
	// tag exclusion and the predicate builder share one contract.
	bboxFields codec.BBoxFields
	// crsExplicit records whether srid/crs_defn was set explicitly at
	// provider or layer level, suppressing source-metadata CRS inference.
	crsExplicit bool
}

func (l Layer) Name() string            { return l.name }
func (l Layer) GeomType() geom.Geometry { return l.geomType }
func (l Layer) SRID() uint64            { return l.srid }
func (l Layer) IDFieldName() string     { return l.idFieldname }
func (l Layer) GeomFieldName() string   { return l.geomFieldname }

// IsMapplGIS reports whether the layer was detected as MapplGIS at
// registration time (either table canonical or SQL sample).
func (l Layer) IsMapplGIS() bool { return l.isMapplGIS }

// MapplGISSource reports the detection source. SystemInfo is only guaranteed
// for MapplGISTableCanonical.
func (l Layer) MapplGISSource() codec.MapplGISSource { return l.mapplSource }
