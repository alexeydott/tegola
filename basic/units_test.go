package basic

import (
	"math"
	"testing"
)

func TestProjectedMetersPerUnit(t *testing.T) {
	for _, tc := range []struct {
		name, units string
		want        float64
	}{
		{"meters", "+units=m", 1},
		{"default meters", "", 1},
		{"feet", "+units=ft", 0.3048},
		{"US survey feet", "+units=us-ft", 1200.0 / 3937},
		{"kilometers", "+units=km", 1000},
		{"custom units", "+to_meter=2.5", 2.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srid, err := RegisterProj4Defn("+proj=utm +zone=31 +datum=WGS84 " + tc.units)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ProjectedMetersPerUnit(srid)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got-tc.want) > tc.want*1e-12 {
				t.Errorf("meters per unit = %v, want %v", got, tc.want)
			}
		})
	}
	RegisterBuiltinProj4SRIDs()
	for _, srid := range []uint64{3857, 3395, 4087, 32631, 32737, 28407} {
		got, err := ProjectedMetersPerUnit(srid)
		if err != nil || got != 1 {
			t.Errorf("SRID %d: factor %v, error %v; want 1, nil", srid, got, err)
		}
	}
	for _, srid := range []uint64{0, 4326, 999999} {
		if _, err := ProjectedMetersPerUnit(srid); err == nil {
			t.Errorf("SRID %d should reject unknown or non-linear units", srid)
		}
	}
}
