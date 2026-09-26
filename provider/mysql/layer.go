package mysql

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
	// layer config: the declared type wins over sampled inference (startup
	// inspection is skipped entirely) and runtime features of a different
	// type are permitted with a one-time warning.
	geomTypeExplicit bool
	// srid is the SRID the layer's data is stored in (the "source SRID").
	// Feature geometries are reprojected from this SRID to Web Mercator
	// on the fly when tiles are served.
	srid uint64
	sql  string
	// geometryFormat mirrors the provider-level geometry_format ("auto",
	// "mysql", "mariadb", "wkb", "wkt", "mos"); used to build !BBOX! for text
	// geometry columns.
	geometryFormat string
	// serverFlavor records the server flavor detected at registration
	// ("mysql" or "mariadb"). MySQL 8 stores geographic SRS geometry
	// latitude-first per its SRS metadata while tegola writes WKT and bbox
	// polygons longitude-first, so geometry constructors for geographic
	// SRIDs must carry 'axis-order=long-lat' on MySQL only (audit P6-3).
	serverFlavor string
	// deferredInspection is set for tile-dependent custom SQL whose geometry
	// type cannot be inferred safely during provider startup.
	deferredInspection bool
	// crsExplicit records whether the provider or layer explicitly selected a
	// CRS. It prevents a runtime MOS system-info row from replacing that CRS.
	crsExplicit bool
	// mosConfig holds the resolved MOS quantization settings (precision,
	// unit factor) plus explicit-config flags that prevent a runtime
	// MapplGIS LayerInfo blob from overriding an explicit provider- or
	// layer-level mos_precision/mos_units setting, including explicit 0.
	mosConfig codec.MOSConfig
	// isMapplGIS records the result of the one-time registration-time
	// MapplGIS table detection (provider/mapplgis contract). It is
	// immutable afterwards: tile requests never repeat the detection.
	isMapplGIS bool
	// mapplSource distinguishes how the layer was detected as MapplGIS:
	// canonical table detection (SystemInfo guaranteed) or the SQL sample
	// probe (no SystemInfo; explicit CRS config required).
	mapplSource codec.MapplGISSource
	// mapplSysInfo is the layer self-description parsed from the OKEY = 1
	// row of a detected MapplGIS table. Valid only when mapplSource is
	// MapplGISTableCanonical.
	mapplSysInfo mos.SystemInfo
	// bboxFields holds the resolved bounds field names (layer > provider >
	// defaults) used by the bounds-backed MOS !BBOX! predicate and excluded
	// from feature tags.
	bboxFields codec.BBoxFields
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
