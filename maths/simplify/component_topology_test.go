package simplify_test

import (
	"reflect"
	"testing"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/maths/simplify"
)

func TestSimplifyPreservesComponentRelationships(t *testing.T) {
	// The shell's right-hand bulge is within tolerance of the straight chord.
	// Removing it would strand the hole outside an otherwise simple shell.
	shell := basic.Line{{0, 0}, {0, 100}, {100, 100}, {110, 50}, {100, 0}}
	hole := basic.Line{{102, 48}, {107, 50}, {102, 52}}
	for _, tc := range []struct {
		name string
		in   tegola.Geometry
	}{
		{"hole remains inside shell", basic.Polygon{shell, hole}},
		{"separate polygon in concavity", basic.MultiPolygon{basic.Polygon{shell}, basic.Polygon{{{110, 60}, {115, 65}, {112, 70}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := simplify.SimplifyGeometry(tc.in, 15)
			if !reflect.DeepEqual(got, tc.in) {
				t.Fatalf("component topology must be retained: got %#v, want %#v", got, tc.in)
			}
		})
	}
}
