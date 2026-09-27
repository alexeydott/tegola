package proj

import (
	"math"
	"reflect"
	"testing"
)

func TestGeographicConversion(t *testing.T) {
	const code EPSGCode = 330000001
	defer RemoveCustomProjection(code)
	for _, defn := range []string{
		"+proj=longlat +datum=WGS84 +no_defs",
		"+proj=latlong +ellps=WGS84 +towgs84=0,0,0 +type=crs",
		"+proj=lonlat +datum=WGS84 +ellps=WGS84 +pm=greenwich +axis=enu +units=degrees",
		"+proj=latlon +datum=WGS84 +towgs84=0,0,0,0,0,0,0",
	} {
		CustomProjection(code, defn)
		input := []float64{37.6, 55.7, -120, -45, 180, 90}
		for _, convert := range []func(EPSGCode, []float64) ([]float64, error){Convert, Inverse} {
			got, err := convert(code, input)
			if err != nil || !reflect.DeepEqual(got, input) {
				t.Fatalf("%s: got %v, %v", defn, got, err)
			}
			got[0] = 0
			if input[0] != 37.6 {
				t.Fatal("conversion aliases input")
			}
			for _, invalid := range [][]float64{{1}, {1, 91}, {math.NaN(), 1}, {1, math.Inf(1)}} {
				if _, err := convert(code, invalid); err == nil {
					t.Fatalf("accepted invalid coordinates %v", invalid)
				}
			}
		}
	}
}

func TestGeographicRejectsUnsupportedSemantics(t *testing.T) {
	const code EPSGCode = 330000002
	defer RemoveCustomProjection(code)
	for _, defn := range []string{
		"+proj=longlat",
		"+proj=longlat +datum=NAD27",
		"+proj=longlat +datum=WGS84 +ellps=clrk66",
		"+proj=longlat +datum=WGS84 +towgs84=1,0,0",
		"+proj=longlat +datum=WGS84 +towgs84=NaN,0,0",
		"+proj=longlat +datum=WGS84 +nadgrids=@null",
		"+proj=longlat +datum=WGS84 +units=rad",
		"+proj=longlat +datum=WGS84 +units=m",
		"+proj=longlat +datum=WGS84 +to_meter=1",
		"+proj=longlat +datum=WGS84 +pm=paris",
		"+proj=longlat +datum=WGS84 +axis=neu",
		"+proj=longlat +datum=WGS84 +lon_0=10",
		"+proj=longlat +datum=WGS84 +a=6371000",
		"+proj=longlat +datum=WGS84 +proj=merc",
		"+proj=longlat +datum=WGS84 +datum=NAD27",
	} {
		if IsGeographicDefinition(defn) {
			t.Errorf("classified unsupported definition %s", defn)
		}
		CustomProjection(code, defn)
		if _, err := Convert(code, []float64{10, 50}); err == nil {
			t.Errorf("accepted unsupported definition %s", defn)
		}
		if _, err := Inverse(code, []float64{10, 50}); err == nil {
			t.Errorf("inverse accepted unsupported definition %s", defn)
		}
	}
}
