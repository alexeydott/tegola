package server

import "net/http"

func (api *FeatureAPI) serveConformance(w http.ResponseWriter, r *http.Request) {
	if !api.discoveryQueryValid(w, r) {
		return
	}
	links := api.representationLinks(r, "/conformance", "application/json", nil)
	api.writeRepresentation(w, r, http.StatusOK, "application/json", struct {
		ConformsTo []string      `json:"conformsTo"`
		Links      []featureLink `json:"links"`
	}{ConformsTo: api.service.ConformanceClasses(), Links: links}, links)
}
