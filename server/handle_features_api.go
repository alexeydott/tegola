package server

import (
	"net/http"
	"strings"
)

func (api *FeatureAPI) serveAPI(w http.ResponseWriter, r *http.Request) {
	if !api.discoveryQueryValid(w, r) {
		return
	}
	paths := map[string]any{}
	for _, route := range []struct{ path, summary string }{
		{path: "/", summary: "Feature API landing page"},
		{path: "/api", summary: "API definition"},
		{path: "/conformance", summary: "Implemented conformance classes"},
		{path: "/collections", summary: "Published collections"},
		{path: "/collections/{collection}", summary: "Collection metadata"},
		{path: "/collections/{collection}/queryables", summary: "Queryable property schema"},
		{path: "/collections/{collection}/items", summary: "Features in a collection"},
		{path: "/collections/{collection}/items/{feature}", summary: "One feature"},
	} {
		operation := map[string]any{"summary": route.summary, "responses": map[string]any{"200": map[string]any{"description": "Successful response"}, "400": map[string]any{"description": "Invalid query parameter"}}}
		if strings.Contains(route.path, "{collection}") {
			operation["parameters"] = []any{map[string]any{"name": "collection", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}
			operation["responses"].(map[string]any)["404"] = map[string]any{"description": "Collection not found"}
		}
		if strings.Contains(route.path, "{feature}") {
			operation["parameters"] = append(operation["parameters"].([]any), map[string]any{"name": "feature", "in": "path", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[0-9]+$"}})
		}
		if route.path == "/collections/{collection}/items" {
			parameters := operation["parameters"].([]any)
			parameters = append(parameters,
				map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "default": api.cfg.DefaultLimit}, "description": "Page size; values above the publication maximum are clamped"},
				map[string]any{"name": "offset", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0}, "description": "Tegola paging offset in stable feature-ID order"},
				map[string]any{"name": "bbox", "in": "query", "style": "form", "explode": false, "schema": map[string]any{"oneOf": []any{map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 4, "maxItems": 4}, map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "minItems": 6, "maxItems": 6}}}, "description": "Four CRS84 or six CRS84h coordinates; only lengths four and six are accepted"},
				map[string]any{"name": "datetime", "in": "query", "schema": map[string]any{"type": "string"}, "description": "RFC3339 instant or interval with at most one open endpoint"},
				map[string]any{"name": "filter", "in": "query", "schema": map[string]any{"type": "string"}, "description": "CQL2 text expression over the collection Queryables catalog; combines with bbox and datetime"},
				map[string]any{"name": "filter-lang", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"cql2-text"}}, "description": "Requires filter; omitted language defaults to cql2-text. filter-crs and other encodings are unsupported"},
			)
			operation["parameters"] = parameters
		}
		if strings.Contains(route.path, "/items") {
			responses := operation["responses"].(map[string]any)
			responses["200"] = map[string]any{"description": "Successful GeoJSON response", "content": map[string]any{"application/geo+json": map[string]any{"schema": map[string]any{"type": "object"}}}}
			responses["408"] = map[string]any{"description": "Request did not complete"}
			responses["500"] = map[string]any{"description": "Feature query failed"}
			responses["501"] = map[string]any{"description": "Query profile is unsupported"}
		}
		operation["responses"].(map[string]any)["406"] = map[string]any{"description": "Requested representation is unavailable"}
		if !strings.Contains(route.path, "/items") {
			mediaType := "application/json"
			if strings.HasSuffix(route.path, "/queryables") {
				mediaType = "application/schema+json"
				operation["responses"].(map[string]any)["501"] = map[string]any{"description": "Queryable capability is unavailable"}
				operation["responses"].(map[string]any)["500"] = map[string]any{"description": "Resource failed"}
			} else if route.path == "/api" {
				mediaType = "application/vnd.oai.openapi+json;version=3.0"
			}
			operation["responses"].(map[string]any)["200"] = map[string]any{"description": "Successful response", "content": map[string]any{mediaType: map[string]any{"schema": map[string]any{"type": "object"}}}}
		}
		paths[route.path] = map[string]any{"get": operation, "head": operation}
	}
	document := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": api.cfg.Title, "description": api.cfg.Description, "version": Version},
		"servers": []any{map[string]any{"url": api.link(r, "", "", "").Href}},
		"paths":   paths,
	}
	api.writeJSON(w, r, http.StatusOK, "application/vnd.oai.openapi+json;version=3.0", document)
}
