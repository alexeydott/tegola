package mysql

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/config"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/mos"
	"github.com/go-spatial/tegola/provider"
)

// wkbPoint takes an x/y pair and returns the full WKB encoding of a 2D Point
// in little-endian byte order.
func wkbPoint(t *testing.T, x, y float64) []byte {
	t.Helper()
	b := []byte{1} // little-endian bom
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
	b = binary.LittleEndian.AppendUint64(b, 1)
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(y))
	return b
}

// TestDecodeMySQLFormat exercises the MySQL native internal geometry layout:
// [4 bytes SRID (little-endian)][1 byte byte-order][WKB (type + body)]
func TestDecodeMySQLFormat(t *testing.T) {
	wkb := wkbPoint(t, 1.5, 2.5)

	// assemble a MySQL-native geometry blob with SRID 3857
	blob := make([]byte, 0, 4+1+len(wkb))
	blob = binary.LittleEndian.AppendUint32(blob, 3857)
	blob = append(blob, 1)
	blob = append(blob, wkb...)

	srid, geo, err := decodeMySQLFormat(blob)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 3857 {
		t.Errorf("expected srid 3857, got %v", srid)
	}
	if geo == nil {
		t.Fatal("expected geometry, got nil")
	}
	if _, ok := geo.(geom.Point); !ok {
		t.Errorf("expected geom.Point, got %T", geo)
	}

	// too-short blob is an error
	if _, _, err := decodeMySQLFormat([]byte{1, 2, 3}); err == nil {
		t.Error("expected error for short blob, got nil")
	}

	// invalid bom is an error
	bad := append([]byte{0, 0, 0, 0}, 9)
	bad = append(bad, wkb...)
	if _, _, err := decodeMySQLFormat(bad); err == nil {
		t.Error("expected error for invalid bom, got nil")
	}
}

// TestDecodeMariaDBFormat exercises the MariaDB native internal geometry
// layout: [1 byte byte-order][4 bytes SRID in that byte order][WKB]. Note
// the SRID follows the byte-order marker, unlike MySQL where it precedes it.
func TestDecodeMariaDBFormat(t *testing.T) {
	wkb := wkbPoint(t, 1.5, 2.5)

	// rebuild cleanly: [bom=1][SRID LE][WKB]
	blob := []byte{1}
	blob = binary.LittleEndian.AppendUint32(blob, 4326)
	blob = append(blob, wkb...)

	srid, geo, err := decodeMariaDBFormat(blob)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 4326 {
		t.Errorf("expected srid 4326, got %v", srid)
	}
	if _, ok := geo.(geom.Point); !ok {
		t.Errorf("expected geom.Point, got %T", geo)
	}

	// big-endian MariaDB blob with SRID 4326
	beBlob := []byte{0} // big-endian bom
	beBlob = binary.BigEndian.AppendUint32(beBlob, 4326)
	beBlob = append(beBlob, wkb...)

	srid, _, err = decodeMariaDBFormat(beBlob)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 4326 {
		t.Errorf("expected srid 4326, got %v", srid)
	}

	// MariaDB 10.7+ axis-order flags stored in the top three SRID bits must
	// be masked off (flags value 1 => SRID | 0x20000000)
	flagged := []byte{1}
	flagged = binary.LittleEndian.AppendUint32(flagged, 4326|0x20000000)
	flagged = append(flagged, wkb...)

	srid, _, err = decodeMariaDBFormat(flagged)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 4326 {
		t.Errorf("expected masked srid 4326, got %v", srid)
	}

	// too-short blob is an error
	if _, _, err := decodeMariaDBFormat([]byte{1}); err == nil {
		t.Error("expected error for short blob, got nil")
	}
}

// TestDecodeGeometryAutoFallback verifies the "auto" format decodes MySQL
// native blobs and falls back to plain WKB when the native layout doesn't fit.
func TestDecodeGeometryAutoFallback(t *testing.T) {
	wkb := wkbPoint(t, 3.0, 4.0)

	// MySQL-native blob
	native := make([]byte, 0)
	native = binary.LittleEndian.AppendUint32(native, 3857)
	native = append(native, 1)
	native = append(native, wkb...)

	srid, geo, err := decodeGeometry(native, GeometryFormatAuto, GeometryFormatMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 3857 {
		t.Errorf("expected srid 3857, got %v", srid)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// plain WKB with the auto flavor should fall back and decode successfully
	// (SRID 0 since plain WKB carries no header)
	srid, geo, err = decodeGeometry(wkb, GeometryFormatAuto, GeometryFormatMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 0 {
		t.Errorf("expected srid 0 for plain WKB, got %v", srid)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// explicit wkb format
	srid, geo, err = decodeGeometry(wkb, GeometryFormatWKB, GeometryFormatMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// unknown format is an error
	if _, _, err := decodeGeometry(wkb, "bogus", GeometryFormatMySQL); err == nil {
		t.Error("expected error for unknown format, got nil")
	}
}

// TestDecodeGeometryWKT verifies WKT text geometry decoding, used for
// geometry stored as text (e.g. a LINESTRING(...) TEXT column).
func TestDecodeGeometryWKT(t *testing.T) {
	const line = "LINESTRING(6832560.11 7372555.90, 6832518.72 7372428.95)"

	// string value
	srid, geo, err := decodeGeometry(line, GeometryFormatWKT, GeometryFormatMariaDB)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 0 {
		t.Errorf("expected srid 0 for WKT, got %v", srid)
	}
	if _, ok := geo.(geom.LineString); !ok {
		t.Errorf("expected geom.LineString, got %T", geo)
	}

	// []byte value (TEXT columns may arrive as []byte depending on driver)
	_, geo, err = decodeGeometry([]byte(line), GeometryFormatWKT, GeometryFormatMariaDB)
	if err != nil {
		t.Fatal(err)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// invalid WKT is an error
	if _, _, err := decodeGeometry("NOT WKT", GeometryFormatWKT, GeometryFormatMariaDB); err == nil {
		t.Error("expected error for invalid WKT, got nil")
	}

	// wrong type is an error
	if _, _, err := decodeGeometry(42, GeometryFormatWKT, GeometryFormatMariaDB); err == nil {
		t.Error("expected error for int geometry value, got nil")
	}

	// auto + MariaDB flavor: WKT text in a blob column falls back through
	// mariadb -> wkb -> wkt and decodes successfully
	_, geo, err = decodeGeometry([]byte(line), GeometryFormatAuto, GeometryFormatMariaDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := geo.(geom.LineString); !ok {
		t.Errorf("expected geom.LineString, got %T", geo)
	}
}

// TestServerFlavorFromVersion checks the VERSION() string parsing.
func TestServerFlavorFromVersion(t *testing.T) {
	cases := []struct {
		version  string
		expected string
	}{
		{"10.11.6-MariaDB-ubu2204", GeometryFormatMariaDB},
		{"8.0.36", GeometryFormatMySQL},
		{"5.7.44-log", GeometryFormatMySQL},
		{"11.4.2-MariaDB", GeometryFormatMariaDB},
	}
	for _, c := range cases {
		if got := serverFlavorFromVersion(c.version); got != c.expected {
			t.Errorf("serverFlavorFromVersion(%q) = %v, expected %v", c.version, got, c.expected)
		}
	}
}

// TestQuoteIdentifier verifies backtick escaping.
func TestQuoteIdentifier(t *testing.T) {
	cases := []struct{ in, want string }{
		{"geom", "`geom`"},
		{"my`geom", "`my``geom`"},
	}
	for _, c := range cases {
		if got := quoteIdentifier(c.in); got != c.want {
			t.Errorf("quoteIdentifier(%q) = %v, expected %v", c.in, got, c.want)
		}
	}
}

// TestReplaceTokens exercises the SQL token replacement using a real tile.
func TestReplaceTokens(t *testing.T) {
	layer := &Layer{
		name:          "testlayer",
		geomFieldname: "geom",
		idFieldname:   "fid",
	}

	tile := provider.NewTile(6, 13, 22, 0, 3857)
	ext, _ := tile.BufferedExtent()

	got := replaceTokens("!BBOX!", layer, tile, ext)
	if !strings.Contains(got, "ST_Intersects(`geom`") {
		t.Errorf("expected ST_Intersects on geom field, got: %v", got)
	}
	if !strings.Contains(got, "ST_GeomFromText('POLYGON((") {
		t.Errorf("expected WKT polygon, got: %v", got)
	}

	got = replaceTokens("!ZOOM!-!Z!-!X!-!Y!", layer, tile, ext)
	if got != "6-6-13-22" {
		t.Errorf("expected 6-6-13-22, got: %v", got)
	}

	got = replaceTokens("!ID_FIELD!-!GEOM_FIELD!", layer, tile, ext)
	if got != "fid-geom" {
		t.Errorf("expected fid-geom, got: %v", got)
	}

	got = replaceTokens("!GEOM_TYPE!", layer, tile, ext)
	if got != "" {
		t.Errorf("expected empty geom type for nil layer geom, got: %v", got)
	}

	// tokens are case-insensitive
	got = replaceTokens("!zoom!-!Zoom!", layer, tile, ext)
	if got != "6-6" {
		t.Errorf("expected case-insensitive zoom tokens (6-6), got: %v", got)
	}

	// numeric tokens must be valid floats
	for _, tok := range []string{config.ScaleDenominatorToken, config.PixelWidthToken, config.PixelHeightToken} {
		got = replaceTokens(tok, layer, tile, ext)
		if _, err := strconv.ParseFloat(got, 64); err != nil {
			t.Errorf("%v: expected a float, got %q", tok, got)
		}
	}
}

// TestConfigValidation runs basic config error paths against NewTileProvider.
// These paths fail before a DB connection is attempted.
func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		config dict.Dict
	}{
		{
			name: "missing host",
			config: dict.Dict{
				"database": "test", "user": "u", "password": "p",
			},
		},
		{
			name: "missing database",
			config: dict.Dict{
				"host": "localhost", "user": "u", "password": "p",
			},
		},
		{
			name: "invalid geometry format",
			config: dict.Dict{
				"host": "localhost", "database": "test",
				"user": "u", "password": "p",
				"geometry_format": "bogus",
			},
		},
	}
	for _, tc := range cases {
		_, err := NewTileProvider(tc.config, nil)
		if err == nil {
			t.Errorf("%v: expected error, got nil", tc.name)
		}
	}
}

// TestApplySystemInfo verifies the sysinfo-driven auto-configuration
// contract: precision and projection are applied when not explicitly
// configured; the blob's MapUnits is informational only and must never
// change the coordinate unit factor (MapplBase stores MOS coordinates
// already dequantized into CRS units).
func TestApplySystemInfo(t *testing.T) {
	projDefn := "+proj=merc +ellps=WGS84 +datum=WGS84 +units=m +no_defs"
	sysInfo := &mos.SystemInfo{
		Precision:       2,
		MapUnits:        mos.UnitsMillimetres,
		MapUnitsDefined: true,
		Projection:      projDefn,
	}

	t.Run("units factor never applied", func(t *testing.T) {
		layer := Layer{name: "l", mosPrecision: 0, mosUnitsFactor: 1}
		if err := applySystemInfo(&layer, dict.Dict{}, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosUnitsFactor != 1 {
			t.Errorf("mosUnitsFactor = %v, want 1 (sysinfo units are informational)", layer.mosUnitsFactor)
		}
	})

	t.Run("precision applied when not explicit", func(t *testing.T) {
		layer := Layer{name: "l", mosPrecision: 0, mosUnitsFactor: 1}
		if err := applySystemInfo(&layer, dict.Dict{}, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosPrecision != 2 {
			t.Errorf("mosPrecision = %v, want 2", layer.mosPrecision)
		}
	})

	t.Run("explicit precision wins", func(t *testing.T) {
		layer := Layer{name: "l", mosPrecision: 4, mosUnitsFactor: 1}
		conf := dict.Dict{"mos_precision": 4.0}
		if err := applySystemInfo(&layer, conf, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosPrecision != 4 {
			t.Errorf("mosPrecision = %v, want 4 (explicit config wins)", layer.mosPrecision)
		}
	})

	t.Run("projection applied when srid not explicit", func(t *testing.T) {
		layer := Layer{name: "l", mosPrecision: 0, mosUnitsFactor: 1}
		if err := applySystemInfo(&layer, dict.Dict{}, sysInfo, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.srid == 0 {
			t.Fatal("expected a synthetic srid to be registered")
		}
		defn, ok := basic.Proj4DefnSRID(projDefn)
		if !ok {
			t.Fatalf("registered srid %v not found by defn lookup", layer.srid)
		}
		if defn != layer.srid {
			t.Errorf("Proj4DefnSRID = %v, want %v", defn, layer.srid)
		}
	})

	t.Run("projection not applied when srid explicit", func(t *testing.T) {
		layer := Layer{name: "l", mosPrecision: 0, mosUnitsFactor: 1, srid: 3857}
		if err := applySystemInfo(&layer, dict.Dict{}, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.srid != 3857 {
			t.Errorf("srid = %v, want 3857 (explicit srid wins)", layer.srid)
		}
	})

	t.Run("nil sysinfo is a no-op", func(t *testing.T) {
		layer := Layer{name: "l", mosPrecision: 3, mosUnitsFactor: 1, srid: 3395}
		if err := applySystemInfo(&layer, dict.Dict{}, nil, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosPrecision != 3 || layer.srid != 3395 {
			t.Errorf("layer modified by nil sysinfo: precision=%v srid=%v", layer.mosPrecision, layer.srid)
		}
	})
}
