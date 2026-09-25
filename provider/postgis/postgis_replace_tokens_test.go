package postgis

import (
	"strings"
	"testing"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/provider"
)

// TestReplaceTokensPreservesTrailingClauses covers the regression matrix row
// "WHERE ... AND !BBOX! ORDER BY ... -> ORDER BY preserved": the runtime
// token replacement must keep trailing clauses verbatim.
func TestReplaceTokensPreservesTrailingClauses(t *testing.T) {
	tile := provider.NewTile(0, 0, 0, 0, tegola.WebMercator)
	lyr := &Layer{name: "t", srid: 3857}
	out, err := replaceTokens("SELECT * FROM t WHERE kind = 'a' AND !BBOX! ORDER BY id DESC", lyr, tile, false)
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
