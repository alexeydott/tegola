package server

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/go-spatial/geom/encoding/mvt"
	"github.com/go-spatial/tegola/atlas"
	"github.com/go-spatial/tegola/cache"
	"github.com/go-spatial/tegola/internal/log"
)

// tileRenders deduplicates concurrent cache-miss renders of the same tile so a
// thundering herd of identical requests triggers a single render.
var tileRenders tileRenderGroup

// TileCacheHandler implements a request cache for tiles on requests when the URLs
// have a /:z/:x/:y scheme suffix (i.e. /osm/1/3/4.pbf)
func TileCacheHandler(a *atlas.Atlas, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error

		// check if a cache backend exists
		cacher := a.GetCache()
		if cacher == nil {
			// nope. move on
			next.ServeHTTP(w, r)
			return
		}

		// parse our URI into a cache key structure (remove any configured URIPrefix + "maps/" )
		key, err := cache.ParseKey(strings.TrimPrefix(r.URL.Path, path.Join(URIPrefix, "maps")))
		if err != nil {
			log.Errorf("cache middleware: ParseKey err: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		query := r.URL.Query()
		_, hasDirty := query[QueryKeyDirty]
		dirty := query.Get(QueryKeyDirty)
		forceRegenerate := hasDirty && (dirty == "" || dirty == "1" || strings.EqualFold(dirty, "true"))

		// The ?dirty parameter drives cache maintenance: it is gated like the
		// ?tile= operations (disabled by default, token + rate limit when
		// enabled). Only the regenerating variant needs a concurrency slot.
		if hasDirty {
			release, err := gateTileOperation(r, forceRegenerate && len(query) == 1)
			if err != nil {
				log.Debugf("cache middleware: dirty regeneration denied for %v: %v", r.URL.Path, err)
				writeTileOperationDenied(w, err)
				return
			}
			defer release()
		}

		// A dirty request is only cacheable when it is the sole query
		// parameter. Caching a response that depends on map query parameters
		// under the ordinary tile key would serve the wrong data later.
		if forceRegenerate && len(query) == 1 {
			// a regeneration is a cache mutation: serialize it against other
			// metatile updates and mark the metatile as updating so concurrent
			// renders know their result is stale.
			state, unlock, err := tileUpdateLocks.acquire(r.Context(), metatileLockKeyForCacheKey(key))
			if err != nil {
				// the request was canceled while waiting for the metatile lock
				log.Debugf("cache middleware: dirty regeneration canceled for %v: %v", r.URL.Path, err)
				return
			}
			defer unlock()
			tileUpdateLocks.beginRegeneration(state)
			defer tileUpdateLocks.endRegeneration(state)

			res := renderTileForCache(r, next, cacher, key, false)
			res.writeTo(w)
			return
		}

		// Preserve the existing behavior for ordinary query parameters.
		if r.URL.RawQuery != "" {
			next.ServeHTTP(w, r)
			return
		}

		// Cache hits are served without holding any lock: reads and response
		// writes never block each other (or wait behind an update).

		// use the URL path as the key
		cachedTile, hit, err := cacher.Get(r.Context(), key)
		if err != nil {
			log.Errorf("cache middleware: error reading from cache: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		if hit {
			// mimetype for mapbox vector tiles
			w.Header().Add("Content-Type", mvt.MimeType)

			// communicate the cache is being used
			w.Header().Add("Tegola-Cache", "HIT")
			w.Header().Add("Content-Length", fmt.Sprintf("%d", len(cachedTile)))

			_, _ = w.Write(cachedTile)
			return
		}

		// cache miss: render the tile once per tile key and share the captured
		// result with all concurrent requests for the same tile
		for attempt := 0; ; attempt++ {
			res, _ := tileRenders.do(r.Context(), key.String(), func() *tileRenderResult {
				return renderTileForCache(r, next, cacher, key, true)
			})

			if res.canceled && attempt < 1 && r.Context().Err() == nil {
				// the shared render was canceled by another request while ours
				// is still alive: try again instead of dropping a good request
				continue
			}
			if r.Context().Err() != nil {
				return
			}
			res.writeTo(w)
			return
		}
	})
}

// tileRenderResult is the outcome of a tile render, captured in full so it can
// be replayed to any number of waiting requests and stored in the cache.
type tileRenderResult struct {
	status int
	header http.Header
	body   []byte
	// canceled reports that the render was abandoned because its request
	// context ended before producing a complete response.
	canceled bool
}

// writeTo replays the captured response to w.
func (res *tileRenderResult) writeTo(w http.ResponseWriter) {
	for k, vals := range res.header {
		w.Header()[k] = vals
	}
	status := res.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if len(res.body) > 0 {
		_, _ = w.Write(res.body)
	}
}

// tileRenderCapture records a handler response instead of sending it. Headers
// are frozen when the response is committed, matching net/http semantics.
type tileRenderCapture struct {
	header http.Header
	frozen http.Header
	status int
	body   bytes.Buffer
}

func newTileRenderCapture() *tileRenderCapture {
	header := http.Header{}
	// communicate the cache is being used (miss); mirrors the old
	// tileCacheResponseWriter behavior of stamping MISS on the response
	header.Set("Tegola-Cache", "MISS")
	return &tileRenderCapture{header: header}
}

func (c *tileRenderCapture) Header() http.Header {
	return c.header
}

func (c *tileRenderCapture) WriteHeader(status int) {
	if c.status != 0 {
		return
	}
	c.status = status
	c.frozen = c.header.Clone()
}

func (c *tileRenderCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
		c.frozen = c.header.Clone()
	}
	return c.body.Write(b)
}

func (c *tileRenderCapture) result(canceled bool) *tileRenderResult {
	status := c.status
	header := c.frozen
	if status == 0 {
		// nothing was written: net/http would answer 200 with the headers set
		status = http.StatusOK
		header = c.header
	}
	return &tileRenderResult{
		status:   status,
		header:   header,
		body:     c.body.Bytes(),
		canceled: canceled,
	}
}

// isMVTContentType reports whether the response Content-Type is a Mapbox
// Vector Tile payload. Only such responses are stored in the tile cache.
func isMVTContentType(header http.Header) bool {
	ct := header.Get("Content-Type")
	if ct == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mediaType == mvt.MimeType
}

// renderTileForCache renders a tile through the wrapped handler and stores the
// encoded result in the cache. The render is captured and replayed to the
// caller instead of being streamed, so concurrent requests can share it.
//
// When checkStale is set (ordinary miss renders) the cached write is skipped
// if a ?tile=update regenerated the metatile while the render was in flight,
// so a slow render can never overwrite freshly updated tiles with stale
// bytes. Mutating paths (?dirty) pass checkStale=false: they hold the metatile
// lock and their write is authoritative.
func renderTileForCache(r *http.Request, next http.Handler, cacher cache.Interface, key *cache.Key, checkStale bool) *tileRenderResult {
	var snap metatileSnapshot
	if checkStale {
		// keep the metatile state alive across the render and remember its
		// generation for the staleness check below
		lockKey := metatileLockKeyForCacheKey(key)
		state := tileUpdateLocks.retain(lockKey)
		defer tileUpdateLocks.release(lockKey, state)
		snap = tileUpdateLocks.snapshot(state)
	}

	// render with a query-parameter-free copy of the request (?dirty and
	// friends must not leak into the provider query), keeping the original
	// context so cancellation still propagates to the provider
	renderReq := r.Clone(r.Context())
	renderReq.URL.RawQuery = ""
	renderReq.URL.Fragment = ""
	renderReq.Form = nil
	renderReq.PostForm = nil

	capture := newTileRenderCapture()
	next.ServeHTTP(capture, renderReq)

	res := capture.result(r.Context().Err() != nil)
	if res.canceled || res.status != http.StatusOK || len(res.body) == 0 {
		return res
	}

	// only MVT tiles belong in the tile cache
	if !isMVTContentType(res.header) {
		return res
	}

	if checkStale && !tileUpdateLocks.stable(snap) {
		// a ?tile=update or ?dirty regeneration rewrote this metatile while we
		// were rendering; the freshly written tiles must not be overwritten
		log.Debugf("cache middleware: skipping stale cache write for tile %v", key)
		return res
	}

	if err := cacher.Set(r.Context(), key, res.body); err != nil {
		log.Warnf("cache response writer err: %v", err)
	}
	return res
}

// tileRenderGroup deduplicates concurrent renders of the same tile key in
// flight. Waiters receive the leader's result; a waiter whose context ends
// first stops waiting and reports cancellation.
type tileRenderGroup struct {
	mu    sync.Mutex
	calls map[string]*tileRenderCall
}

type tileRenderCall struct {
	done chan struct{}
	res  *tileRenderResult
}

// do executes fn for key unless an identical call is already in flight, in
// which case it waits for and returns that call's result. The second return
// value reports whether the result was shared from another call.
func (g *tileRenderGroup) do(ctx context.Context, key string, fn func() *tileRenderResult) (res *tileRenderResult, shared bool) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*tileRenderCall)
	}
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		select {
		case <-call.done:
			return call.res, true
		case <-ctx.Done():
			return &tileRenderResult{canceled: true}, true
		}
	}
	call := &tileRenderCall{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	call.res = fn()

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(call.done)

	return call.res, false
}
