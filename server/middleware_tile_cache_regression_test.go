package server

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTileCacheResponseWriterCachesImplicitSuccessfulWrite(t *testing.T) {
	var cached bytes.Buffer
	recorder := httptest.NewRecorder()
	writer := newTileCacheResponseWriter(recorder, &cached)

	body := []byte("tile")
	if n, err := writer.Write(body); err != nil {
		t.Fatal(err)
	} else if n != len(body) {
		t.Fatalf("Write returned %d bytes, want %d", n, len(body))
	}

	if recorder.Code != 200 {
		t.Fatalf("response status = %d, want 200", recorder.Code)
	}
	if !bytes.Equal(cached.Bytes(), body) {
		t.Fatalf("cached body = %q, want %q", cached.Bytes(), body)
	}
	if !bytes.Equal(recorder.Body.Bytes(), body) {
		t.Fatalf("response body = %q, want %q", recorder.Body.Bytes(), body)
	}
}

func TestTileUpdateCoordinatorSerializesMetatileWork(t *testing.T) {
	coordinator := newTileUpdateCoordinator()
	unlock := coordinator.acquire("map/layer/4/8/8")

	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		otherUnlock := coordinator.acquire("map/layer/4/8/8")
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
