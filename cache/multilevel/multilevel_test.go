package multilevel

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/go-spatial/tegola/cache"
	"github.com/go-spatial/tegola/dict"
)

type fakeCache struct {
	value       []byte
	hit         bool
	getErr      error
	setErr      error
	purgeErr    error
	setCalls    int
	purgeCalls  int
	lastSetData []byte
}

func (c *fakeCache) Get(context.Context, *cache.Key) ([]byte, bool, error) {
	return c.value, c.hit, c.getErr
}

func (c *fakeCache) Set(_ context.Context, _ *cache.Key, value []byte) error {
	c.setCalls++
	c.lastSetData = append([]byte(nil), value...)
	return c.setErr
}

func (c *fakeCache) Purge(context.Context, *cache.Key) error {
	c.purgeCalls++
	return c.purgeErr
}

func TestGetUsesMemoryBeforeFile(t *testing.T) {
	memory := &fakeCache{value: []byte("memory"), hit: true}
	file := &fakeCache{value: []byte("file"), hit: true}
	c := &Cache{memory: memory, file: file}

	value, hit, err := c.Get(context.Background(), &cache.Key{Z: 1, X: 1, Y: 1})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !hit || !reflect.DeepEqual(value, []byte("memory")) {
		t.Fatalf("Get() = (%q, %v), want (%q, true)", value, hit, "memory")
	}
	if file.setCalls != 0 {
		t.Fatalf("file backend received %d Set calls, want 0", file.setCalls)
	}
}

func TestGetPromotesFileHit(t *testing.T) {
	memory := &fakeCache{}
	file := &fakeCache{value: []byte("file"), hit: true}
	c := &Cache{memory: memory, file: file}

	value, hit, err := c.Get(context.Background(), &cache.Key{Z: 1, X: 1, Y: 1})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !hit || !reflect.DeepEqual(value, []byte("file")) {
		t.Fatalf("Get() = (%q, %v), want (%q, true)", value, hit, "file")
	}
	if memory.setCalls != 1 || !reflect.DeepEqual(memory.lastSetData, []byte("file")) {
		t.Fatalf("memory promotion = (%d, %q), want (1, %q)", memory.setCalls, memory.lastSetData, "file")
	}
}

func TestGetPromotionFailureKeepsFileHit(t *testing.T) {
	promotionErr := errors.New("memory unavailable")
	memory := &fakeCache{setErr: promotionErr}
	file := &fakeCache{value: []byte("file"), hit: true}
	c := &Cache{memory: memory, file: file}

	value, hit, err := c.Get(context.Background(), &cache.Key{Z: 1, X: 1, Y: 1})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !hit || !reflect.DeepEqual(value, []byte("file")) {
		t.Fatalf("Get() = (%q, %v), want (%q, true)", value, hit, "file")
	}
}

func TestSetAndPurgeUseBothLevels(t *testing.T) {
	memoryErr := errors.New("memory set failed")
	fileErr := errors.New("file purge failed")
	memory := &fakeCache{setErr: memoryErr}
	file := &fakeCache{purgeErr: fileErr}
	c := &Cache{memory: memory, file: file}
	key := &cache.Key{Z: 1, X: 1, Y: 1}

	if err := c.Set(context.Background(), key, []byte("tile")); !errors.Is(err, memoryErr) {
		t.Fatalf("Set() error = %v, want memory error", err)
	}
	if memory.setCalls != 1 || file.setCalls != 1 {
		t.Fatalf("Set() calls = (%d, %d), want (1, 1)", memory.setCalls, file.setCalls)
	}

	if err := c.Purge(context.Background(), key); !errors.Is(err, fileErr) {
		t.Fatalf("Purge() error = %v, want file error", err)
	}
	if memory.purgeCalls != 1 || file.purgeCalls != 1 {
		t.Fatalf("Purge() calls = (%d, %d), want (1, 1)", memory.purgeCalls, file.purgeCalls)
	}
}

func TestNewRequiresNestedBackends(t *testing.T) {
	if _, err := New(dict.Dict{}); !errors.Is(err, ErrMemoryConfigMissing) {
		t.Fatalf("New() error = %v, want %v", err, ErrMemoryConfigMissing)
	}
}

func TestNewWithMemoryAndFileBackends(t *testing.T) {
	c, err := New(dict.Dict{
		ConfigKeyMemory: dict.Dict{},
		ConfigKeyFile: dict.Dict{
			"basepath": t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	key := &cache.Key{MapName: "map", LayerName: "layer", Z: 1, X: 2, Y: 3}
	if err := c.Set(context.Background(), key, []byte("tile")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if value, hit, err := c.Get(context.Background(), key); err != nil {
		t.Fatalf("Get() error = %v", err)
	} else if !hit || !reflect.DeepEqual(value, []byte("tile")) {
		t.Fatalf("Get() = (%q, %v), want (%q, true)", value, hit, "tile")
	}
}
