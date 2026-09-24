package geometrycodec_test

import (
	"math"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/mos"
	"github.com/go-spatial/tegola/provider/geometrycodec"
)

func TestValidateMOSPrecision(t *testing.T) {
	type tcase struct {
		precision float64
		expectErr bool
	}

	fn := func(tc tcase) func(*testing.T) {
		return func(t *testing.T) {
			err := geometrycodec.ValidateMOSPrecision(tc.precision)
			if tc.expectErr && err == nil {
				t.Errorf("expected error, got nil")
				return
			}
			if !tc.expectErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		}
	}

	tests := map[string]tcase{
		"zero":            {0, false},
		"integer":         {3, false},
		"negative":        {-1, true},
		"fractional":      {2.5, true},
		"nan":             {math.NaN(), true},
		"too large":       {309, true},
		"max valid":       {308, false},
		"infinity":        {math.Inf(1), true},
	}
	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

func TestValidateMVTGeometryFormat(t *testing.T) {
	type tcase struct {
		format      string
		expectedErr string
	}

	fn := func(tc tcase) func(*testing.T) {
		return func(t *testing.T) {
			err := geometrycodec.ValidateMVTGeometryFormat(tc.format)
			if tc.expectedErr == "" {
				if err != nil {
					t.Errorf("unexpected error, Expected nil Got %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error, Expected %v Got nil", tc.expectedErr)
				return
			}
			if err.Error() != tc.expectedErr {
				t.Errorf("incorrect error,\n Expected \n \t%v\n Got \n \t%v", tc.expectedErr, err.Error())
			}
		}
	}

	tests := map[string]tcase{
		"empty is valid":  {"", ""},
		"native is valid": {"native", ""},
		"wkb rejected": {
			"wkb",
			`geometry_format = "wkb" is not supported for MVT providers; use a standard provider (postgis/hana) instead`,
		},
		"wkt rejected": {
			"wkt",
			`geometry_format = "wkt" is not supported for MVT providers; use a standard provider (postgis/hana) instead`,
		},
		"mos rejected": {
			"mos",
			`geometry_format = "mos" is not supported for MVT providers; use a standard provider (postgis/hana) instead`,
		},
	}
	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

func TestResolveMOSConfig(t *testing.T) {
	type tcase struct {
		provider     dict.Dict
		layer        dict.Dict
		expectPrec   float64
		expectPrecSet bool
		expectFactor float64
		expectUnitsSet bool
		expectErr    bool
	}

	fn := func(tc tcase) func(*testing.T) {
		return func(t *testing.T) {
			cfg, err := geometrycodec.ResolveMOSConfig(tc.provider, tc.layer, "test")
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Precision != tc.expectPrec {
				t.Errorf("precision, expected %v got %v", tc.expectPrec, cfg.Precision)
			}
			if cfg.PrecisionSet != tc.expectPrecSet {
				t.Errorf("precision set, expected %v got %v", tc.expectPrecSet, cfg.PrecisionSet)
			}
			if cfg.UnitFactor != tc.expectFactor {
				t.Errorf("unit factor, expected %v got %v", tc.expectFactor, cfg.UnitFactor)
			}
			if cfg.UnitsSet != tc.expectUnitsSet {
				t.Errorf("units set, expected %v got %v", tc.expectUnitsSet, cfg.UnitsSet)
			}
		}
	}

	tests := map[string]tcase{
		"defaults": {
			provider:     dict.Dict{},
			layer:        dict.Dict{},
			expectPrec:   0,
			expectFactor: 1,
		},
		"provider level": {
			provider:       dict.Dict{"mos_precision": 3.0, "mos_units": "mm"},
			expectPrec:     3,
			expectPrecSet:  true,
			expectFactor:   0.001,
			expectUnitsSet: true,
		},
		"layer overrides provider": {
			provider:       dict.Dict{"mos_precision": 3.0, "mos_units": "mm"},
			layer:          dict.Dict{"mos_precision": 0.0, "mos_units": "km"},
			expectPrec:     0,
			expectPrecSet:  true,
			expectFactor:   1000,
			expectUnitsSet: true,
		},
		"layer precision only": {
			provider:     dict.Dict{"mos_precision": 3.0},
			layer:        dict.Dict{"mos_precision": 1.0},
			expectPrec:   1,
			expectPrecSet: true,
			expectFactor: 1,
		},
		"blank units not explicit": {
			provider:     dict.Dict{"mos_units": "  "},
			expectPrec:   0,
			expectFactor: 1,
		},
		"invalid precision": {
			provider:  dict.Dict{"mos_precision": 2.5},
			expectErr: true,
		},
		"invalid units": {
			provider:  dict.Dict{"mos_units": "furlongs"},
			expectErr: true,
		},
		"layer invalid precision": {
			provider:  dict.Dict{},
			layer:     dict.Dict{"mos_precision": -2},
			expectErr: true,
		},
		"nil layer": {
			provider:     dict.Dict{"mos_precision": 2.0},
			expectPrec:   2,
			expectPrecSet: true,
			expectFactor: 1,
		},
	}
	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

// sysInfoBlob builds a minimal valid MapplGIS LayerInfo blob with the given
// precision, map units and projection, exercising the real mos parser.
func sysInfoBlob(t *testing.T, precision int32, mapUnits byte, mapUnitsDefined bool, projection string) []byte {
	t.Helper()
	buf := make([]byte, 128)
	// version marker: byte 5 followed by "Ver 1"
	buf[0] = 5
	copy(buf[1:], "Ver 1")
	// version string is 10 bytes, precision int32 follows at offset 11
	buf[11] = byte(precision)
	buf[12] = byte(precision >> 8)
	buf[13] = byte(precision >> 16)
	buf[14] = byte(precision >> 24)
	buf[15] = 1 // flProjection
	buf[26] = 1 // LayerID
	buf[52] = mapUnits
	if mapUnitsDefined {
		buf[53] = 1 // flMapUnitsDefined
	}
	projSize := int32(len(projection))
	buf[60] = byte(projSize)
	buf[61] = byte(projSize >> 8)
	buf[62] = byte(projSize >> 16)
	buf[63] = byte(projSize >> 24)
	copy(buf[64:], projection)
	return buf
}

func TestSystemInfoApplication(t *testing.T) {
	si := &mos.SystemInfo{
		Precision:       2,
		MapUnits:        mos.UnitsMillimetres,
		MapUnitsDefined: true,
	}

	applied := geometrycodec.DefaultMOSConfig()
	if err := applied.ApplySystemInfo(si); err != nil {
		t.Fatalf("apply system info: %v", err)
	}
	if applied.Precision != 2 {
		t.Errorf("applied precision, expected 2 got %v", applied.Precision)
	}
	if applied.UnitFactor != 0.001 {
		t.Errorf("applied unit factor, expected 0.001 got %v", applied.UnitFactor)
	}

	// explicit config must win over system info
	explicit := geometrycodec.MOSConfig{Precision: 5, UnitFactor: 100, PrecisionSet: true, UnitsSet: true}
	if err := explicit.ApplySystemInfo(si); err != nil {
		t.Fatalf("apply system info: %v", err)
	}
	if explicit.Precision != 5 {
		t.Errorf("explicit precision overwritten: %v", explicit.Precision)
	}
	if explicit.UnitFactor != 100 {
		t.Errorf("explicit units overwritten: %v", explicit.UnitFactor)
	}

	// nil is a no-op
	noop := geometrycodec.DefaultMOSConfig()
	if err := noop.ApplySystemInfo(nil); err != nil {
		t.Errorf("nil system info should be a no-op, got %v", err)
	}
}

func TestSystemInfoBlobDetection(t *testing.T) {
	blob := sysInfoBlob(t, 3, byte('m'), true, "")
	if !geometrycodec.IsSystemInfoValue(blob) {
		t.Fatal("expected blob to be recognized as system info")
	}
	if geometrycodec.IsSystemInfoValue([]byte("LINESTRING(0 0, 1 1)")) {
		t.Fatal("expected WKT text not to be recognized as system info")
	}
	if geometrycodec.IsSystemInfoValue(42) {
		t.Fatal("expected non-blob value not to be recognized as system info")
	}

	si, err := geometrycodec.ParseSystemInfoValue(blob)
	if err != nil {
		t.Fatalf("parse system info: %v", err)
	}
	if si.Precision != 3 {
		t.Errorf("precision, expected 3 got %v", si.Precision)
	}
}

func TestDecodeMOS(t *testing.T) {
	// MOS layout: [type][mod][addflag u16][subobjects u16][points i32]
	// then per-subobject u32 counts and i32 x,y pairs. One subobject, two
	// points: type 2 decodes to a MultiPoint.
	blob := []byte{
		2, 0, 0, 0, // type 2 (point), mod 0, addflag 0
		1, 0, // one subobject
		2, 0, 0, 0, // two points
		2, 0, 0, 0, // subobject point count
		0, 0, 0, 0, 0, 0, 0, 0, // point (0,0)
		10, 0, 0, 0, 10, 0, 0, 0, // point (10,10)
	}

	g, err := geometrycodec.DecodeMOS(blob, geometrycodec.DefaultMOSConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mp, ok := g.(geom.MultiPoint)
	if !ok {
		t.Fatalf("expected MultiPoint, got %T", g)
	}
	pts := mp.Points()
	if len(pts) != 2 || pts[0][0] != 0 || pts[1][0] != 10 {
		t.Errorf("unexpected points: %v", pts)
	}

	if _, err := geometrycodec.DecodeMOS([]byte{0xFF, 0x00}, geometrycodec.DefaultMOSConfig()); err == nil {
		t.Error("expected error for garbage blob")
	}
	if _, err := geometrycodec.DecodeMOS(42, geometrycodec.DefaultMOSConfig()); err == nil {
		t.Error("expected error for non-blob value")
	}
}

func TestDecodeWKT(t *testing.T) {
	g, err := geometrycodec.DecodeWKT("POINT(1 2)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pt, ok := g.(geom.Point)
	if !ok {
		t.Fatalf("expected Point, got %T", g)
	}
	if pt.X() != 1 || pt.Y() != 2 {
		t.Errorf("unexpected point coordinates: %v %v", pt.X(), pt.Y())
	}

	if _, err := geometrycodec.DecodeWKT([]byte("POINT(3 4)")); err != nil {
		t.Errorf("unexpected error for []byte: %v", err)
	}
	if _, err := geometrycodec.DecodeWKT(42); err == nil {
		t.Error("expected error for non-text value")
	}
	if _, err := geometrycodec.DecodeWKT("NOT A WKT"); err == nil {
		t.Error("expected error for invalid WKT")
	}
}

func TestGeometryIntersectsExtent(t *testing.T) {
	extent := geom.NewExtent([2]float64{0, 0}, [2]float64{10, 10})
	pointIn, _ := geometrycodecDecodeWKTPoint(t, "POINT(5 5)")
	pointOut, _ := geometrycodecDecodeWKTPoint(t, "POINT(20 20)")
	pointTouch, _ := geometrycodecDecodeWKTPoint(t, "POINT(10 10)")

	if !geometrycodec.GeometryIntersectsExtent(pointIn, extent) {
		t.Error("point inside extent should intersect")
	}
	if geometrycodec.GeometryIntersectsExtent(pointOut, extent) {
		t.Error("point outside extent should not intersect")
	}
	if !geometrycodec.GeometryIntersectsExtent(pointTouch, extent) {
		t.Error("point on extent boundary should intersect (inclusive bounds)")
	}
	// conservative behavior
	if !geometrycodec.GeometryIntersectsExtent(nil, extent) {
		t.Error("nil geometry should be kept")
	}
	if !geometrycodec.GeometryIntersectsExtent(pointIn, nil) {
		t.Error("nil extent should keep geometry")
	}
}

func geometrycodecDecodeWKTPoint(t *testing.T, wktStr string) (geom.Geometry, error) {
	t.Helper()
	return geometrycodec.DecodeWKT(wktStr)
}

func TestGeomTypeName(t *testing.T) {
	cases := []struct {
		wkt      string
		expected string
	}{
		{"POINT(1 2)", "POINT"},
		{"MULTIPOINT(1 2)", "MULTIPOINT"},
		{"LINESTRING(0 0, 1 1)", "LINESTRING"},
		{"MULTILINESTRING((0 0, 1 1))", "MULTILINESTRING"},
		{"POLYGON((0 0, 1 0, 1 1, 0 0))", "POLYGON"},
		{"MULTIPOLYGON(((0 0, 1 0, 1 1, 0 0)))", "MULTIPOLYGON"},
	}
	for _, tc := range cases {
		g, err := geometrycodec.DecodeWKT(tc.wkt)
		if err != nil {
			t.Fatalf("decode %v: %v", tc.wkt, err)
		}
		if got := geometrycodec.GeomTypeName(g); got != tc.expected {
			t.Errorf("%v: expected %v got %v", tc.wkt, tc.expected, got)
		}
	}
	if got := geometrycodec.GeomTypeName(nil); got != "" {
		t.Errorf("nil geometry: expected empty name got %v", got)
	}
}
