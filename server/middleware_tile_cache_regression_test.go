package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-spatial/geom/encoding/mvt"
	"github.com/go-spatial/tegola/cache"
)

func TestRenderTileForCacheCachesImplicitSuccessfulWrite(t *testing.T) {
	cacher := newFakeTileCache()
	key := &cache.Key{MapName: "m", LayerName: "l", Z: 4, X: 3, Y: 2}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mvt.MimeType)
		// no explicit WriteHeader: net/http semantics imply 200
		_, _ = w.Write([]byte("tile"))
	})

	req := httptest.NewRequest(http.MethodGet, "/maps/m/l/4/3/2", nil)
	res := renderTileForCache(req.Context(), req, handler, cacher, key, true)

	if res.status != http.StatusOK {
		t.Fatalf("rendered status = %d, want 200", res.status)
	}
	if !bytes.Equal(res.body, []byte("tile")) {
		t.Fatalf("rendered body = %q, want %q", res.body, "tile")
	}
	if got := cacher.setCount(key.String()); got != 1 {
		t.Fatalf("cacher.Set calls = %d, want 1", got)
	}
}

func TestTileUpdateCoordinatorSerializesMetatileWork(t *testing.T) {
	coordinator := newTileUpdateCoordinator()
	const lockKey = "map/layer/4/8/8"

	state, unlock, err := coordinator.acquire(context.Background(), lockKey)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer coordinator.release(lockKey, state)

	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		otherState, otherUnlock, err := coordinator.acquire(context.Background(), lockKey)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			close(done)
			return
		}
		defer coordinator.release(lockKey, otherState)
		close(acquired)
		otherUnlock()
		close(done)
	}()

	select {
	case <-acquired:
		t.Fatal("same-metatile work acquired the lock concurrently")
	case <-time.After(20 * time.Millisecond):
	}

	unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("same-metatile work did not acquire the lock after release")
	}
}

func TestTileUpdateCoordinatorAcquireHonorsContextCancel(t *testing.T) {
	coordinator := newTileUpdateCoordinator()
	const lockKey = "map/layer/4/8/8"

	state, unlock, err := coordinator.acquire(context.Background(), lockKey)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer coordinator.release(lockKey, state)
	defer unlock()

	// a waiter whose context is canceled must leave the queue immediately
	// instead of blocking until the lock is free
	ctx, cancel := context.WithCancel(context.Background())
	waitErr := make(chan error, 1)
	go func() {
		waitState, waitUnlock, err := coordinator.acquire(ctx, lockKey)
		if err == nil {
			defer coordinator.release(lockKey, waitState)
			waitUnlock()
		}
		waitErr <- err
	}()

	cancel()

	select {
	case err := <-waitErr:
		if err == nil {
			t.Fatal("canceled acquire returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled acquire stayed queued")
	}
}
