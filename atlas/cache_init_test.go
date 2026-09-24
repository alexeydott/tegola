package atlas

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-spatial/tegola/cache"
)

func TestCheckCacheTypes(t *testing.T) {
	c := cache.Registered()
	exp := []string{"azblob", "file", "gcs", "memory", "multilevel", "redis", "s3"}
	sort.Strings(exp)
	if !reflect.DeepEqual(c, exp) {
		t.Errorf("registered cachés, expected %v got %v", exp, c)
	}
}

// TestRootReadmeListsRegisteredCaches keeps the root README cache backend
// list in sync with the cache registry.
func TestRootReadmeListsRegisteredCaches(t *testing.T) {
	// registry name -> README link path
	readmeLinks := map[string]string{
		"azblob":     "(cache/azblob)",
		"file":       "(cache/file)",
		"gcs":        "(cache/gcs)",
		"memory":     "(cache/memory)",
		"multilevel": "(cache/multilevel)",
		"redis":      "(cache/redis)",
		"s3":         "(cache/s3)",
	}

	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Skipf("root README not readable from this working dir: %v", err)
	}
	content := string(readme)

	for _, name := range cache.Registered() {
		link, ok := readmeLinks[name]
		if !ok {
			// backend without a README link mapping; check bare mention
			if !strings.Contains(content, name) {
				t.Errorf("registered cache backend %q is not mentioned in root README.md", name)
			}
			continue
		}
		if !strings.Contains(content, link) {
			t.Errorf("registered cache backend %q has no [%s](cache/%s) link in root README.md", name, name, name)
		}
	}
}
