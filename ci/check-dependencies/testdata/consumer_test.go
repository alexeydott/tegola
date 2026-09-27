package consumer

import (
	"context"
	"math"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/mvt"
	vectorTile "github.com/go-spatial/geom/encoding/mvt/vector_tile"
	"github.com/go-spatial/tegola/basic"
	"google.golang.org/protobuf/proto"
)

func TestForkMVT(t *testing.T) {
	feature := mvt.Feature{Geometry: geom.LineString{{1, 2}}}
	tileFeature, err := feature.VTileFeature(context.Background(), nil, nil)
	if err != nil || tileFeature != nil {
		t.Fatalf("degenerate line: got %v, %v; want nil, nil", tileFeature, err)
	}
	if _, err := proto.Marshal(&vectorTile.Tile{}); err != nil {
		t.Fatalf("protobuf APIv2: %v", err)
	}
}

func TestForkDatumShift(t *testing.T) {
	code, err := basic.RegisterProj4Defn("+proj=etmerc +ellps=bessel +towgs84=41,-107.6,-93,0,0,0,0 +x_0=0 +y_0=0 +lon_0=37.5 +k_0=1 +lat_0=55.6666666667 +units=m +no_defs")
	if err != nil {
		t.Fatal(err)
	}
	converted, err := basic.ToWebMercator(code, geom.Point{769.792, 19300.763})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := converted.(geom.Point)
	if !ok {
		t.Fatalf("unexpected geometry type %T", converted)
	}
	// Independent PROJ reference, also used by the root regression test.
	want := geom.Point{4175652.829, 7526703.655}
	if math.IsNaN(got[0]) || math.IsNaN(got[1]) || math.Abs(got[0]-want[0]) > 0.1 || math.Abs(got[1]-want[1]) > 0.1 {
		t.Fatalf("datum shift: got %v, want %v within 0.1 metre", got, want)
	}
}
