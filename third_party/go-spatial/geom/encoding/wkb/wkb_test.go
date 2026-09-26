package wkb_test

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/geom/encoding/wkb/internal/decode"
)

// leHeader builds a little-endian WKB geometry header (byte order marker +
// geometry type).
func leHeader(typ uint32) []byte {
	b := make([]byte, 5)
	b[0] = 1 // little-endian
	binary.LittleEndian.PutUint32(b[1:], typ)
	return b
}

// hostileCount builds a geometry whose header declares the maximum element
// count but whose body is only a few bytes: decoding must fail with a count
// error instead of allocating slices for the declared count.
func hostileCount(typ uint32) []byte {
	b := leHeader(typ)
	b = binary.LittleEndian.AppendUint32(b, 0xFFFFFFFF)
	// 10 bytes of body: nowhere near enough for any element.
	return append(b, make([]byte, 10)...)
}

// TestDecodeBytesHostileCounts verifies that a huge element/point count in a
// WKB header is rejected against the remaining input size before any
// allocation is made (defence against OOM on corrupt or hostile data).
func TestDecodeBytesHostileCounts(t *testing.T) {
	tests := []struct {
		name string
		typ  uint32
	}{
		{"linestring", wkb.LineString},
		{"polygon", wkb.Polygon},
		{"multipoint", wkb.MultiPoint},
		{"multilinestring", wkb.MultiLineString},
		{"multipolygon", wkb.MultiPolygon},
		{"collection", wkb.Collection},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wkb.DecodeBytes(hostileCount(tc.typ))
			if err == nil {
				t.Fatal("expected an error for a hostile element count, got none")
			}
			var countErr decode.ErrElementCount
			if !errors.As(err, &countErr) {
				t.Fatalf("expected decode.ErrElementCount, got %T: %v", err, err)
			}
			if countErr.Count != 0xFFFFFFFF {
				t.Fatalf("expected the declared count 0xFFFFFFFF in the error, got %v", countErr.Count)
			}
		})
	}
}

// TestDecodeBytesCountExceedsRemaining covers counts that are large but not
// saturated and nested geometries whose inner count is the hostile one.
func TestDecodeBytesCountExceedsRemaining(t *testing.T) {
	// 3 points declared, only 2 present: 3*16 > 32 remaining bytes.
	b := leHeader(wkb.LineString)
	b = binary.LittleEndian.AppendUint32(b, 3)
	b = append(b, make([]byte, 32)...)
	if _, err := wkb.DecodeBytes(b); err == nil {
		t.Fatal("expected an error when the point count exceeds the input size")
	} else {
		var countErr decode.ErrElementCount
		if !errors.As(err, &countErr) {
			t.Fatalf("expected decode.ErrElementCount, got %T: %v", err, err)
		}
	}

	// A multilinestring with one line entry whose own point count is hostile:
	// the outer count (1 element) fits, the inner count must be caught.
	nested := leHeader(wkb.MultiLineString)
	nested = binary.LittleEndian.AppendUint32(nested, 1)
	nested = append(nested, leHeader(wkb.LineString)...)
	nested = binary.LittleEndian.AppendUint32(nested, 0xFFFFFFFF)
	nested = append(nested, make([]byte, 10)...)
	if _, err := wkb.DecodeBytes(nested); err == nil {
		t.Fatal("expected an error for a hostile nested point count")
	} else {
		var countErr decode.ErrElementCount
		if !errors.As(err, &countErr) {
			t.Fatalf("expected decode.ErrElementCount, got %T: %v", err, err)
		}
	}
}

// TestDecodeBytesValidRoundTrip ensures the count guards do not reject valid
// geometries, including empty ones.
func TestDecodeBytesValidRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		g    geom.Geometry
	}{
		{"point", geom.Point{1.5, -2.25}},
		{"empty linestring", geom.LineString{}},
		{"linestring", geom.LineString{{0, 0}, {1, 1}, {2, 4}}},
		{"polygon", geom.Polygon{{{0, 0}, {0, 1}, {1, 1}, {0, 0}}}},
		{"multipoint", geom.MultiPoint{{0, 0}, {1, 1}}},
		{"multilinestring", geom.MultiLineString{{{0, 0}, {1, 1}}}},
		{"multipolygon", geom.MultiPolygon{{{{0, 0}, {0, 1}, {1, 1}, {0, 0}}}}},
		{"collection", geom.Collection{geom.Point{0, 0}, geom.LineString{{0, 0}, {1, 1}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bs, err := wkb.EncodeBytes(tc.g)
			if err != nil {
				t.Fatalf("EncodeBytes: %v", err)
			}
			if _, err := wkb.DecodeBytes(bs); err != nil {
				t.Fatalf("DecodeBytes(EncodeBytes(%v)): %v", tc.name, err)
			}
		})
	}
}

// FuzzDecodeBytes ensures arbitrary input never panics or drives oversized
// allocations through the element/point counters.
func FuzzDecodeBytes(f *testing.F) {
	// Seeds: a valid point, a valid linestring and hostile count headers for
	// every geometry type carrying a count.
	point := leHeader(wkb.Point)
	point = binary.LittleEndian.AppendUint64(point, 0x3FF8000000000000) // 1.5
	point = binary.LittleEndian.AppendUint64(point, 0xC002000000000000) // -2.25
	f.Add(point)

	ln := leHeader(wkb.LineString)
	ln = binary.LittleEndian.AppendUint32(ln, 2)
	ln = append(ln, make([]byte, 32)...)
	f.Add(ln)

	for _, typ := range []uint32{
		wkb.LineString, wkb.Polygon, wkb.MultiPoint,
		wkb.MultiLineString, wkb.MultiPolygon, wkb.Collection,
	} {
		f.Add(hostileCount(typ))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = wkb.DecodeBytes(data)
	})
}
