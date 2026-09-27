package proj

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-spatial/proj/support"
)

// IsGeographicDefinition reports whether defn is a supported geographic CRS:
// WGS84 longitude/latitude in degrees, Greenwich meridian, east/north axes.
// Other datums, angular units, grids and coordinate modifiers are deliberately
// rejected rather than silently treated as an identity transformation.
func IsGeographicDefinition(defn string) bool {
	ps, err := support.NewProjString(defn)
	if err != nil {
		return false
	}
	geographic, err := geographicDefinition(ps)
	return geographic && err == nil
}

func geographicDefinition(ps *support.ProjString) (bool, error) {
	geographic := false
	for _, p := range ps.Pairs {
		if p.Key == "proj" {
			switch p.Value {
			case "longlat", "latlong", "lonlat", "latlon":
				geographic = true
			}
		}
	}
	if !geographic {
		return false, nil
	}
	seen := map[string]bool{}
	wgs84 := false
	for _, p := range ps.Pairs {
		valid := false
		if seen[p.Key] {
			return true, fmt.Errorf("geographic CRS has duplicate parameter %q", p.Key)
		}
		seen[p.Key] = true
		switch p.Key {
		case "proj":
			valid = p.Value == "longlat" || p.Value == "latlong" || p.Value == "lonlat" || p.Value == "latlon"
		case "datum", "ellps":
			valid = p.Value == "WGS84"
			wgs84 = wgs84 || valid
		case "no_defs", "wktext":
			valid = p.Value == ""
		case "type":
			valid = p.Value == "crs"
		case "axis":
			valid = p.Value == "enu"
		case "units":
			valid = p.Value == "degrees"
		case "pm":
			valid = p.Value == "greenwich" || zeroGeographicNumber(p.Value)
		case "towgs84":
			values := strings.Split(p.Value, ",")
			valid = len(values) == 3 || len(values) == 7
			for _, value := range values {
				valid = valid && zeroGeographicNumber(value)
			}
		}
		if !valid {
			return true, fmt.Errorf("unsupported geographic CRS parameter %q (only WGS84 longitude/latitude in degrees is supported)", p.Key)
		}
	}
	if !wgs84 {
		return true, fmt.Errorf("geographic CRS requires explicit +datum=WGS84 or +ellps=WGS84")
	}
	return true, nil
}

func zeroGeographicNumber(value string) bool {
	v, err := strconv.ParseFloat(value, 64)
	return err == nil && v == 0
}

func copyGeographicCoordinates(input []float64) ([]float64, error) {
	if len(input)%2 != 0 {
		return nil, fmt.Errorf("input array of lon/lat values must be an even number")
	}
	for i, v := range input {
		if math.IsNaN(v) || math.IsInf(v, 0) || (i%2 == 1 && math.Abs(v) > 90) {
			return nil, fmt.Errorf("invalid geographic coordinate at index %d", i)
		}
	}
	output := make([]float64, len(input))
	copy(output, input)
	return output, nil
}
