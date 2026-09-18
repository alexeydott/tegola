package mysql

import "github.com/go-spatial/geom"

type Layer struct {
	name          string
	tablename     string
	tagFieldnames []string
	idFieldname   string
	geomFieldname string
	geomType      geom.Geometry
	// srid is the SRID the layer's data is stored in (the "source SRID").
	// Feature geometries are reprojected from this SRID to Web Mercator
	// on the fly when tiles are served.
	srid          uint64
	sql           string
	// geometryFormat mirrors the provider-level geometry_format ("auto",
	// "mysql", "mariadb", "wkb", "wkt", "mos"); used to build !BBOX! for text
	// geometry columns.
	geometryFormat string
	// mosPrecision is the number of decimal digits MOS blob coordinates
	// carry (mos_precision config or TLayerSystemInfoRec.Precision); 0
	// means integer units.
	mosPrecision float64
	// mosUnitsFactor scales decoded MOS coordinates from the layer's map
	// units (TLayerSystemInfoRec.MapUnits) to metres; defaults to 1.
	mosUnitsFactor float64
}

func (l Layer) Name() string            { return l.name }
func (l Layer) GeomType() geom.Geometry { return l.geomType }
func (l Layer) SRID() uint64            { return l.srid }
func (l Layer) IDFieldName() string     { return l.idFieldname }
func (l Layer) GeomFieldName() string   { return l.geomFieldname }
