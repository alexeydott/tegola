package crsconfig

import (
	"testing"

	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/dict"
)

func TestResolveProvider(t *testing.T) {
	type tcase struct {
		name           string
		config         dict.Dicter
		fallback       int
		expectedSRID   int
		expectedExpl   bool
		expectErr      bool
	}

	fn := func(t *testing.T, tc tcase) {
		t.Helper()
		got, err := ResolveProvider(tc.config, tc.fallback)
		if tc.expectErr {
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			return
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.SRID != tc.expectedSRID {
			t.Fatalf("expected srid %v, got %v", tc.expectedSRID, got.SRID)
		}
		if got.Explicit != tc.expectedExpl {
			t.Fatalf("expected explicit %v, got %v", tc.expectedExpl, got.Explicit)
		}
	}

	basic.RegisterBuiltinProj4SRIDs()

	fn(t, tcase{
		name:         "nil config falls back",
		config:       nil,
		fallback:     4326,
		expectedSRID: 4326,
		expectedExpl: false,
	})
	fn(t, tcase{
		name:         "empty config falls back",
		config:       dict.Dict{},
		fallback:     3857,
		expectedSRID: 3857,
		expectedExpl: false,
	})
	fn(t, tcase{
		name:         "explicit numeric srid",
		config:       dict.Dict{"srid": 32634},
		fallback:     3857,
		expectedSRID: 32634,
		expectedExpl: true,
	})
	fn(t, tcase{
		name:         "explicit zero srid is still explicit",
		config:       dict.Dict{"srid": 0},
		fallback:     3857,
		expectedSRID: 0,
		expectedExpl: true,
	})
	fn(t, tcase{
		name: "crs_defn wins over srid",
		config: dict.Dict{
			"srid":     4326,
			"crs_defn": "+proj=utm +zone=34 +datum=WGS84 +units=m +no_defs",
		},
		fallback:     3857,
		expectedSRID: int(basic.SyntheticSRIDMin),
		expectedExpl: true,
	})
	fn(t, tcase{
		name:         "invalid crs_defn errors",
		config:       dict.Dict{"crs_defn": "+proj=notaproj +datum=WGS84"},
		fallback:     3857,
		expectedSRID: 3857,
		expectErr:    true,
	})
	fn(t, tcase{
		name:         "wrong srid type errors",
		config:       dict.Dict{"srid": "not a number"},
		fallback:     3857,
		expectedSRID: 3857,
		expectErr:    true,
	})
}

func TestResolveLayer(t *testing.T) {
	// layer resolution is the same contract applied on top of a fallback that
	// already went through provider resolution / source auto-detection
	got, err := ResolveLayer(dict.Dict{}, 32634)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SRID != 32634 || got.Explicit {
		t.Fatalf("expected inherited srid 32634, got %+v", got)
	}

	got, err = ResolveLayer(dict.Dict{"srid": 4326}, 32634)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SRID != 4326 || !got.Explicit {
		t.Fatalf("expected explicit srid 4326, got %+v", got)
	}
}

func TestApplySystemInfoCRS(t *testing.T) {
	basic.RegisterBuiltinProj4SRIDs()

	type tcase struct {
		name          string
		currentSRID   int
		explicit      bool
		projection    string
		expectedSRID  int
		expectedApply bool
		expectErr     bool
	}

	fn := func(t *testing.T, tc tcase) {
		t.Helper()
		gotSRID, applied, err := ApplySystemInfoCRS(tc.currentSRID, tc.explicit, tc.projection)
		if tc.expectErr {
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			return
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if applied != tc.expectedApply {
			t.Fatalf("expected applied %v, got %v", tc.expectedApply, applied)
		}
		if gotSRID != tc.expectedSRID {
			t.Fatalf("expected srid %v, got %v", tc.expectedSRID, gotSRID)
		}
	}

	fn(t, tcase{
		name:          "empty projection is a no-op",
		currentSRID:   4326,
		explicit:      false,
		projection:    "",
		expectedSRID:  4326,
		expectedApply: false,
	})
	fn(t, tcase{
		name:          "blank projection is a no-op",
		currentSRID:   4326,
		explicit:      false,
		projection:    "   ",
		expectedSRID:  4326,
		expectedApply: false,
	})
	fn(t, tcase{
		name:        "explicit provider srid suppresses projection",
		currentSRID: 3857,
		explicit:    true,
		projection:  "+proj=utm +zone=34 +datum=WGS84 +units=m +no_defs",
		expectedSRID:  3857,
		expectedApply: false,
	})
	fn(t, tcase{
		name:          "auto srid registers projection as synthetic",
		currentSRID:   3857,
		explicit:      false,
		projection:    "+proj=utm +zone=34 +datum=WGS84 +units=m +no_defs",
		expectedSRID:  int(basic.SyntheticSRIDMin),
		expectedApply: true,
	})

	// the registered synthetic SRID must reverse-lookup the same proj4 string
	proj4 := "+proj=utm +zone=34 +datum=WGS84 +units=m +no_defs"
	gotSRID, applied, err := ApplySystemInfoCRS(0, false, proj4)
	if err != nil || !applied {
		t.Fatalf("unexpected result: srid %v applied %v err %v", gotSRID, applied, err)
	}
	code, ok := basic.Proj4DefnSRID(proj4)
	if !ok || uint64(gotSRID) != code {
		t.Fatalf("expected reverse lookup for synthetic srid %v, got code %v ok %v", gotSRID, code, ok)
	}

	fn(t, tcase{
		name:        "invalid projection errors",
		currentSRID: 4326,
		explicit:    false,
		projection:  "+proj=notaproj +datum=WGS84",
		expectErr:   true,
	})
}
