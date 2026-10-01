package server

import "net/http"

func (api *FeatureAPI) serveLanding(w http.ResponseWriter, r *http.Request) {
	if !api.discoveryQueryValid(w, r) {
		return
	}
	response := struct {
		Title       string        `json:"title,omitempty"`
		Description string        `json:"description,omitempty"`
		Links       []featureLink `json:"links"`
	}{
		Title: api.cfg.Title, Description: api.cfg.Description,
		Links: []featureLink{
			api.link(r, "", "self", "application/json"),
			api.link(r, "/api", "service-desc", "application/vnd.oai.openapi+json;version=3.0"),
			api.link(r, "/conformance", "conformance", "application/json"),
			api.link(r, "/collections", "data", "application/json"),
		},
	}
	api.writeJSON(w, r, http.StatusOK, "application/json", response)
}
