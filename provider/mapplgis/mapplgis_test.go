package mapplgis

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-spatial/tegola/mos"
)

// buildBlob encodes a minimal valid LayerSystemInfo blob with the given
// units and precision.
func buildBlob(precision int, units byte, unitsDefined bool, projection string) []byte {
	buf := make([]byte, 64+len(projection))
	copy(buf, []byte{5, 'V', 'e', 'r', ' ', '1'})
	le := func(off int, v uint32) {
		buf[off] = byte(v)
		buf[off+1] = byte(v >> 8)
		buf[off+2] = byte(v >> 16)
		buf[off+3] = byte(v >> 24)
	}
	le(11, uint32(precision))
	buf[15] = 1 // flProjection
	le(26, 42) // LayerID
	le(60, uint32(len(projection)))
	copy(buf[64:], projection)
	buf[52] = units
	if unitsDefined {
		buf[53] = 1
	}
	return buf
}

func fullColumns() []string {
	return []string{"OKEY", "MUID", "MINX", "MAXX", "MINY", "MAXY", "ObjectStyle", "ObjectType", "LINE"}
}

func fullIndexes() []string {
	return []string{"MUID", "MINX", "MAXX", "MINY", "MAXY", "ObjectType"}
}

func validFetcher() SystemInfoFetcher {
	blob := buildBlob(0, 0, true, "")
	return func() (*mos.SystemInfo, error) {
		si, err := mos.ParseSystemInfo(blob)
		if err != nil {
			return nil, err
		}
		return &si, nil
	}
}

func TestDetect(t *testing.T) {
	t.Run("full contract passes", func(t *testing.T) {
		info, err := Detect(TableMeta{Columns: fullColumns(), PKColumn: "OKEY", IndexedColumns: fullIndexes()}, validFetcher())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !info.IsMapplGIS {
			t.Fatal("expected IsMapplGIS=true")
		}
		if info.SystemInfo.LayerID != 42 {
			t.Errorf("expected LayerID 42, got %d", info.SystemInfo.LayerID)
		}
	})

	t.Run("case-insensitive names", func(t *testing.T) {
		cols := []string{"okey", "muid", "minx", "maxx", "miny", "maxy", "objectstyle", "objecttype", "line"}
		meta := TableMeta{Columns: cols, PKColumn: "Okey", IndexedColumns: []string{"muid", "MinX", "maxx", "MiNy", "MAXY", "objecttype"}}
		info, err := Detect(meta, validFetcher())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !info.IsMapplGIS {
			t.Fatal("expected IsMapplGIS=true with mixed-case metadata")
		}
	})

	t.Run("missing required column", func(t *testing.T) {
		for _, drop := range fullColumns() {
			var cols []string
			for _, c := range fullColumns() {
				if c != drop {
					cols = append(cols, c)
				}
			}
			meta := TableMeta{Columns: cols, PKColumn: "OKEY", IndexedColumns: fullIndexes()}
			info, err := Detect(meta, func() (*mos.SystemInfo, error) {
				t.Fatalf("OKEY=1 query must not run when a required column is missing")
				return nil, nil
			})
			if err != nil {
				t.Fatalf("unexpected error dropping %v: %v", drop, err)
			}
			if info.IsMapplGIS {
				t.Errorf("expected IsMapplGIS=false when %v missing", drop)
			}
		}
	})

	t.Run("OKEY not primary key", func(t *testing.T) {
		meta := TableMeta{Columns: fullColumns(), PKColumn: "MUID", IndexedColumns: fullIndexes()}
		info, err := Detect(meta, validFetcher())
		if err != nil || info.IsMapplGIS {
			t.Errorf("expected not-detected with non-OKEY PK (info=%v err=%v)", info, err)
		}
	})

	t.Run("no primary key", func(t *testing.T) {
		meta := TableMeta{Columns: fullColumns(), PKColumn: "", IndexedColumns: fullIndexes()}
		info, err := Detect(meta, validFetcher())
		if err != nil || info.IsMapplGIS {
			t.Errorf("expected not-detected without PK (info=%v err=%v)", info, err)
		}
	})

	t.Run("each required index dropped", func(t *testing.T) {
		for _, drop := range requiredIndexes {
			var idx []string
			for _, c := range fullIndexes() {
				if !strings.EqualFold(c, drop) {
					idx = append(idx, c)
				}
			}
			meta := TableMeta{Columns: fullColumns(), PKColumn: "OKEY", IndexedColumns: idx}
			info, err := Detect(meta, validFetcher())
			if err != nil {
				t.Fatalf("unexpected error dropping index %v: %v", drop, err)
			}
			if info.IsMapplGIS {
				t.Errorf("expected IsMapplGIS=false without index on %v", drop)
			}
		}
	})

	t.Run("OKEY=1 row missing", func(t *testing.T) {
		meta := TableMeta{Columns: fullColumns(), PKColumn: "OKEY", IndexedColumns: fullIndexes()}
		info, err := Detect(meta, func() (*mos.SystemInfo, error) {
			return nil, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.IsMapplGIS {
			t.Error("expected IsMapplGIS=false when the OKEY=1 row is absent")
		}
	})

	t.Run("invalid LINE blob is a controlled error", func(t *testing.T) {
		meta := TableMeta{Columns: fullColumns(), PKColumn: "OKEY", IndexedColumns: fullIndexes()}
		_, err := Detect(meta, func() (*mos.SystemInfo, error) {
			return nil, fmt.Errorf("not a system info blob")
		})
		if err == nil {
			t.Fatal("expected an error for an unparseable LINE blob, not a silent miss")
		}
	})
}

func TestRequiredColumnListMatchesGeometryField(t *testing.T) {
	found := false
	for _, c := range requiredColumns {
		if strings.EqualFold(c, GeometryField) {
			found = true
		}
	}
	if !found {
		t.Errorf("GeometryField %v must be among the required columns", GeometryField)
	}
}
