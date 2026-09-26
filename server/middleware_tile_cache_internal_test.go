package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-spatial/geom/encoding/mvt"
	"github.com/go-spatial/tegola/cache"
)

// fakeTileCache is an in-memory cache.Interface that records Set calls so
// tests can assert what was written to the cache.
type fakeTileCache struct {
	mu       sync.Mutex
	items    map[string][]byte
	setCalls map[string]int
}

func newFakeTileCache() *fakeTileCache {
	return &fakeTileCache{
		items:    make(map[string][]byte),
		setCalls: make(map[string]int),
	}
}

func (f *fakeTileCache) Get(_ context.Context, key *cache.Key) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.items[key.String()]
	return v, ok, nil
}

func (f *fakeTileCache) Set(_ context.Context, key *cache.Key, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[key.String()] = value
	f.setCalls[key.String()]++
	return nil
}

func (f *fakeTileCache) Purge(_ context.Context, key *cache.Key) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, key.String())
	return nil
}

func (f *fakeTileCache) setCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setCalls[key]
}

// timeAfter returns a channel that fires after a test-friendly timeout.
func timeAfter(t *testing.T) <-chan time.Time {
	t.Helper()
	return time.After(2 * time.Second)
}

func TestTileRenderCaptureCapturesResponse(t *testing.T) {
	c := newTileRenderCapture()

	// the capture is stamped as a cache miss like the old response writer
	if got := c.Header().Get("Tegola-Cache"); got != "MISS" {
		t.Fatalf("Tegola-Cache = %q, want MISS", got)
	}

	c.Header().Set("Content-Type", mvt.MimeType)
	if n, err := c.Write([]byte{0x1, 0x2}); err != nil || n != 2 {
		t.Fatalf("Write = %d, %v; want 2, nil", n, err)
	}
	if n, err := c.Write([]byte{0x3}); err != nil || n != 1 {
		t.Fatalf("Write = %d, %v; want 1, nil", n, err)
	}

	res := c.result(false)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (implicit)", res.status)
	}
	if !bytes.Equal(res.body, []byte{0x1, 0x2, 0x3}) {
		t.Fatalf("body = %v, want [1 2 3]", res.body)
	}
	if res.canceled {
		t.Fatal("result unexpectedly canceled")
	}
	if got := res.header.Get("Content-Type"); got != mvt.MimeType {
		t.Fatalf("result Content-Type = %q, want %q", got, mvt.MimeType)
	}

	// headers are frozen at commit: later mutations must not leak into the
	// captured result
	c.Header().Set("Content-Type", "text/plain")
	if got := res.header.Get("Content-Type"); got != mvt.MimeType {
		t.Fatalf("result Content-Type after mutation = %q, want %q", got, mvt.MimeType)
	}
}

func TestTileRenderCaptureWriteHeaderFirstWins(t *testing.T) {
	c := newTileRenderCapture()
	c.WriteHeader(http.StatusOK)
	c.WriteHeader(http.StatusInternalServerError)

	res := c.result(false)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (first WriteHeader wins)", res.status)
	}

	// a capture with no writes at all resolves to an empty 200
	empty := newTileRenderCapture().result(false)
	if empty.status != http.StatusOK || len(empty.body) != 0 {
		t.Fatalf("empty capture = %d, %q; want 200, empty", empty.status, empty.body)
	}
}

func TestRenderTileForCacheCachesOnlySuccessfulMVT(t *testing.T) {
	type tcase struct {
		status      int
		contentType string
		wantCached  bool
	}

	tests := map[string]tcase{
		"ok mvt":            {status: http.StatusOK, contentType: mvt.MimeType, wantCached: true},
		"ok mvt with codec": {status: http.StatusOK, contentType: mvt.MimeType + "; charset=utf-8", wantCached: true},
		"error status":      {status: http.StatusInternalServerError, contentType: mvt.MimeType, wantCached: false},
		"not mvt":           {status: http.StatusOK, contentType: "application/json", wantCached: false},
		"no content type":   {status: http.StatusOK, contentType: "", wantCached: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cacher := newFakeTileCache()
			key := &cache.Key{MapName: "m", LayerName: "l", Z: 1, X: 0, Y: 0}

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				// write the body in parts to prove multi-write bodies are
				// captured whole
				_, _ = w.Write([]byte{0x1, 0x2})
				_, _ = w.Write([]byte{0x3})
			})

			req := httptest.NewRequest(http.MethodGet, "/maps/m/l/1/0/0", nil)
			res := renderTileForCache(req, handler, cacher, key, true)

			if res.status != tc.status {
				t.Fatalf("rendered status = %d, want %d", res.status, tc.status)
			}
			if tc.status == http.StatusOK && !bytes.Equal(res.body, []byte{0x1, 0x2, 0x3}) {
				t.Fatalf("rendered body = %v, want [1 2 3]", res.body)
			}

			want := 0
			if tc.wantCached {
				want = 1
			}
			if got := cacher.setCount(key.String()); got != want {
				t.Fatalf("cacher.Set calls = %d, want %d", got, want)
			}
			if tc.wantCached {
				cached, hit, err := cacher.Get(context.Background(), key)
				if err != nil || !hit || !bytes.Equal(cached, []byte{0x1, 0x2, 0x3}) {
					t.Fatalf("cached = %v, %v, %v; want [1 2 3], true, nil", cached, hit, err)
				}
			}
		})
	}
}

func TestRenderTileForCacheSkipsStaleWrites(t *testing.T) {
	cacher := newFakeTileCache()
	key := &cache.Key{MapName: "m", LayerName: "l", Z: 1, X: 0, Y: 0}
	lockKey := metatileLockKeyForCacheKey(key)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// a ?tile=update regenerates this metatile while the render is in
		// flight: its generation advances and the render result goes stale
		state := tileUpdateLocks.retain(lockKey)
		defer tileUpdateLocks.release(lockKey, state)
		tileUpdateLocks.beginUpdate(state)
		tileUpdateLocks.endUpdate(state)

		w.Header().Set("Content-Type", mvt.MimeType)
		_, _ = w.Write([]byte("stale"))
	})

	req := httptest.NewRequest(http.MethodGet, "/maps/m/l/1/0/0", nil)
	renderTileForCache(req, handler, cacher, key, true)

	if got := cacher.setCount(key.String()); got != 0 {
		t.Fatalf("cacher.Set calls = %d, want 0 (stale render must not write)", got)
	}
}

func TestTileRenderGroupSharesResults(t *testing.T) {
	var group tileRenderGroup
	started := make(chan struct{})
	var mu sync.Mutex
	var calls int
	var results []*tileRenderResult

	// a single render result is shared by all concurrent callers
	fn := func() *tileRenderResult {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		// give the concurrent callers time to join this call
		time.Sleep(50 * time.Millisecond)
		return &tileRenderResult{status: http.StatusOK, body: []byte("tile")}
	}

	var ready sync.WaitGroup
	var wg sync.WaitGroup
	wg.Add(4)
	ready.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			res, _ := group.do(context.Background(), "k", fn)
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}()
	}

	ready.Wait()
	<-started
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("render calls = %d, want 1", calls)
	}
	for i, res := range results {
		if res == nil || !bytes.Equal(res.body, []byte("tile")) {
			t.Fatalf("result %d = %+v, want shared 200 body", i, res)
		}
	}
}

func TestTileRenderGroupWaiterUnblocksOnContextCancel(t *testing.T) {
	var group tileRenderGroup
	started := make(chan struct{})
	release := make(chan struct{})

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = group.do(context.Background(), "k", func() *tileRenderResult {
			close(started)
			<-release
			return &tileRenderResult{status: http.StatusOK, body: []byte("tile")}
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiterDone := make(chan *tileRenderResult, 1)
	go func() {
		res, _ := group.do(ctx, "k", func() *tileRenderResult {
			t.Error("waiter must not run its own render")
			return nil
		})
		waiterDone <- res
	}()

	// cancel the waiter while the leader is still rendering
	cancel()
	select {
	case res := <-waiterDone:
		if !res.canceled {
			t.Fatalf("waiter result = %+v, want canceled", res)
		}
	case <-timeAfter(t):
		t.Fatal("waiter stayed queued after its context was canceled")
	}

	// the leader is unaffected and finishes normally
	close(release)
	select {
	case <-leaderDone:
	case <-timeAfter(t):
		t.Fatal("leader did not finish")
	}
}
