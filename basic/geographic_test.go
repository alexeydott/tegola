package basic

import (
	"math"
	"testing"

	"github.com/go-spatial/geom"
)

func TestRegisterGeographicDefinition(t *testing.T) {
	const defn = "+proj=longlat +datum=WGS84 +no_defs +type=crs"
	code, err := RegisterProj4Defn(defn)
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := RegisterProj4Defn(defn); err != nil || repeated != code {
		t.Fatalf("registration is not stable: %d, %v", repeated, err)
	}
	const explicit = 910042
	if err := RegisterProj4SRID(explicit, defn); err != nil {
		t.Fatal(err)
	}
	for _, srid := range []uint64{4326, code, explicit} {
		if !IsGeographicSRID(srid) {
			t.Fatalf("SRID %d is not recognized as geographic", srid)
		}
		if _, err := ProjectedMetersPerUnit(srid); err == nil {
			t.Fatalf("SRID %d incorrectly has linear units", srid)
		}
		for _, point := range []geom.Point{{37.6, 55.7}, {-73.9, 40.7}, {151, -33}, {0, 0}} {
			want, err := ToWebMercator(4326, point)
			if err != nil {
				t.Fatal(err)
			}
			projected, err := ToWebMercator(srid, point)
			if err != nil || projected != want {
				t.Fatalf("SRID %d: projection %v, want %v, error %v", srid, projected, want, err)
			}
			back, err := FromWebMercator(srid, projected)
			if err != nil {
				t.Fatal(err)
			}
			got := back.(geom.Point)
			if math.Abs(got[0]-point[0]) > 1e-9 || math.Abs(got[1]-point[1]) > 1e-9 {
				t.Fatalf("SRID %d: round trip %v, want %v", srid, got, point)
			}
		}
	}
	if err := RegisterProj4SRID(explicit, "+proj=utm +zone=31 +datum=WGS84"); err != nil {
		t.Fatal(err)
	}
	if IsGeographicSRID(explicit) || IsGeographicSRID(3857) || IsGeographicSRID(0) {
		t.Fatal("projected or unknown SRID classified as geographic")
	}
	projected, err := FromWebMercator(explicit, geom.Point{0, 0})
	if err != nil || math.Abs(projected.(geom.Point)[0]-166021.443) > 0.01 {
		t.Fatalf("replaced geographic conversion retained stale cache: %v, %v", projected, err)
	}
	if err := RegisterProj4SRID(explicit, defn); err != nil {
		t.Fatal(err)
	}
	geographic, err := FromWebMercator(explicit, geom.Point{0, 0})
	if err != nil || math.Abs(geographic.(geom.Point)[0]) > 1e-9 || math.Abs(geographic.(geom.Point)[1]) > 1e-9 {
		t.Fatalf("replaced projected conversion retained stale cache: %v, %v", geographic, err)
	}
}

func TestRegisterUnsupportedGeographicDefinition(t *testing.T) {
	for _, defn := range []string{
		"+proj=longlat +datum=NAD27",
		"+proj=longlat +datum=WGS84 +units=m",
		"+proj=longlat +datum=WGS84 +towgs84=1,0,0",
		"+proj=longlat +datum=WGS84 +pm=paris",
	} {
		if _, err := RegisterProj4Defn(defn); err == nil {
			t.Errorf("accepted unsupported geographic definition %q", defn)
		}
		if err := RegisterProj4SRID(910043, defn); err == nil {
			t.Errorf("accepted unsupported explicit geographic definition %q", defn)
		}
	}
}
