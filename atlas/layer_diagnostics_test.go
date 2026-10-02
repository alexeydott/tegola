package atlas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alexeydott/geom/slippy"
	"github.com/alexeydott/tegola/provider"
)

type deadlineTileProvider struct{ errorTileProvider }

func (deadlineTileProvider) TileFeatures(ctx context.Context, _ string, _ provider.Tile, _ provider.Params, _ func(*provider.Feature) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestLayerDeadlineDiagnostic(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	m := NewWebMercatorMap("test-map")
	m.Layers = []Layer{{Name: "public-layer", ProviderLayerName: "source-layer", Provider: deadlineTileProvider{}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := m.encodeMVTTile(ctx, slippy.Tile{Z: 15, X: 19782, Y: 10323}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error identity changed: %v", err)
	}
	for _, line := range bytes.Split(output.Bytes(), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) != nil || record["msg"] != "tile layer timing" {
			continue
		}
		if record["outcome"] != "deadline" || record["level"] != "WARN" || record["map"] != "test-map" || record["provider_layer"] != "source-layer" || record["x"] != float64(19782) {
			t.Fatalf("unexpected diagnostic: %v", record)
		}
		return
	}
	t.Fatal("missing layer deadline diagnostic")
}
