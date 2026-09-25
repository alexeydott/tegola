package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"cloud.google.com/go/storage"

	"github.com/go-spatial/tegola/cache"
)

// errTransient simulates a backend failure (e.g. a network blip or a 5xx).
var errTransient = errors.New("gcs: transient backend failure")

func testKey() *cache.Key {
	return &cache.Key{MapName: "test-map", LayerName: "test-layer", Z: 0, X: 0, Y: 0}
}

// A transient backend error must be surfaced as an error, not swallowed into a
// cache miss (regression test for upstream go-spatial/tegola#938).
func TestGetTransientErrorIsNotAMiss(t *testing.T) {
	c := &GCSCache{
		newReader: func(ctx context.Context, key string) (io.ReadCloser, error) {
			return nil, errTransient
		},
	}

	val, hit, err := c.Get(context.Background(), testKey())
	if err == nil {
		t.Fatal("expected a backend error to be returned, got nil (error swallowed as a cache miss)")
	}
	if !errors.Is(err, errTransient) {
		t.Errorf("expected the backend error to propagate, got %v", err)
	}
	if hit {
		t.Error("expected hit=false on a backend error")
	}
	if val != nil {
		t.Errorf("expected nil value on a backend error, got %v", val)
	}
}

// A missing object must yield a clean, error-free miss.
func TestGetObjectNotExistIsACleanMiss(t *testing.T) {
	for name, readErr := range map[string]error{
		"bare":    storage.ErrObjectNotExist,
		"wrapped": fmt.Errorf("gcs read: %w", storage.ErrObjectNotExist),
	} {
		t.Run(name, func(t *testing.T) {
			c := &GCSCache{
				newReader: func(ctx context.Context, key string) (io.ReadCloser, error) {
					return nil, readErr
				},
			}

			val, hit, err := c.Get(context.Background(), testKey())
			if err != nil {
				t.Fatalf("a missing object must be a clean miss, got error %v", err)
			}
			if hit {
				t.Error("expected hit=false for a missing object")
			}
			if val != nil {
				t.Errorf("expected nil value for a missing object, got %v", val)
			}
		})
	}
}

// Sanity check that a successful read still returns the payload as a hit.
func TestGetSuccess(t *testing.T) {
	const want = "tile-bytes"
	c := &GCSCache{
		newReader: func(ctx context.Context, key string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(want)), nil
		},
	}

	val, hit, err := c.Get(context.Background(), testKey())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hit {
		t.Fatal("expected hit=true on a successful read")
	}
	if string(val) != want {
		t.Errorf("value mismatch: got %q, want %q", val, want)
	}
}
