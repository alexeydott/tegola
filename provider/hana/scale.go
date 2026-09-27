package hana

import (
	"fmt"
	"math"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/slippy"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/provider"
)

// tileScale uses unbuffered source-CRS bounds, not the MVT coordinate extent.
// provider.Tile has no pixel-size accessor; custom tiles can optionally expose
// PixelSize. The built-in slippy tiles use slippy.DefaultTileSize.
func tileScale(tile provider.Tile, layerSRID uint64) (width, height, denominator float64, err error) {
	extent, tileSRID, err := getTileExtent(tile, false)
	if err != nil {
		return 0, 0, 0, err
	}
	pixelWidth, pixelHeight := uint(slippy.DefaultTileSize), uint(slippy.DefaultTileSize)
	if sized, ok := tile.(interface{ PixelSize() (uint, uint) }); ok {
		pixelWidth, pixelHeight = sized.PixelSize()
	}
	if pixelWidth == 0 || pixelHeight == 0 {
		return 0, 0, 0, fmt.Errorf("tile pixel dimensions must be positive")
	}

	tileSRID, layerSRID = transformSRID(tileSRID), transformSRID(layerSRID)
	sourceExtent := extent
	switch {
	case tileSRID == layerSRID:
	case tileSRID == tegola.WebMercator:
		sourceExtent, err = basic.FromWebMercatorExtent(layerSRID, extent)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("converting scale extent to SRID %d: %w", layerSRID, err)
		}
	default:
		return 0, 0, 0, fmt.Errorf("scale extent CRS %d must be WebMercator or match layer CRS %d", tileSRID, layerSRID)
	}
	if err := validateTileExtent(sourceExtent); err != nil {
		return 0, 0, 0, err
	}
	width = (sourceExtent.MaxX() - sourceExtent.MinX()) / float64(pixelWidth)
	height = (sourceExtent.MaxY() - sourceExtent.MinY()) / float64(pixelHeight)

	var metersPerUnit float64
	if layerSRID == tegola.WGS84 {
		if sourceExtent.MinY() < -90 || sourceExtent.MaxY() > 90 ||
			sourceExtent.MaxX()-sourceExtent.MinX() > 360+1e-9 {
			return 0, 0, 0, fmt.Errorf("scale extent is outside geographic CRS bounds")
		}
		center := geom.Point{(extent.MinX() + extent.MaxX()) / 2, (extent.MinY() + extent.MaxY()) / 2}
		if tileSRID == tegola.WebMercator {
			converted, cerr := basic.FromWebMercator(tegola.WGS84, center)
			if cerr != nil {
				return 0, 0, 0, fmt.Errorf("converting scale latitude: %w", cerr)
			}
			var ok bool
			center, ok = converted.(geom.Point)
			if !ok {
				return 0, 0, 0, fmt.Errorf("expected scale center point, got %T", converted)
			}
		}
		// Spherical parallel-arc approximation at the tile center latitude,
		// using the WGS84 semi-major radius. This is not a geodesic chord.
		metersPerUnit = 6378137 * math.Pi / 180 * math.Cos(center[1]*math.Pi/180)
	} else {
		metersPerUnit, err = basic.ProjectedMetersPerUnit(layerSRID)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	// OGC standardized rendering pixel: 0.28 mm. Projected CRSs use map
	// distance, without undoing projection distortion (including Mercator).
	denominator = width * metersPerUnit / 0.00028
	for _, v := range []float64{width, height, denominator} {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, 0, 0, fmt.Errorf("invalid scale for SRID %d: pixel width %v, height %v, denominator %v", layerSRID, width, height, denominator)
		}
	}
	return width, height, denominator, nil
}
