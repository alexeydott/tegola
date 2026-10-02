package server

import (
	"errors"
	"net/http"

	"github.com/alexeydott/tegola/ogc/features"
	"github.com/dimfeld/httptreemux"
)

type featureCollectionDescription struct {
	ID          string        `json:"id"`
	Title       string        `json:"title,omitempty"`
	Description string        `json:"description,omitempty"`
	ItemType    string        `json:"itemType"`
	Links       []featureLink `json:"links"`
}

func (api *FeatureAPI) describeCollection(r *http.Request, metadata features.CollectionMetadata) featureCollectionDescription {
	description := featureCollectionDescription{ID: metadata.ID, Title: metadata.Title, Description: metadata.Description, ItemType: "feature", Links: []featureLink{api.link(r, "/collections/"+metadata.ID, "self", "application/json"), api.link(r, "/collections/"+metadata.ID+"/items", "items", "application/geo+json")}}
	if _, err := api.service.Queryables(metadata.ID); err == nil {
		description.Links = append(description.Links, api.link(r, "/collections/"+metadata.ID+"/queryables", "http://www.opengis.net/def/rel/ogc/1.0/queryables", "application/schema+json"))
	}
	return description
}

func (api *FeatureAPI) serveCollections(w http.ResponseWriter, r *http.Request) {
	if !api.discoveryQueryValid(w, r) {
		return
	}
	metadata := api.service.Collections()
	collections := make([]featureCollectionDescription, 0, len(metadata))
	for _, collection := range metadata {
		collections = append(collections, api.describeCollection(r, collection))
	}
	response := struct {
		Collections []featureCollectionDescription `json:"collections"`
		Links       []featureLink                  `json:"links"`
	}{Collections: collections, Links: []featureLink{api.link(r, "/collections", "self", "application/json")}}
	api.writeJSON(w, r, http.StatusOK, "application/json", response)
}

func (api *FeatureAPI) serveCollection(w http.ResponseWriter, r *http.Request) {
	if !api.discoveryQueryValid(w, r) {
		return
	}
	id := httptreemux.ContextParams(r.Context())["collection"]
	metadata, err := api.service.Collection(id)
	if err != nil {
		var missing features.CollectionNotFoundError
		if errors.As(err, &missing) {
			api.writeError(w, r, http.StatusNotFound, "NotFound", "Collection not found")
			return
		}
		api.writeError(w, r, http.StatusInternalServerError, "InternalError", "Collection lookup failed")
		return
	}
	api.writeJSON(w, r, http.StatusOK, "application/json", api.describeCollection(r, metadata))
}
