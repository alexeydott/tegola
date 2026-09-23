package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-spatial/tegola/cache/memory"
	"github.com/go-spatial/tegola/server"
)

func TestMiddlewareTileCacheHandler(t *testing.T) {
	type tcase struct {
		uri       string
		uriPrefix string
	}

	fn := func(tc tcase) func(t *testing.T) {
		return func(t *testing.T) {
			var err error

			if tc.uriPrefix != "" {
				server.URIPrefix = tc.uriPrefix
			} else {
				server.URIPrefix = "/"
			}

			a := newTestMapWithLayers(testLayer1, testLayer2, testLayer3)
			cacher, _ := memory.New(nil)
			a.SetCache(cacher)

			w, router, err := doRequest(t, a, http.MethodGet, tc.uri, nil)
			if err != nil {
				t.Errorf("error making request, expected nil got %v", err)
				return
			}

			// first response we expect the cache to MISS
			if w.Header().Get("Tegola-Cache") != "MISS" {
				t.Errorf("header Tegola-Cache, expected MISS got %v", w.Header().Get("Tegola-Cache"))
				return
			}

			// play the request again to get a HIT
			r, err := http.NewRequest("GET", tc.uri, nil)
			if err != nil {
				t.Errorf("error making request, expected nil got %v", err)
				return
			}

			w = httptest.NewRecorder()
			router.ServeHTTP(w, r)

			if w.Header().Get("Tegola-Cache") != "HIT" {
				t.Errorf("Tegoal-Cache, expected HIT got %v", w.Header().Get("Tegola-Cache"))
				return
			}
		}
	}

	tests := map[string]tcase{
		"map": {
			uri: "/maps/test-map/10/2/3.pbf",
		},
		"map layer": {
			uri: "/maps/test-map/test-layer/4/2/3.pbf",
		},
		"map and uri prefix": {
			uri:       "/tegola/maps/test-map/10/2/3.pbf",
			uriPrefix: "/tegola",
		},
		"map layer and uri prefix": {
			uri:       "/tegola/maps/test-map/test-layer/4/2/3.pbf",
			uriPrefix: "/tegola",
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

func TestMiddlewareTileCacheHandlerIgnoreParams(t *testing.T) {
	type tcase struct {
		uri       string
		uriPrefix string
	}

	fn := func(tc tcase) func(t *testing.T) {
		return func(t *testing.T) {
			var err error

			if tc.uriPrefix != "" {
				server.URIPrefix = tc.uriPrefix
			} else {
				server.URIPrefix = "/"
			}

			a := newTestMapWithLayers(testLayer1, testLayer2, testLayer3)
			cacher, _ := memory.New(nil)
			a.SetCache(cacher)

			w, router, err := doRequest(t, a, http.MethodGet, tc.uri, nil)
			if err != nil {
				t.Errorf("error making request, expected nil got %v", err)
				return
			}

			// we expect the cache to not being used
			if w.Header().Get("Tegola-Cache") != "" {
				t.Errorf("no header Tegola-Cache is expected, got %v", w.Header().Get("Tegola-Cache"))
				return
			}

			// play the request again
			r, err := http.NewRequest("GET", tc.uri, nil)
			if err != nil {
				t.Errorf("error making request, expected nil got %v", err)
				return
			}

			w = httptest.NewRecorder()
			router.ServeHTTP(w, r)

			if w.Header().Get("Tegola-Cache") != "" {
				t.Errorf("no header Tegola-Cache is expected, got %v", w.Header().Get("Tegola-Cache"))
				return
			}
		}
	}

	tests := map[string]tcase{
		"map params": {
			uri: "/maps/test-map/10/2/3.pbf?param=value",
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

func TestMiddlewareTileCacheHandlerDirtyRegenerates(t *testing.T) {
	server.URIPrefix = "/"
	a := newTestMapWithLayers(testLayer1)
	cacher, _ := memory.New(nil)
	a.SetCache(cacher)
	router := server.NewRouter(a)
	uri := "/maps/test-map/4/2/3.pbf"

	request := func(uri string) *httptest.ResponseRecorder {
		r, err := http.NewRequest(http.MethodGet, uri, nil)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	if w := request(uri); w.Header().Get("Tegola-Cache") != "MISS" {
		t.Fatalf("initial request should miss cache, got %q", w.Header().Get("Tegola-Cache"))
	}
	if w := request(uri); w.Header().Get("Tegola-Cache") != "HIT" {
		t.Fatalf("second request should hit cache, got %q", w.Header().Get("Tegola-Cache"))
	}
	if w := request(uri + "?" + server.QueryKeyDirty + "=true"); w.Header().Get("Tegola-Cache") != "MISS" {
		t.Fatalf("dirty request should regenerate and miss cache, got %q", w.Header().Get("Tegola-Cache"))
	}
	if w := request(uri); w.Header().Get("Tegola-Cache") != "HIT" {
		t.Fatalf("request after dirty regeneration should hit cache, got %q", w.Header().Get("Tegola-Cache"))
	}
}
