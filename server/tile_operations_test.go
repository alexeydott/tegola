package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-spatial/tegola/atlas"
	"github.com/go-spatial/tegola/cache"
	"github.com/go-spatial/tegola/cache/memory"
	"github.com/go-spatial/tegola/server"
)

const testTileOpsToken = "test-tile-ops-token"

// countingCache counts cache writes in total and per key.
type countingCache struct {
	cache.Interface
	mu    sync.Mutex
	sets  int
	byKey map[string]int
}

func (c *countingCache) Set(ctx context.Context, key *cache.Key, val []byte) error {
	c.mu.Lock()
	if c.byKey == nil {
		c.byKey = make(map[string]int)
	}
	c.sets++
	c.byKey[key.String()]++
	c.mu.Unlock()

	return c.Interface.Set(ctx, key, val)
}

func (c *countingCache) setCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.sets
}

func (c *countingCache) keyCount(key cache.Key) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.byKey[key.String()]
}

// newTileOpsTestServer wires a counting cache into the given atlas and returns
// the cache together with a router serving the atlas.
func newTileOpsTestServer(t *testing.T, a *atlas.Atlas) (*countingCache, http.Handler) {
	t.Helper()

	mem, err := memory.New(nil)
	if err != nil {
		t.Fatalf("memory.New() error = %v", err)
	}
	cacher := &countingCache{Interface: mem}
	a.SetCache(cacher)

	return cacher, server.NewRouter(a)
}

// enableTileOperations overrides the package level tile operations config for
// one test and restores the previous value afterwards.
func enableTileOperations(t *testing.T, cfg server.TileOperationsConfig) {
	t.Helper()

	old := server.TileOperations
	t.Cleanup(func() {
		server.TileOperations = old
	})
	server.TileOperations = cfg
}

// serverTileOpsTestConfig enables tile operations with a fixed token and very
// generous limits, so tests are not affected by the shared rate/concurrency
// window state.
func serverTileOpsTestConfig() server.TileOperationsConfig {
	return server.TileOperationsConfig{
		Enabled:       true,
		Token:         testTileOpsToken,
		RatePerMinute: 1000,
		MaxConcurrent: 100,
	}
}

// tileOpsRequest runs a GET request through the router; a non-empty token is
// sent in the tile operations token header.
func tileOpsRequest(router http.Handler, uri, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, uri, nil)
	if token != "" {
		r.Header.Set(server.TileOperationsTokenHeader, token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	return w
}

func TestTileOperationsDisabledRefusesOperations(t *testing.T) {
	a := newTestMapWithLayers(testLayer1)
	cacher, router := newTileOpsTestServer(t, a)
	enableTileOperations(t, server.TileOperationsConfig{})

	base := "/maps/test-map/test-layer/4/2/3.pbf"
	for _, op := range []string{
		"?tile=update",
		"?tile=getupdated",
		"?tile=status",
		"?dirty=true",
		"?dirty=1",
		"?dirty",
	} {
		// even a well formed token must not enable operations while the
		// feature flag is off
		w := tileOpsRequest(router, base+op, testTileOpsToken)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 (%s)", op, w.Code, w.Body.String())
		}
	}

	if got := cacher.setCount(); got != 0 {
		t.Errorf("tile operations wrote to the cache %d times while disabled, want 0", got)
	}

	// ordinary tile serving keeps working while operations are disabled
	w := tileOpsRequest(router, base, "")
	if w.Code != http.StatusOK {
		t.Fatalf("ordinary tile status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Tegola-Cache"); got != "MISS" {
		t.Errorf("Tegola-Cache = %q, want MISS", got)
	}
	if got := cacher.setCount(); got != 1 {
		t.Errorf("ordinary tile cache writes = %d, want 1", got)
	}

	w = tileOpsRequest(router, base, "")
	if got := w.Header().Get("Tegola-Cache"); got != "HIT" {
		t.Errorf("Tegola-Cache = %q, want HIT", got)
	}
}

func TestTileOperationsRequireToken(t *testing.T) {
	a := newTestMapWithLayers(testLayer1)
	cacher, router := newTileOpsTestServer(t, a)
	enableTileOperations(t, server.TileOperationsConfig{
		Enabled:       true,
		Token:         testTileOpsToken,
		RatePerMinute: 1000,
		MaxConcurrent: 100,
	})

	base := "/maps/test-map/test-layer/4/2/3.pbf"

	// missing and wrong tokens are refused
	w := tileOpsRequest(router, base+"?tile=update", "")
	if w.Code != http.StatusForbidden {
		t.Errorf("missing token: status = %d, want 403", w.Code)
	}
	w = tileOpsRequest(router, base+"?tile=update", "wrong-token")
	if w.Code != http.StatusForbidden {
		t.Errorf("wrong token: status = %d, want 403", w.Code)
	}
	w = tileOpsRequest(router, base+"?dirty=true", "")
	if w.Code != http.StatusForbidden {
		t.Errorf("dirty without token: status = %d, want 403", w.Code)
	}

	if got := cacher.setCount(); got != 0 {
		t.Fatalf("refused operations wrote to the cache %d times, want 0", got)
	}

	// a valid token authorizes the operation
	w = tileOpsRequest(router, base+"?tile=update", testTileOpsToken)
	if w.Code != http.StatusNoContent {
		t.Fatalf("authorized update: status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
	// z4 tile 2/3 lives in metatile x[0..7] y[0..7]; the map has no bounds
	// configured so all 64 tiles must be cached
	if got := cacher.setCount(); got != 64 {
		t.Errorf("update cache writes = %d, want 64", got)
	}

	// status works with a token
	w = tileOpsRequest(router, base+"?tile=status", testTileOpsToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status: status = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	// dirty regenerates a single tile with a valid token
	before := cacher.keyCount(cache.Key{MapName: "test-map", LayerName: "test-layer", Z: 4, X: 2, Y: 3})
	w = tileOpsRequest(router, base+"?dirty=true", testTileOpsToken)
	if w.Code != http.StatusOK {
		t.Fatalf("dirty: status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Tegola-Cache"); got != "MISS" {
		t.Errorf("dirty Tegola-Cache = %q, want MISS", got)
	}
	if got, want := cacher.keyCount(cache.Key{MapName: "test-map", LayerName: "test-layer", Z: 4, X: 2, Y: 3}), before+1; got != want {
		t.Errorf("dirty tile cache writes = %d, want %d", got, want)
	}
}

func TestTileOperationsEnabledWithoutTokenFailsClosed(t *testing.T) {
	a := newTestMapWithLayers(testLayer1)
	cacher, router := newTileOpsTestServer(t, a)
	enableTileOperations(t, server.TileOperationsConfig{
		Enabled:       true,
		Token:         "",
		RatePerMinute: 1000,
		MaxConcurrent: 100,
	})

	base := "/maps/test-map/test-layer/4/2/3.pbf?tile=update"
	for _, token := range []string{"", "anything"} {
		w := tileOpsRequest(router, base, token)
		if w.Code != http.StatusForbidden {
			t.Errorf("token %q: status = %d, want 403", token, w.Code)
		}
	}

	if got := cacher.setCount(); got != 0 {
		t.Errorf("fail closed operations wrote to the cache %d times, want 0", got)
	}
}

func TestTileOperationsRateLimitEnforced(t *testing.T) {
	a := newTestMapWithLayers(testLayer1)
	_, router := newTileOpsTestServer(t, a)
	enableTileOperations(t, server.TileOperationsConfig{
		Enabled:       true,
		Token:         testTileOpsToken,
		RatePerMinute: 1,
		MaxConcurrent: 100,
	})

	// the rate limiter window is shared, so depending on operations issued by
	// other tests the first request may already be refused; what must hold is
	// that a single operation per minute refuses at least one of the two.
	base := "/maps/test-map/test-layer/4/2/3.pbf?tile=status"
	var codes []int
	for i := 0; i < 2; i++ {
		w := tileOpsRequest(router, base, testTileOpsToken)
		codes = append(codes, w.Code)
		if w.Code != http.StatusTooManyRequests && w.Code != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200 or 429", i, w.Code)
		}
	}

	if codes[0] != http.StatusTooManyRequests && codes[1] != http.StatusTooManyRequests {
		t.Errorf("rate limit of 1 per minute was not enforced: statuses %v", codes)
	}
}
