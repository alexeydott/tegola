package postgis

import (
	"fmt"
	"strings"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/internal/sqltoken"
	"github.com/go-spatial/tegola/provider"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
)

// Native PostGIS placeholders are geometry operands, unlike raw MOS bounds
// predicates. Substitute an envelope before shared permissive probe rewriting.
func prepareLayerProbeSQL(sql string, layer *Layer, geomType string) (string, error) {
	if !codec.IsRawFormat(layer.geometryFormat) && sqltoken.PostgreSQL.ContainsTokenFold(sql, "!BBOX!", "!BOX!") {
		bbox, err := replaceTokens("!BBOX!", layer, provider.NewTile(0, 0, 0, 0, tegola.WebMercator), false)
		if err != nil {
			return "", fmt.Errorf("layer (%v) native probe envelope: %w", layer.name, err)
		}
		sql = sqltoken.PostgreSQL.MapTokens(sql, func(token string) string {
			if strings.EqualFold(token, "!BBOX!") || strings.EqualFold(token, "!BOX!") {
				return bbox
			}
			return token
		})
	}
	return codec.PostgreSQL.PrepareProbeSQL(sql, layer.geomField, layer.idField, geomType), nil
}
