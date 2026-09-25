package mysql

import (
	"strings"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/provider"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
)

// TestReplaceTokensPreservesTrailingClauses covers the regression matrix row
// "WHERE ... AND !BBOX! ORDER BY ... -> ORDER BY preserved": the runtime
// token replacement must keep trailing clauses verbatim.
func TestReplaceTokensPreservesTrailingClauses(t *testing.T) {
	tile := provider.NewTile(0, 0, 0, 0, tegola.WebMercator)
	extent := geom.NewExtent([2]float64{-10, -10}, [2]float64{10, 10})
	layer := &Layer{geomFieldname: "geom", bboxFields: codec.DefaultBBoxFields()}
	out, err := replaceTokens("SELECT * FROM t WHERE kind = 'a' AND !BBOX! ORDER BY id DESC", layer, tile, extent)
	if err != nil {
		t.Fatalf("replaceTokens: %v", err)
	}
	if !strings.Contains(strings.ToUpper(out), "ORDER BY ID DESC") {
		t.Errorf("ORDER BY must be preserved: %q", out)
	}
	if strings.Contains(out, "!BBOX!") {
		t.Errorf("!BBOX! must be replaced: %q", out)
	}
}
