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
