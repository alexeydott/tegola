//go:build cgo

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/tegola/atlas"
	"github.com/alexeydott/tegola/cache"
	"github.com/alexeydott/tegola/cache/memory"
	"github.com/alexeydott/tegola/config"
	"github.com/alexeydott/tegola/provider/test"
)

func TestWritableRouterTileMaintenance(t *testing.T) {
	for _, protocol := range []string{"features", "wfs"} {
		t.Run(protocol, func(t *testing.T) {
			preserveDiscoveryGlobals(t)
			oldOps := TileOperations
			TileOperations = TileOperationsConfig{Enabled: true, Token: "maintenance-test", RatePerMinute: 1000, MaxConcurrent: 100}
			t.Cleanup(func() { AwaitMetatileRegeneration(); TileOperations = oldOps })
			svc, _ := part4Service(t)
			api := part4API(t, svc)
			options := RouterOptions{Features: api}
			if protocol == "wfs" {
				options = RouterOptions{WFS: &WFSHandler{Service: svc, Config: config.WFSConfig{Enabled: true}.Resolved(), WriteConfig: api.cfg.Write}}
			}
			a := &atlas.Atlas{}
			m := atlas.NewWebMercatorMap("maintenance-map")
			m.Layers = []atlas.Layer{{Name: "points", ProviderLayerName: "test-layer-1", MinZoom: 0, MaxZoom: 22, Provider: &test.TileProvider{}, GeomType: geom.Point{}}}
			a.AddMap(m)
			c, err := memory.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			a.SetCache(c)
			router, err := NewRouterWithOptions(a, options)
			if err != nil {
				t.Fatal(err)
			}
			const uri = "/maps/maintenance-map/points/4/2/3.pbf"
			key := &cache.Key{MapName: "maintenance-map", LayerName: "points", Z: 4, X: 2, Y: 3}
			stale := []byte("stale persisted tile")
			if err := c.Set(context.Background(), key, stale); err != nil {
				t.Fatal(err)
			}
			request := func(query, token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, uri+query, nil)
				r.Header.Set(TileOperationsTokenHeader, token)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				return w
			}
			for _, op := range []string{"status", "update", "getupdated"} {
				if w := request("?tile="+op, ""); w.Code != http.StatusForbidden {
					t.Fatalf("unauthenticated %s: %d", op, w.Code)
				}
			}
			assertOrdinaryBypass := func() {
				t.Helper()
				w := request("", "")
				if w.Code != http.StatusOK || bytes.Equal(w.Body.Bytes(), stale) || w.Header().Get("Tegola-Cache") != "" || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("ordinary writable read used cache: %d %v %q", w.Code, w.Header(), w.Body.Bytes())
				}
			}
			assertOrdinaryBypass()
			w := request("?tile=status", "maintenance-test")
			var status tileStatusResponse
			if w.Code != http.StatusOK {
				t.Fatalf("status: %d %s", w.Code, w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if !status.Cached {
				t.Fatal("status ignored configured cache")
			}
			w = request("?tile=update", "maintenance-test")
			if w.Code != http.StatusAccepted {
				t.Fatalf("update: %d %s", w.Code, w.Body.String())
			}
			AwaitMetatileRegeneration()
			for y := uint(0); y < 8; y++ {
				for x := uint(0); x < 8; x++ {
					k := &cache.Key{MapName: key.MapName, LayerName: key.LayerName, Z: key.Z, X: x, Y: y}
					data, hit, err := c.Get(context.Background(), k)
					if err != nil || !hit || bytes.Equal(data, stale) {
						t.Fatalf("metatile %d/%d: hit=%v err=%v", x, y, hit, err)
					}
				}
			}
			if err := c.Set(context.Background(), key, stale); err != nil {
				t.Fatal(err)
			}
			w = request("?tile=getupdated", "maintenance-test")
			if w.Code != http.StatusOK || w.Body.Len() == 0 || bytes.Equal(w.Body.Bytes(), stale) {
				t.Fatalf("getupdated: %d %q", w.Code, w.Body.Bytes())
			}
			AwaitMetatileRegeneration()
			data, hit, err := c.Get(context.Background(), key)
			if err != nil || !hit || bytes.Equal(data, stale) {
				t.Fatalf("getupdated did not refresh cache: hit=%v err=%v", hit, err)
			}
			assertOrdinaryBypass()
		})
	}
}
