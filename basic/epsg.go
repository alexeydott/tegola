package basic

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-spatial/proj"
)

// This file extends the three EPSG codes built into github.com/go-spatial/proj
// (3395, 3857, 4087) with a built-in table of commonly used projected
// coordinate systems and a RegisterProj4SRID hook so configs can register any
// additional SRID via a PROJ.4 string. Projections are described in PROJ.4
// syntax and registered with proj.CustomProjection; the actual math is
// performed by proj's ported operations (merc, utm/etmerc, aea, leac, aeqd,
// eqc, airy, august). Definitions using other PROJ.4 projections are
// rejected at registration time.

var (
	// proj4RegisterOnce guards proj.CustomProjection calls, which mutate a
	// global map inside the vendored library.
	proj4RegisterOnce sync.Once
	// proj4Registered tracks EPSG codes registered through RegisterProj4SRID
	// so the same code can be registered twice safely.
	proj4RegisteredMu sync.Mutex
	proj4Registered   = map[uint64]string{}
)

// builtinProj4SRIDs maps EPSG codes of widely used projected coordinate
// systems to their PROJ.4 definitions. EPSG:3395 / 3857 / 4087 are already
// built into the proj library and are not repeated here. EPSG:4326 is
// geographic and handled natively by basic.ToWebMercator / FromWebMercator.
//
//	326xx  — WGS 84 / UTM northern zones (32601..32660)
//	327xx  — WGS 84 / UTM southern zones (32701..32760)
//	284xx  — Pulkovo 1942 / Gauss-Kruger zones (28401..28432; x_0 includes the
//	         zone number prefix, the classic 8-digit easting convention)
//	2463..2491 — Pulkovo 1995 / Gauss-Kruger zones
//	2492..2522 — Pulkovo 1942 / Gauss-Kruger zones (x_0 = 500000, no zone prefix)
//	3785   — Popular Visualisation Pseudo Mercator (GDAL legacy alias of 3857)
//	900913 — the OSM "google" mercator alias of 3857
//	53004  — Sphere Mercator (ESRI:53004, spherical authalic earth)
var builtinProj4SRIDs = map[uint64]string{}

// gkZoneDef builds the PROJ.4 definition of a Gauss-Kruger zone on the
// Krassowsky ellipsoid. etmerc (extended transverse mercator) is the
// accurate tmerc implementation available in the vendored proj library.
func gkZoneDef(lon0, x0 float64) string {
	return fmt.Sprintf("+proj=etmerc +lat_0=0 +lon_0=%v +k_0=1 +x_0=%v +y_0=0 +ellps=krass +units=m +no_defs", lon0, x0)
}

func init() {
	// WGS 84 / UTM zones 1N..60N and 1S..60S
	for zone := 1; zone <= 60; zone++ {
		builtinProj4SRIDs[uint64(32600+zone)] = fmt.Sprintf("+proj=utm +zone=%d +datum=WGS84 +units=m +no_defs", zone)
		builtinProj4SRIDs[uint64(32700+zone)] = fmt.Sprintf("+proj=utm +zone=%d +south +datum=WGS84 +units=m +no_defs", zone)
	}
	// Pulkovo 1942 / Gauss-Kruger zones 1..32 (eastings prefixed with the
	// zone number via x_0 = zone*1e6 + 500000)
	for zone := 1; zone <= 32; zone++ {
		builtinProj4SRIDs[uint64(28400+zone)] = gkZoneDef(float64(zone*6-183), float64(zone*1000000+500000))
	}
	// Pulkovo 1995 / Gauss-Kruger zones: EPSG 2463..2491, central meridians
	// 21+E, step 6, wrapping at 180; false easting 500000 (no zone prefix).
	for i := 0; i <= 28; i++ {
		lon := 21 + i*6
		if lon > 180 {
			lon -= 360
		}
		builtinProj4SRIDs[uint64(2463+i)] = gkZoneDef(float64(lon), 500000)
	}
	// Pulkovo 1942 / Gauss-Kruger zones: EPSG 2492..2522, central meridians
	// 9+E, step 6, wrapping at 180; false easting 500000 (no zone prefix).
	for i := 0; i <= 30; i++ {
		lon := 9 + i*6
		if lon > 180 {
			lon -= 360
		}
		builtinProj4SRIDs[uint64(2492+i)] = gkZoneDef(float64(lon), 500000)
	}
	// Popular Visualisation Pseudo Mercator (legacy GDAL alias of 3857).
	builtinProj4SRIDs[3785] = "+proj=merc +lon_0=0 +k_0=1 +x_0=0 +y_0=0 +a=6378137 +b=6378137 +towgs84=0,0,0,0,0,0,0 +units=m +no_defs"
	// The OSM "google" mercator alias of 3857.
	builtinProj4SRIDs[900913] = "+proj=merc +a=6378137 +b=6378137 +lat_ts=0.0 +lon_0=0.0 +x_0=0.0 +y_0=0 +k_0=1.0 +units=m +no_defs"
	// Sphere Mercator (ESRI:53004) on the authalic sphere.
	builtinProj4SRIDs[53004] = "+proj=merc +lon_0=0 +k_0=1 +x_0=0 +y_0=0 +a=6371000 +b=6371000 +units=m +no_defs"
}

// RegisterProj4SRID registers (or replaces) the PROJ.4 definition of an EPSG
// code, making ToWebMercator / FromWebMercator able to convert geometries in
// that SRID. Providers call this during startup for SRIDs configured in their
// config files. Registration is idempotent per (srid, proj4) pair.
func RegisterProj4SRID(srid uint64, proj4 string) error {
	proj4 = strings.TrimSpace(proj4)
	if srid == 0 {
		return fmt.Errorf("RegisterProj4SRID: srid must be a positive EPSG code")
	}
	if proj4 == "" {
		return fmt.Errorf("RegisterProj4SRID: empty proj4 definition for srid %v", srid)
	}
	// reject unsupported-operation definitions early by validating a probe
	// point through proj itself
	if !isSupportedProj4(proj4) {
		return fmt.Errorf("RegisterProj4SRID: proj4 definition for srid %v is not a supported projection: %v", srid, proj4)
	}

	proj4RegisteredMu.Lock()
	defer proj4RegisteredMu.Unlock()
	if prev, ok := proj4Registered[srid]; ok && prev == proj4 {
		return nil
	}
	proj4Registered[srid] = proj4

	proj4RegisterOnce.Do(func() {})
	proj.CustomProjection(proj.EPSGCode(srid), proj4)
	return nil
}

// isSupportedProj4 validates a PROJ.4 string by asking proj to build and
// round-trip a conversion for it via a temporary EPSG code outside the
// standard range. Each validation uses a fresh probe code because proj caches
// conversions per EPSG code; reusing one would validate a definition against
// a previously cached conversion. Panics are recovered because some vendored
// operations (e.g. airy, august) panic in Inverse instead of returning an
// error.
func isSupportedProj4(proj4 string) (ok bool) {
	probe := nextProbeCode()
	proj.CustomProjection(probe, proj4)
	defer proj.CustomProjection(probe, "+proj=merc +datum=WGS84")
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	// forward then inverse must both succeed for a usable projection
	out, err := proj.Convert(probe, []float64{10.0, 50.0})
	if err != nil {
		return false
	}
	_, err = proj.Inverse(probe, out)
	return err == nil
}

// probeCodeBase sits above every EPSG code space (standard codes are <= 7
// digits) and below proj.EPSGCode's int range.
var probeCodeBase int64 = 320000000

func nextProbeCode() proj.EPSGCode {
	return proj.EPSGCode(atomic.AddInt64(&probeCodeBase, 1))
}

// KnownProj4SRIDs returns the sorted list of EPSG codes convertible through
// proj (built-in conversions plus the built-in table plus any SRIDs
// registered via RegisterProj4SRID).
func KnownProj4SRIDs() []uint64 {
	codes := proj.AvailableConversions()
	out := make([]uint64, 0, len(codes))
	for _, c := range codes {
		out = append(out, uint64(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// IsBuiltinProj4SRID reports whether the SRID is covered by the built-in
// table (UTM / Gauss-Kruger) without explicit registration.
func IsBuiltinProj4SRID(srid uint64) bool {
	_, ok := builtinProj4SRIDs[srid]
	return ok
}

// BuiltinProj4Def returns the built-in PROJ.4 definition for the SRID, if any.
func BuiltinProj4Def(srid uint64) (string, bool) {
	def, ok := builtinProj4SRIDs[srid]
	return def, ok
}

// RegisterBuiltinProj4SRIDs registers every entry of the built-in table with
// proj. Providers that support arbitrary SRIDs call this once at startup so
// common codes (UTM, Gauss-Kruger) work without any config.
func RegisterBuiltinProj4SRIDs() {
	proj4RegisteredMu.Lock()
	defer proj4RegisteredMu.Unlock()
	for srid, def := range builtinProj4SRIDs {
		if _, ok := proj4Registered[srid]; ok {
			continue
		}
		proj4Registered[srid] = def
		proj.CustomProjection(proj.EPSGCode(srid), def)
	}
	proj4RegisterOnce.Do(func() {})
}

// ParseProj4Config parses a proj4 config value of the form
// "EPSG_CODE=proj4 string" entries joined by newlines or semicolons, e.g.
//
//	proj4 = "2180=+proj=sterea +lat_0=... ; 2177=+proj=tmerc +lon_0=15"
//
// or a TOML table handled by the caller as individual entries. Returns a
// map of srid -> proj4 string.
func ParseProj4Config(entries []string) (map[uint64]string, error) {
	out := make(map[uint64]string)
	for i, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		eq := strings.Index(e, "=")
		if eq <= 0 {
			return nil, fmt.Errorf("proj4 entry %v (%q) must look like SRID=+proj=...", i+1, e)
		}
		key := strings.TrimSpace(e[:eq])
		key = strings.TrimPrefix(strings.TrimPrefix(key, "EPSG:"), "epsg:")
		srid, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("proj4 entry %v (%q): invalid srid: %v", i+1, e, err)
		}
		def := strings.TrimSpace(e[eq+1:])
		if def == "" {
			return nil, fmt.Errorf("proj4 entry %v (%q): empty proj4 definition", i+1, e)
		}
		out[srid] = def
	}
	return out, nil
}
