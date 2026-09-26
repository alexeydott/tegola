package basic

import "testing"

// The synthetic SRID allocation must be deterministic across processes: two
// tegola instances loading the same crs_defn must agree on the SRID, because
// SRIDs flow into cache keys, logs and external systems. These tests exercise
// the pure allocation helpers with fresh ownership maps to simulate separate
// processes / config load orders.

const (
	testDefnA = "+proj=merc +lon_0=0 +k_0=1 +x_0=0 +y_0=0 +ellps=WGS84 +datum=WGS84 +units=m +no_defs"
	testDefnB = "+proj=etmerc +lat_0=0 +lon_0=61 +k_0=1 +x_0=500000 +y_0=0 +ellps=krass +units=m +no_defs"
)

// TestDefnFirstChoiceStable locks the hash stability contract: the same
// definition must map to the same SRID in every process and Go version, so the
// expected values are pinned here rather than recomputed.
func TestDefnFirstChoiceStable(t *testing.T) {
	tests := []struct {
		defn string
		want uint64
	}{
		{testDefnA, 407467314},
		{testDefnB, 344822088},
	}
	for _, tc := range tests {
		got := defnFirstChoice(tc.defn)
		if got != tc.want {
			t.Fatalf("defnFirstChoice(%q) = %v, want %v (synthetic SRIDs must be stable across processes)", tc.defn, got, tc.want)
		}
		if got < SyntheticSRIDMin {
			t.Fatalf("defnFirstChoice(%q) = %v, want SRID >= %v", tc.defn, got, SyntheticSRIDMin)
		}
		if got >= SyntheticSRIDMin+defnSRIDSpan {
			t.Fatalf("defnFirstChoice(%q) = %v, outside synthetic SRID space [%v, %v)", tc.defn, got, SyntheticSRIDMin, SyntheticSRIDMin+defnSRIDSpan)
		}
	}
}

// TestResolveDefnSRIDDeterministicAcrossRegistries simulates two fresh
// processes (two fresh registries) registering the same definitions: the
// resulting SRIDs must match pairwise regardless of any counter or load order.
func TestResolveDefnSRIDDeterministicAcrossRegistries(t *testing.T) {
	registry1 := map[uint64]string{}
	registry2 := map[uint64]string{}

	codeA1, ok := resolveDefnSRID(testDefnA, registry1)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnA, registry1) failed")
	}
	// registry2 registers the definitions in the opposite order to also
	// cover load-order independence for the single-definition case.
	codeB2, ok := resolveDefnSRID(testDefnB, registry2)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnB, registry2) failed")
	}
	codeA2, ok := resolveDefnSRID(testDefnA, registry2)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnA, registry2) failed")
	}
	codeB1, ok := resolveDefnSRID(testDefnB, registry1)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnB, registry1) failed")
	}

	if codeA1 != codeA2 {
		t.Fatalf("testDefnA got SRID %v in one registry and %v in another; synthetic SRIDs must be process-deterministic", codeA1, codeA2)
	}
	if codeB1 != codeB2 {
		t.Fatalf("testDefnB got SRID %v in one registry and %v in another; synthetic SRIDs must be process-deterministic", codeB1, codeB2)
	}
	for _, code := range []uint64{codeA1, codeB1} {
		if code < SyntheticSRIDMin {
			t.Fatalf("synthetic SRID %v below minimum %v", code, SyntheticSRIDMin)
		}
	}
}

// TestResolveDefnSRIDDistinctDefinitions verifies distinct definitions never
// share a synthetic SRID within one registry.
func TestResolveDefnSRIDDistinctDefinitions(t *testing.T) {
	registry := map[uint64]string{}
	codeA, ok := resolveDefnSRID(testDefnA, registry)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnA) failed")
	}
	registry[codeA] = testDefnA

	codeB, ok := resolveDefnSRID(testDefnB, registry)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnB) failed")
	}
	if codeB == codeA {
		t.Fatalf("distinct definitions share synthetic SRID %v", codeA)
	}
	if codeB < SyntheticSRIDMin {
		t.Fatalf("synthetic SRID %v below minimum %v", codeB, SyntheticSRIDMin)
	}
}

// TestResolveDefnSRIDCollisionPath verifies the deterministic probe step: when
// a definition's first choice is owned by a different definition, allocation
// walks forward with a fixed step until a free slot is found and re-resolving
// the same definition in the same registry is idempotent.
func TestResolveDefnSRIDCollisionPath(t *testing.T) {
	start := defnFirstChoice(testDefnA)
	// Another definition already owns A's first choice and the next slot;
	// A must land on the second probe.
	registry := map[uint64]string{
		start: "some other definition",
	}
	codeA, ok := resolveDefnSRID(testDefnA, registry)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnA) failed")
	}
	want := start + defnSRIDProbeStep
	if codeA != want {
		t.Fatalf("collision probe gave SRID %v, want %v (start %v + step %v)", codeA, want, start, defnSRIDProbeStep)
	}

	// idempotent: once A owns the probed slot, resolving it again returns it.
	registry[codeA] = testDefnA
	codeAgain, ok := resolveDefnSRID(testDefnA, registry)
	if !ok {
		t.Fatal("resolveDefnSRID(testDefnA) re-run failed")
	}
	if codeAgain != codeA {
		t.Fatalf("re-resolving testDefnA gave %v, want %v", codeAgain, codeA)
	}
}

// ---- P6-21: validation probe point ----

// TestProj4ProbePoint locks the probe-point selection used by
// isSupportedProj4: definitions declaring a projection center validate at that
// center (so definitions whose domain excludes the historical fixed point
// (10, 50) are not incorrectly rejected), UTM +zone definitions validate at
// their central meridian, and definitions without a declared center fall back
// to the fixed point.
func TestProj4ProbePoint(t *testing.T) {
	tests := []struct {
		name   string
		proj4  string
		lonLat [2]float64
	}{
		{"center via lon_0 and lat_0", "+proj=aea +lat_1=-30 +lat_2=-60 +lat_0=-45 +lon_0=170 +datum=WGS84", [2]float64{170, -45}},
		{"zero center is still a declared center", "+proj=merc +lon_0=0 +lat_0=0 +ellps=WGS84", [2]float64{0, 0}},
		{"lat_0 without lon_0", "+proj=merc +lat_0=50 +ellps=WGS84", [2]float64{0, 50}},
		{"lon_0 without lat_0", "+proj=merc +lon_0=37.5 +ellps=WGS84", [2]float64{37.5, 0}},
		{"utm zone central meridian", "+proj=utm +zone=1 +datum=WGS84", [2]float64{-177, 0}},
		{"utm zone 33", "+proj=utm +zone=33 +datum=WGS84", [2]float64{15, 0}},
		{"utm zone 60", "+proj=utm +zone=60 +datum=WGS84", [2]float64{177, 0}},
		{"explicit lon_0 wins over zone", "+proj=utm +zone=33 +lon_0=10 +datum=WGS84", [2]float64{10, 0}},
		{"longitude wrap east", "+proj=merc +lon_0=190 +lat_0=10 +ellps=WGS84", [2]float64{-170, 10}},
		{"longitude wrap west", "+proj=merc +lon_0=-190 +lat_0=10 +ellps=WGS84", [2]float64{170, 10}},
		{"no center falls back to fixed point", "+proj=merc +ellps=WGS84", [2]float64{10, 50}},
		{"unparseable center falls back", "+proj=merc +lon_0=39,5 +ellps=WGS84", [2]float64{10, 50}},
	}
	for _, tc := range tests {
		lon, lat := proj4ProbePoint(tc.proj4)
		if lon != tc.lonLat[0] || lat != tc.lonLat[1] {
			t.Errorf("proj4ProbePoint(%q) = (%v, %v), want (%v, %v)", tc.proj4, lon, lat, tc.lonLat[0], tc.lonLat[1])
		}
	}
}

// TestIsSupportedProj4UsesDeclaredCenter is the behavioral P6-21 regression:
// +proj=merc +lat_0=90 declares its center at the pole, which merc cannot
// project. The historical fixed probe (10, 50) validated it as usable even
// though its own declared center lies outside the projection's domain; probing
// the center must reject it.
func TestIsSupportedProj4UsesDeclaredCenter(t *testing.T) {
	broken := "+proj=merc +a=6370997 +b=6370997 +lat_0=90 +lon_0=0 +units=m +no_defs"
	if isSupportedProj4(broken) {
		t.Fatalf("definition %q declares an unprojectable center (0, 90) and must be rejected", broken)
	}
	// the same definition with a projectable center stays accepted
	good := "+proj=merc +a=6370997 +b=6370997 +lat_0=89 +lon_0=0 +units=m +no_defs"
	if !isSupportedProj4(good) {
		t.Fatalf("definition %q is valid and must be accepted", good)
	}
}

// TestBuiltinProj4DefnsValidate guards P6-21/P6-8: every built-in table
// definition must survive validation unchanged after the probe point and
// definition checks changed.
func TestBuiltinProj4DefnsValidate(t *testing.T) {
	for srid, def := range builtinProj4SRIDs {
		if !isSupportedProj4(def) {
			t.Errorf("builtin EPSG:%d definition %q does not validate", srid, def)
		}
	}
}

// TestIsSupportedProj4StillRejectsBrokenDefns keeps genuinely broken
// definitions rejected: unsupported projections and the vendored airy /
// august operations, whose Inverse panics.
func TestIsSupportedProj4StillRejectsBrokenDefns(t *testing.T) {
	for _, def := range []string{
		"+proj=stere +lat_0=46",
		"+proj=airy +lat_0=49 +lon_0=-2 +ellps=WGS84",
		"+proj=august +lat_0=45 +lon_0=0 +a=6378137 +b=6378137",
	} {
		if isSupportedProj4(def) {
			t.Errorf("broken definition %q must be rejected", def)
		}
	}
}

// ---- P6-8: finite output and round-trip defense ----

// TestIsSupportedProj4RejectsNonFiniteOutput guards P6-8: definitions whose
// forward operation overflows to Inf/NaN without returning an error were
// accepted as usable. The scale factor below is finite (so parameter parsing
// accepts it) but overflows the forward easting to +Inf.
func TestIsSupportedProj4RejectsNonFiniteOutput(t *testing.T) {
	overflow := "+proj=merc +a=6370997 +b=6370997 +lon_0=0 +lat_0=50 +k_0=1.79e308 +units=m +no_defs"
	if isSupportedProj4(overflow) {
		t.Fatalf("definition %q produces non-finite output and must be rejected", overflow)
	}
}

// TestIsSupportedProj4RejectsBrokenRoundTrip guards P6-8: the inverse of the
// forward probe output must return close to the probe point. The false
// northing below shifts the forward output so far that the inverse loses the
// entire latitude (round trip returns (0, 0) for probe point (0, 50)) yet
// both operations return no error.
func TestIsSupportedProj4RejectsBrokenRoundTrip(t *testing.T) {
	broken := "+proj=merc +a=6370997 +b=6370997 +lon_0=0 +lat_0=50 +y_0=1e308 +units=m +no_defs"
	if isSupportedProj4(broken) {
		t.Fatalf("definition %q does not round-trip its probe point and must be rejected", broken)
	}
	// the same definition with a sane false northing round-trips fine
	good := "+proj=merc +a=6370997 +b=6370997 +lon_0=0 +lat_0=50 +y_0=1000 +units=m +no_defs"
	if !isSupportedProj4(good) {
		t.Fatalf("definition %q is valid and must be accepted", good)
	}
}
