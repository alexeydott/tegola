//go:build cgo

package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/geom/encoding/wkb"

	"github.com/alexeydott/tegola/config"
	"github.com/alexeydott/tegola/dict"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/provider"
	"github.com/alexeydott/tegola/provider/gpkg"
)

const wfsDDL = `CREATE TABLE wfs_sites (
	fid INTEGER PRIMARY KEY,
	geom BLOB,
	name TEXT,
	seats INTEGER
)`

func wfsService(t *testing.T) *features.Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wfs.gpkg")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(wfsDDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	conf := dict.Dict{
		"filepath": path,
		"layers": []map[string]interface{}{
			{
				"name":               "wfs_sites",
				"tablename":          "wfs_sites",
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
	gp := tiler.(*gpkg.Provider)
	layers, err := gp.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	var layer provider.LayerInfo
	for _, l := range layers {
		if l.Name() == "wfs_sites" {
			layer = l
		}
	}
	service, err := features.NewService([]features.CollectionSource{
		{ID: "wfs_sites", Title: "Sites", Layer: layer, Querier: gp},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func wfsHandler(t *testing.T, service *features.Service) (*WFSHandler, http.Handler) {
	t.Helper()
	wfsCfg := config.WFSConfig{Enabled: true, BasePath: "/wfs"}.Resolved()
	writeCfg := config.FeaturesWriteConfig{Enabled: true}
	writeCfg.Collections = []config.WriteCollectionConfig{
		{ID: "wfs_sites", Operations: []string{"create", "replace", "update", "delete"}},
	}
	h := &WFSHandler{Service: service, Config: wfsCfg, WriteConfig: writeCfg}
	preserveDiscoveryGlobals(t)
	router, err := NewRouterWithOptions(nil, RouterOptions{WFS: h})
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	return h, router
}

func TestWFSGetCapabilities(t *testing.T) {
	_, router := wfsHandler(t, wfsService(t))
	for _, v := range []string{"1.1.0", "2.0.0"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wfs?service=WFS&request=GetCapabilities&version="+v, nil))
		if rec.Code != 200 {
			t.Fatalf("version %s: status = %d", v, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "WFS_Capabilities") {
			t.Fatalf("version %s: not a capabilities document", v)
		}
		if !strings.Contains(body, "wfs_sites") {
			t.Fatalf("version %s: feature type missing", v)
		}
		if !strings.Contains(body, "Transaction") {
			t.Fatalf("version %s: Transaction not advertised", v)
		}
	}
}

func TestWFSDescribeFeatureType(t *testing.T) {
	_, router := wfsHandler(t, wfsService(t))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wfs?service=WFS&request=DescribeFeatureType&version=2.0.0&typeNames=wfs_sites", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "xsd:schema") {
		t.Fatal("not an XSD schema")
	}
	if !strings.Contains(body, `name="name"`) || !strings.Contains(body, `name="seats"`) {
		t.Fatal("properties missing from XSD")
	}
}

func TestWFSGetFeature(t *testing.T) {
	service := wfsService(t)
	_, router := wfsHandler(t, service)
	// Seed one feature via the provider directly.
	mp, _, err := service.MutationProviderFor("wfs_sites")
	if err != nil {
		t.Fatalf("mutation provider: %v", err)
	}
	tx, _ := mp.BeginFeatureTx(context.Background(), provider.TxOptions{})
	out, err := tx.Apply(context.Background(), provider.Mutation{
		Op:          provider.MutationInsert,
		Collection:  "wfs_sites",
		Properties:  map[string]provider.MutationValue{"name": wfsStrVal("seed")},
		GeometryWKB: wfsWKBPoint(t, 30, 40),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wfs?service=WFS&request=GetFeature&version=2.0.0&typeNames=wfs_sites", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "wfs:FeatureCollection") {
		t.Fatalf("not a feature collection: %s", body[:200])
	}
	if !strings.Contains(body, "wfs_sites."+strconv.FormatUint(out.FeatureID, 10)) {
		t.Fatalf("inserted feature missing from GML")
	}
	if !strings.Contains(body, "seed") {
		t.Fatal("property value missing from GML")
	}
}

func TestWFSTransaction(t *testing.T) {
	_, router := wfsHandler(t, wfsService(t))
	// Insert via WFS-T.
	insertXML := `<wfs:Transaction version="2.0.0" service="WFS" xmlns:wfs="http://www.opengis.net/wfs/2.0" xmlns:gml="http://www.opengis.net/gml/3.2" xmlns:t="http://example.com/tegola/wfs_sites">
  <wfs:Insert>
    <t:wfs_sites>
      <t:name>wfs-insert</t:name>
      <t:seats>7</t:seats>
      <t:geom><gml:Point srsName="urn:ogc:def:crs:EPSG::4326"><gml:pos>50 60</gml:pos></gml:Point></t:geom>
    </t:wfs_sites>
  </wfs:Insert>
</wfs:Transaction>`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/wfs", strings.NewReader(insertXML))
	req.Header.Set("Content-Type", "application/xml")
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("Transaction Insert status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "totalInserted>1") {
		t.Fatalf("insert not acknowledged: %s", body)
	}
	// Extract the assigned FID.
	fid := extractFID(t, body)

	// GetFeature should return it.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wfs?service=WFS&request=GetFeature&version=1.1.0&typeName=wfs_sites", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "wfs-insert") {
		t.Fatalf("inserted feature not readable via WFS 1.1: %d", rec.Code)
	}

	// Update via WFS-T.
	updateXML := `<wfs:Transaction version="2.0.0" service="WFS" xmlns:wfs="http://www.opengis.net/wfs/2.0" xmlns:fes="http://www.opengis.net/fes/2.0">
  <wfs:Update typeName="wfs_sites">
    <wfs:Property><wfs:ValueReference>seats</wfs:ValueReference><wfs:Value>8</wfs:Value></wfs:Property>
    <fes:Filter><fes:ResourceId rid="` + fid + `"/></fes:Filter>
  </wfs:Update>
</wfs:Transaction>`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/wfs", strings.NewReader(updateXML))
	req.Header.Set("Content-Type", "application/xml")
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "totalUpdated>1") {
		t.Fatalf("Transaction Update failed: %d %s", rec.Code, rec.Body.String())
	}

	// Delete via WFS-T.
	deleteXML := `<wfs:Transaction version="1.1.0" service="WFS" xmlns:wfs="http://www.opengis.net/wfs" xmlns:ogc="http://www.opengis.net/ogc">
  <wfs:Delete typeName="wfs_sites">
    <ogc:Filter><ogc:FeatureId fid="` + fid + `"/></ogc:Filter>
  </wfs:Delete>
</wfs:Transaction>`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/wfs", strings.NewReader(deleteXML))
	req.Header.Set("Content-Type", "application/xml")
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "totalDeleted>1") {
		t.Fatalf("Transaction Delete failed: %d %s", rec.Code, rec.Body.String())
	}
}

func extractFID(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `rid="`)
	if i < 0 {
		i = strings.Index(body, `fid="`)
		if i < 0 {
			t.Fatalf("no FID in response: %s", body)
		}
		i += 5
	} else {
		i += 5
	}
	j := strings.Index(body[i:], `"`)
	return body[i : i+j]
}

func wfsStrVal(s string) provider.MutationValue {
	return provider.MutationValue{Kind: provider.MutationValueString, String: s}
}

func wfsWKBPoint(t *testing.T, x, y float64) []byte {
	t.Helper()
	b, err := wkb.EncodeBytes(geom.Point{x, y})
	if err != nil {
		t.Fatalf("wkb: %v", err)
	}
	return b
}
