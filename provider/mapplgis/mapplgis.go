// Package mapplgis implements the canonical one-time MapplGIS table
// detection contract shared by all standard providers.
//
// A table is recognized as a MapplGIS layer only when ALL of the following
// hold (checked case-insensitively):
//
//  1. Its DDL contains the columns OKEY, MUID, MINX, MAXX, MINY, MAXY,
//     ObjectStyle, ObjectType and LINE, and OKEY is the primary key.
//  2. Indexes exist on MUID, MINX, MAXX, MINY, MAXY and ObjectType.
//  3. The row OKEY = 1 carries a non-empty LINE value that parses as a
//     MapplGIS LayerSystemInfo blob.
//
// Detection runs once per tablename layer at provider registration; tile
// requests never repeat it. Custom SQL layers are never auto-detected.
package mapplgis

import (
	"fmt"
	"strings"

	"github.com/go-spatial/tegola/mos"
)

// GeometryField is the geometry column of a detected MapplGIS table.
const GeometryField = "LINE"

// requiredColumns lists the DDL columns a MapplGIS table must carry.
var requiredColumns = []string{
	"OKEY", "MUID", "MINX", "MAXX", "MINY", "MAXY",
	"OBJECTSTYLE", "OBJECTTYPE", "LINE",
}

// requiredIndexes lists the columns that must be covered by an index.
var requiredIndexes = []string{
	"MUID", "MINX", "MAXX", "MINY", "MAXY", "OBJECTTYPE",
}

// TableMeta is the backend-collected schema metadata fed to Detect. Each
// provider populates it with its own introspection mechanism.
type TableMeta struct {
	// Columns holds every column name of the table in its stored case.
	Columns []string
	// PKColumn is the primary key column name, empty when the table has none.
	PKColumn string
	// IndexedColumns holds every column covered by at least one index.
	IndexedColumns []string
}

// SystemInfoFetcher reads the OKEY = 1 metadata row and parses its LINE
// value as a LayerSystemInfo blob. It returns (nil, nil) when no such row
// exists, the parsed blob when it does, and an error when the row exists
// but cannot be parsed.
type SystemInfoFetcher func() (*mos.SystemInfo, error)

// Info is the immutable detection result stored on the layer.
type Info struct {
	// IsMapplGIS reports whether the table satisfied the full contract.
	IsMapplGIS bool
	// SystemInfo is the parsed layer self-description. Valid only when
	// IsMapplGIS is true.
	SystemInfo mos.SystemInfo
}

// Detect applies the canonical MapplGIS table contract to the given schema
// metadata. It returns (Info{}, nil) when the table is not a MapplGIS layer
// and a controlled error when the metadata row exists but is invalid —
// never a silent true on partial evidence.
func Detect(meta TableMeta, fetch SystemInfoFetcher) (Info, error) {
	columns := make(map[string]struct{}, len(meta.Columns))
	for _, c := range meta.Columns {
		columns[strings.ToLower(c)] = struct{}{}
	}
	for _, req := range requiredColumns {
		if _, ok := columns[strings.ToLower(req)]; !ok {
			return Info{}, nil
		}
	}

	if !strings.EqualFold(meta.PKColumn, "OKEY") {
		return Info{}, nil
	}

	indexed := make(map[string]struct{}, len(meta.IndexedColumns))
	for _, c := range meta.IndexedColumns {
		indexed[strings.ToLower(c)] = struct{}{}
	}
	for _, req := range requiredIndexes {
		if _, ok := indexed[strings.ToLower(req)]; !ok {
			return Info{}, nil
		}
	}

	sysInfo, err := fetch()
	if err != nil {
		return Info{}, fmt.Errorf("mapplgis: reading OKEY=1 layer system info: %w", err)
	}
	if sysInfo == nil {
		// No OKEY = 1 metadata row: the table carries the MapplGIS DDL
		// but not the layer self-description, so the contract fails.
		return Info{}, nil
	}

	return Info{IsMapplGIS: true, SystemInfo: *sysInfo}, nil
}
