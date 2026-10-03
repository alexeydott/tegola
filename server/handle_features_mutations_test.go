//go:build cgo

package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/alexeydott/tegola/config"
	"github.com/alexeydott/tegola/dict"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/provider"
	"github.com/alexeydott/tegola/provider/gpkg"
)

const part4DDL = `CREATE TABLE sites (
	fid INTEGER PRIMARY KEY,
	geom BLOB,
	name TEXT,
	seats INTEGER
)`

func part4Service(t *testing.T) (*features.Service, *gpkg.Provider) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "part4.gpkg")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(part4DDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	conf := dict.Dict{
		"filepath": path,
		"layers": []map[string]interface{}{
			{
				"name":               "sites",
				"tablename":          "sites",
				"id_fieldname":       "fid",
				"geometry_fieldname": "geom",
				"geometry_format":    "wkb",
				"srid":               4326,
				"fields":             []string{"name", "seats"},
			},
		},
	}
	tiler, err := gpkg.NewTileProvider(conf, nil)
	if err != nil {
		t.Fatalf("NewTileProvider: %v", err)
	}
	t.Cleanup(gpkg.Cleanup)
	gp, ok := tiler.(*gpkg.Provider)
	if !ok {
		t.Fatalf("not a *gpkg.Provider")
	}
	layers, err := gp.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	var layer provider.LayerInfo
	for _, l := range layers {
		if l.Name() == "sites" {
			layer = l
		}
	}
	if layer == nil {
		t.Fatalf("layer not found")
	}
	service, err := features.NewService([]features.CollectionSource{
		{ID: "sites", Title: "Sites", Layer: layer, Querier: gp},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, gp
}

func part4API(t *testing.T, service *features.Service) *FeatureAPI {
	t.Helper()
	writeCfg := config.FeaturesWriteConfig{Enabled: true}
	writeCfg.Collections = []config.WriteCollectionConfig{
		{ID: "sites", Operations: []string{"create", "replace", "update", "delete"}},
	}
	api, err := NewFeatureAPI(service, FeatureAPIConfig{
		BasePath:     "/features",
		DefaultLimit: 100,
		MaxLimit:     10000,
		Title:        "t",
		Write:        writeCfg,
	})
	if err != nil {
		t.Fatalf("NewFeatureAPI: %v", err)
	}
	return api
}

func part4Router(t *testing.T, api *FeatureAPI) http.Handler {
	t.Helper()
	preserveDiscoveryGlobals(t)
	router, err := NewRouterWithOptions(nil, RouterOptions{Features: api})
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	return router
}

func doRequest(t *testing.T, router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPart4CRUD(t *testing.T) {
	service, _ := part4Service(t)
	router := part4Router(t, part4API(t, service))
	ct := map[string]string{"Content-Type": "application/geo+json"}

	// Create.
	createBody := `{"type":"Feature","geometry":{"type":"Point","coordinates":[10,20]},"properties":{"name":"hq","seats":42}}`
	rec := doRequest(t, router, http.MethodPost, "/features/collections/sites/items", createBody, ct)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("missing Location header")
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag header")
	}
	var created map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := int(created["id"].(float64))

	// GET the item by parsed id path.
	getPath := "/features/collections/sites/items/" + strconv.Itoa(id)
	rec = doRequest(t, router, http.MethodGet, getPath, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	getETag := rec.Header().Get("ETag")
	if getETag == "" {
		t.Fatal("GET missing ETag")
	}

	// PUT with stale If-Match -> 412.
	rec = doRequest(t, router, http.MethodPut, getPath,
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[11,21]},"properties":{"name":"hq2","seats":43}}`,
		map[string]string{"Content-Type": "application/geo+json", "If-Match": `"stale"`})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("PUT stale If-Match status = %d, want 412", rec.Code)
	}

	// PUT with correct If-Match -> 200.
	rec = doRequest(t, router, http.MethodPut, getPath,
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[11,21]},"properties":{"name":"hq2","seats":43}}`,
		map[string]string{"Content-Type": "application/geo+json", "If-Match": getETag})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body: %s", rec.Code, rec.Body.String())
	}
	newETag := rec.Header().Get("ETag")
	if newETag == "" || newETag == getETag {
		t.Fatal("PUT should return a new ETag")
	}

	// PATCH merge-patch: change seats only.
	rec = doRequest(t, router, http.MethodPatch, getPath, `{"properties":{"seats":100}}`,
		map[string]string{"Content-Type": "application/merge-patch+json", "If-Match": newETag})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var patched map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	props := patched["properties"].(map[string]interface{})
	if props["seats"].(float64) != 100 {
		t.Fatalf("seats = %v, want 100", props["seats"])
	}
	if props["name"].(string) != "hq2" {
		t.Fatalf("name = %v, want hq2 (preserved)", props["name"])
	}
	patchETag := rec.Header().Get("ETag")

	// DELETE with If-Match -> 204.
	rec = doRequest(t, router, http.MethodDelete, getPath, "",
		map[string]string{"If-Match": patchETag})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, body: %s", rec.Code, rec.Body.String())
	}

	// GET after delete -> 404.
	rec = doRequest(t, router, http.MethodGet, getPath, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d, want 404", rec.Code)
	}
}

func TestPart4OPTIONS(t *testing.T) {
	service, _ := part4Service(t)
	router := part4Router(t, part4API(t, service))

	rec := doRequest(t, router, http.MethodOptions, "/features/collections/sites/items", "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d", rec.Code)
	}
	allow := rec.Header().Get("Allow")
	if !strings.Contains(allow, "POST") {
		t.Fatalf("Allow = %q, want POST", allow)
	}

	rec = doRequest(t, router, http.MethodOptions, "/features/collections/sites/items/1", "", nil)
	allow = rec.Header().Get("Allow")
	for _, m := range []string{"PUT", "PATCH", "DELETE"} {
		if !strings.Contains(allow, m) {
			t.Fatalf("Allow = %q, want %s", allow, m)
		}
	}
	if ap := rec.Header().Get("Accept-Patch"); ap != "application/merge-patch+json" {
		t.Fatalf("Accept-Patch = %q", ap)
	}
}

func TestPart4WriteDisabled(t *testing.T) {
	service, _ := part4Service(t)
	// No write config: all mutations must be unavailable.
	api, err := NewFeatureAPI(service, FeatureAPIConfig{
		BasePath: "/features", DefaultLimit: 100, MaxLimit: 10000, Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := part4Router(t, api)
	rec := doRequest(t, router, http.MethodPost, "/features/collections/sites/items",
		`{"type":"Feature","geometry":null,"properties":{}}`,
		map[string]string{"Content-Type": "application/geo+json"})
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST with write disabled status = %d, want 404/405", rec.Code)
	}
}

