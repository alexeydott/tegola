package basic

import (
	"fmt"
	"math"

	"github.com/go-spatial/proj/core"
	"github.com/go-spatial/proj/support"
	"github.com/go-spatial/tegola"
)

// ProjectedMetersPerUnit returns the linear unit conversion used by the
// registered projection. Geographic and unknown CRSs return an error.
func ProjectedMetersPerUnit(srid uint64) (float64, error) {
	// FromWebMercator handles 3857 directly, regardless of registrations.
	if srid == tegola.WebMercator {
		return 1, nil
	}
	proj4RegisteredMu.Lock()
	defn, ok := proj4Registered[srid]
	proj4RegisteredMu.Unlock()
	if !ok {
		switch srid {
		case 3395, 4087:
			return 1, nil
		default:
			return 0, fmt.Errorf("no projected unit definition for SRID %d", srid)
		}
	}
	ps, err := support.NewProjString(defn)
	if err != nil {
		return 0, fmt.Errorf("SRID %d units: %w", srid, err)
	}
	// Use the projection engine's unit parser, including +units, +to_meter
	// and their precedence, rather than interpreting PROJ.4 independently.
	sys, _, err := core.NewSystem(ps)
	if err != nil {
		return 0, fmt.Errorf("SRID %d units: %w", srid, err)
	}
	if sys.Right != core.IOUnitsClassic && sys.Right != core.IOUnitsProjected {
		return 0, fmt.Errorf("SRID %d has unsupported non-linear units", srid)
	}
	if sys.ToMeter <= 0 || math.IsNaN(sys.ToMeter) || math.IsInf(sys.ToMeter, 0) {
		return 0, fmt.Errorf("SRID %d has invalid meters per unit: %v", srid, sys.ToMeter)
	}
	return sys.ToMeter, nil
}
