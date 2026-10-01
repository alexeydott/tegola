package server

import "net/http"

func (api *FeatureAPI) serveConformance(w http.ResponseWriter, r *http.Request) {
	if !api.discoveryQueryValid(w, r) {
		return
	}
	// Discovery alone proves no complete OGC conformance class.
	api.writeJSON(w, r, http.StatusOK, "application/json", struct {
		ConformsTo []string `json:"conformsTo"`
	}{ConformsTo: []string{}})
}
