package consumer

import (
	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola"
	_ "github.com/go-spatial/tegola/provider/postgis"
)

// The public API must retain upstream geometry type identity.
var Bounds *geom.Extent = tegola.WebMercatorBounds
