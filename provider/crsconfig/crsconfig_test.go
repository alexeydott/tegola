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
