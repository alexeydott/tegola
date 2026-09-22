package basic_test

import (
	"math"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/basic"
)

// TestRegisterProj4Defn verifies the crs_defn path: a full PROJ.4 definition
// gets a stable synthetic SRID, round-trips through the web mercator
// conversion helpers and is rejected when not a valid projection.
func TestRegisterProj4Defn(t *testing.T) {
	// EPSG:3395 equivalent definition
	defn := "+proj=merc +lon_0=0 +k_0=1 +x_0=0 +y_0=0 +ellps=WGS84 +datum=WGS84 +units=m +no_defs"

	code, err := basic.RegisterProj4Defn(defn)
	if err != nil {
		t.Fatalf("RegisterProj4Defn returned error: %v", err)
	}

	if code < 340000001 {
		t.Fatalf("expected a synthetic SRID >= 340000001, got %v", code)
	}

	// idempotent registration returns the same code
	code2, err := basic.RegisterProj4Defn(defn)
	if err != nil {
		t.Fatalf("second RegisterProj4Defn returned error: %v", err)
	}
	if code2 != code {
		t.Fatalf("expected idempotent registration to return %v, got %v", code, code2)
	}

	// reverse lookup
	lookup, ok := basic.Proj4DefnSRID(defn)
	if !ok || lookup != code {
		t.Fatalf("Proj4DefnSRID expected (%v, true), got (%v, %v)", code, lookup, ok)
	}

	if _, ok := basic.Proj4DefnSRID("+proj=unknown_defn_42"); ok {
		t.Fatalf("Proj4DefnSRID unexpectedly resolved an unregistered definition")
	}

	// a point in the custom CRS round-trips through web mercator
	src := geom.Point{6835100, 7455000}
	wm, err := basic.ToWebMercator(code, src)
	if err != nil {
		t.Fatalf("ToWebMercator returned error: %v", err)
	}
	wmPt, ok := wm.(geom.Point)
	if !ok {
		t.Fatalf("expected geom.Point, got %T", wm)
	}

	back, err := basic.FromWebMercator(code, wmPt)
	if err != nil {
		t.Fatalf("FromWebMercator returned error: %v", err)
	}
	backPt, ok := back.(geom.Point)
	if !ok {
		t.Fatalf("expected geom.Point, got %T", back)
	}
	if math.Abs(backPt[0]-src[0]) > 0.01 || math.Abs(backPt[1]-src[1]) > 0.01 {
		t.Fatalf("round trip mismatch: got (%v, %v), want (%v, %v)", backPt[0], backPt[1], src[0], src[1])
	}

	// a second, distinct definition must get a distinct synthetic SRID
	defnB := "+proj=etmerc +lat_0=0 +lon_0=61 +k_0=1 +x_0=500000 +y_0=0 +ellps=krass +units=m +no_defs"
	codeB, err := basic.RegisterProj4Defn(defnB)
	if err != nil {
		t.Fatalf("RegisterProj4Defn(defnB) returned error: %v", err)
	}
	if codeB == code {
		t.Fatalf("distinct definitions must not share a synthetic SRID: %v", codeB)
	}

	// invalid definitions must be rejected
	for _, bad := range []string{"", "   ", "not a proj def"} {
		if _, err := basic.RegisterProj4Defn(bad); err == nil {
			t.Fatalf("RegisterProj4Defn(%q) expected an error, got none", bad)
		}
	}

}

func TestRegisterProj4DefnAppliesTowgs84(t *testing.T) {
	defn := "+proj=etmerc +ellps=bessel +towgs84=41,-107.6,-93,0,0,0,0 +x_0=0 +y_0=0 +lon_0=37.5 +k_0=1 +lat_0=55.6666666667 +units=m +no_defs"
	code, err := basic.RegisterProj4Defn(defn)
	if err != nil {
		t.Fatalf("RegisterProj4Defn returned error: %v", err)
	}

	wm, err := basic.ToWebMercator(code, geom.Point{769.792, 19300.763})
	if err != nil {
		t.Fatalf("ToWebMercator returned error: %v", err)
	}
	got := wm.(geom.Point)
	// Independent PROJ result for the same Bessel/towgs84 coordinate.
	want := geom.Point{4175652.829, 7526703.655}
	if math.Abs(got[0]-want[0]) > 0.1 || math.Abs(got[1]-want[1]) > 0.1 {
		t.Fatalf("datum shift mismatch: got (%v, %v), want (%v, %v)", got[0], got[1], want[0], want[1])
	}

	back, err := basic.FromWebMercator(code, got)
	if err != nil {
		t.Fatalf("FromWebMercator returned error: %v", err)
	}
	backPoint := back.(geom.Point)
	if math.Abs(backPoint[0]-769.792) > 0.1 || math.Abs(backPoint[1]-19300.763) > 0.1 {
		t.Fatalf("datum round trip mismatch: got (%v, %v), want (769.792, 19300.763)", backPoint[0], backPoint[1])
	}
}
