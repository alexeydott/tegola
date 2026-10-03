package server

import (
	"fmt"

	"github.com/alexeydott/tegola/feature"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/provider"
)

// buildInsertMutation converts a parsed GeoJSON feature into a neutral
// Insert command. Geometry arrives as WKB in the storage CRS (GeoJSON is
// defined as CRS84; the writer validates/transforms per its profile).
func buildInsertMutation(schema *feature.SchemaDescriptor, collection string, gf *geoJSONFeature) (provider.Mutation, error) {
	props, err := mutationInputToProvider(schema, gf.Properties)
	if err != nil {
		return provider.Mutation{}, err
	}
	m := provider.Mutation{
		Op:         provider.MutationInsert,
		Collection: collection,
		Properties: props,
	}
	if gf.HasGeometry {
		wkbBytes, err := geometryToWKB(gf.Geometry)
		if err != nil {
			return provider.Mutation{}, err
		}
		m.GeometryWKB = wkbBytes
		m.GeometrySRID = 4326 // GeoJSON default CRS
	} else if gf.GeometryNull {
		m.GeometryAbsent = true
	}
	return m, nil
}

// buildReplaceMutation converts a parsed GeoJSON feature into Replace.
// Every writable property is set; absent ones become NULL/default.
func buildReplaceMutation(schema *feature.SchemaDescriptor, collection string, featureID uint64, gf *geoJSONFeature) (provider.Mutation, error) {
	m, err := buildInsertMutation(schema, collection, gf)
	if err != nil {
		return provider.Mutation{}, err
	}
	m.Op = provider.MutationReplace
	m.FeatureID = featureID
	return m, nil
}

// buildPatchMutation applies an RFC 7396 merge patch to the current
// feature representation and builds an Update from the diff. null in the
// patch removes the JSON member; the schema decides whether that maps to
// SQL NULL (nullable) or is a validation error (required).
func buildPatchMutation(schema *feature.SchemaDescriptor, collection string, featureID uint64, current features.Feature, patch map[string]interface{}) (provider.Mutation, error) {
	// Merge patch applies to the GeoJSON representation as a whole.
	currentDoc := map[string]interface{}{
		"properties": copyProps(current.Properties),
	}
	if len(current.Geometry) > 0 && string(current.Geometry) != "null" {
		currentDoc["geometry"] = jsonRawToMap(current.Geometry)
	}
	merged := applyMergePatch(currentDoc, patch)
	mergedProps, _ := merged["properties"].(map[string]interface{})
	if mergedProps == nil {
		mergedProps = map[string]interface{}{}
	}
	// Diff against current: only changed members become Update values;
	// removed members become explicit null (schema validates).
	changed := map[string]interface{}{}
	for k, v := range mergedProps {
		cv, ok := current.Properties[k]
		if !ok || !jsonEqual(cv, v) {
			changed[k] = v
		}
	}
	for k := range current.Properties {
		if _, ok := mergedProps[k]; !ok {
			changed[k] = nil // removed by patch -> explicit null
		}
	}
	props, err := mutationInputToProvider(schema, changed)
	if err != nil {
		return provider.Mutation{}, err
	}
	m := provider.Mutation{
		Op:         provider.MutationUpdate,
		Collection: collection,
		FeatureID:  featureID,
		Properties: props,
	}
	// Geometry change via patch: parse the merged geometry if it differs.
	if mg, ok := merged["geometry"]; ok {
		if mg == nil {
			m.GeometryAbsent = true
		} else if gm, ok := mg.(map[string]interface{}); ok {
			g, err := parseGeoJSONGeometry(gm)
			if err != nil {
				return provider.Mutation{}, err
			}
			wkbBytes, err := geometryToWKB(g)
			if err != nil {
				return provider.Mutation{}, err
			}
			// Only send geometry when it actually changed.
			if !geometryEqual(current.Geometry, wkbBytes) {
				m.GeometryWKB = wkbBytes
				m.GeometrySRID = 4326
			}
		}
	}
	if len(m.Properties) == 0 && m.GeometryWKB == nil && !m.GeometryAbsent {
		return provider.Mutation{}, fmt.Errorf("patch changes nothing")
	}
	return m, nil
}

func copyProps(in map[string]any) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
