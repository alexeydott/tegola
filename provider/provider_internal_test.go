package provider

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/go-spatial/tegola/dict"
)

func TestProviderFilterInclude(t *testing.T) {

	type tcase struct {
		Expected providerFilter
		Filters  []providerType
		IsMVT    bool
		IsSTD    bool
	}

	fn := func(tc tcase) func(*testing.T) {
		return func(t *testing.T) {

			got := providerFilterInclude(tc.Filters...)
			if got != tc.Expected {
				t.Errorf("providerFilterInclude, expected %v got %v", tc.Expected, got)
				return
			}
			is := got.Is(TypeStd)
			if is != tc.IsSTD {
				t.Errorf("IsStd, expected %v got %v", tc.IsSTD, is)
			}
			is = got.Is(TypeMvt)
			if is != tc.IsMVT {
				t.Errorf("IsMVT, expected %v got %v", tc.IsMVT, is)
			}
		}
	}
	tests := map[string]tcase{
		"none":  {},
		"none2": {Filters: []providerType{}},
		"std": {
			Expected: 0b00000001,
			Filters:  []providerType{TypeStd},
			IsSTD:    true,
		},
		"mvt": {
			Expected: 0b00000010,
			Filters:  []providerType{TypeMvt},
			IsMVT:    true,
		},
		"all": {
			Expected: 0b00000011,
			Filters:  []providerType{TypeStd, TypeMvt},
			IsSTD:    true,
			IsMVT:    true,
		},
		"all2": {
			Expected: 0b00000011,
			Filters:  []providerType{TypeMvt, TypeStd},
			IsSTD:    true,
			IsMVT:    true,
		},
	}
	for name, tc := range tests {
		t.Run(name, fn(tc))
	}

}

// TestTypeAllCombinesProviderTypes (audit P6-23) guards against TypeAll being
// computed as the intersection (&) of TypeStd and TypeMvt, which evaluates to
// 0 and silently broke the Drivers filter for "all types" requests.
func TestTypeAllCombinesProviderTypes(t *testing.T) {
	if TypeAll != TypeStd|TypeMvt {
		t.Errorf("TypeAll: expected %08b (%d), got %08b (%d)", TypeStd|TypeMvt, TypeStd|TypeMvt, TypeAll, TypeAll)
	}
	if uint8(TypeAll) == 0 {
		t.Error("TypeAll is 0: Drivers() can never filter on all provider types")
	}
}

// TestDriversFilterAllTypeCombos (audit P6-23) exercises the Drivers() type
// filter with every combination of TypeStd/TypeMvt/TypeAll. The pre-fix
// TypeAll = TypeStd & TypeMvt == 0 made Drivers(TypeStd, TypeMvt) return only
// MVT drivers instead of all drivers.
func TestDriversFilterAllTypeCombos(t *testing.T) {
	savedProviders := providers
	defer func() { providers = savedProviders }()

	var fakeStd InitFunc = func(dict.Dicter, []Map) (Tiler, error) { return nil, nil }
	var fakeMvt MVTInitFunc = func(dict.Dicter, []Map) (MVTTiler, error) { return nil, nil }

	providers = map[string]pfns{
		"p-std":  {init: fakeStd},
		"p-mvt":  {mvtInit: fakeMvt},
		"p-both": {init: fakeStd, mvtInit: fakeMvt},
	}

	tests := map[string]struct {
		Filters  []providerType
		Expected []string
	}{
		"no filter":      {nil, []string{"p-both", "p-mvt", "p-std"}},
		"empty filter":   {[]providerType{}, []string{"p-both", "p-mvt", "p-std"}},
		"std only":       {[]providerType{TypeStd}, []string{"p-both", "p-std"}},
		"mvt only":       {[]providerType{TypeMvt}, []string{"p-both", "p-mvt"}},
		"std and mvt":    {[]providerType{TypeStd, TypeMvt}, []string{"p-both", "p-mvt", "p-std"}},
		"mvt and std":    {[]providerType{TypeMvt, TypeStd}, []string{"p-both", "p-mvt", "p-std"}},
		"explicit all":   {[]providerType{TypeAll}, []string{"p-both", "p-mvt", "p-std"}},
		"all with std":   {[]providerType{TypeAll, TypeStd}, []string{"p-both", "p-mvt", "p-std"}},
		"all duplicates": {[]providerType{TypeAll, TypeAll}, []string{"p-both", "p-mvt", "p-std"}},
		"std duplicates": {[]providerType{TypeStd, TypeStd}, []string{"p-both", "p-std"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Drivers(tc.Filters...)
			sort.Strings(got)
			if len(got) != len(tc.Expected) {
				t.Fatalf("Drivers(%v): expected %v, got %v", tc.Filters, tc.Expected, got)
			}
			for i := range got {
				if got[i] != tc.Expected[i] {
					t.Fatalf("Drivers(%v): expected %v, got %v", tc.Filters, tc.Expected, got)
				}
			}
		})
	}
}

// fakes for TestIsTileJSONV3Compatible asserting the LayerFielder contract.

type fakeStdFielder struct{}

func (fakeStdFielder) Layers() ([]LayerInfo, error) { return nil, nil }
func (fakeStdFielder) TileFeatures(context.Context, string, Tile, Params, func(*Feature) error) error {
	return nil
}
func (fakeStdFielder) LayerFields(context.Context, string) (map[string]any, error) { return nil, nil }

type fakeStdPlain struct{}

func (fakeStdPlain) Layers() ([]LayerInfo, error) { return nil, nil }
func (fakeStdPlain) TileFeatures(context.Context, string, Tile, Params, func(*Feature) error) error {
	return nil
}

type fakeMVTFielder struct{}

func (fakeMVTFielder) Layers() ([]LayerInfo, error) { return nil, nil }
func (fakeMVTFielder) MVTForLayers(context.Context, Tile, Params, []Layer) ([]byte, error) {
	return nil, nil
}
func (fakeMVTFielder) LayerFields(context.Context, string) (map[string]any, error) { return nil, nil }

type fakeMVTPlain struct{}

func (fakeMVTPlain) Layers() ([]LayerInfo, error) { return nil, nil }
func (fakeMVTPlain) MVTForLayers(context.Context, Tile, Params, []Layer) ([]byte, error) {
	return nil, nil
}

// TestIsTileJSONV3Compatible (audit P6-5) guards the MVT branch of
// TilerUnion.IsTileJSONV3Compatible: it asserted tu.Std.(LayerFielder) while
// Std is nil for MVT providers, so every MVT provider was rejected
// regardless of whether it implemented LayerFielder.
func TestIsTileJSONV3Compatible(t *testing.T) {
	tests := map[string]struct {
		TU      TilerUnion
		WantOK  bool
		WantErr error
	}{
		"mvt with LayerFielder": {
			TU:     TilerUnion{Mvt: fakeMVTFielder{}},
			WantOK: true,
		},
		"mvt without LayerFielder": {
			TU:      TilerUnion{Mvt: fakeMVTPlain{}},
			WantOK:  false,
			WantErr: ErrNotTileJSONV3Compatible,
		},
		"std with LayerFielder": {
			TU:     TilerUnion{Std: fakeStdFielder{}},
			WantOK: true,
		},
		"std without LayerFielder": {
			TU:      TilerUnion{Std: fakeStdPlain{}},
			WantOK:  false,
			WantErr: ErrNotTileJSONV3Compatible,
		},
		"no provider": {
			TU:      TilerUnion{},
			WantOK:  false,
			WantErr: ErrNoProvider,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tc.TU.IsTileJSONV3Compatible()
			if tc.WantErr != nil {
				if !errors.Is(err, tc.WantErr) {
					t.Fatalf("expected error %v, got %v", tc.WantErr, err)
				}
			} else if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if got != tc.WantOK {
				t.Fatalf("expected compatible=%v, got %v", tc.WantOK, got)
			}
		})
	}
}
