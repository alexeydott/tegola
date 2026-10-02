package features

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/proj"
	"github.com/alexeydott/tegola/basic"
	"github.com/alexeydott/tegola/internal/log"
	"github.com/alexeydott/tegola/provider"
	"github.com/alexeydott/tegola/provider/crsconfig"
	codec "github.com/alexeydott/tegola/provider/geometrycodec"
)

// QueryCollection emits independently owned CRS84 GeoJSON features. Bounds remain
// in the caller's CRS: exact selection and paging belong to the provider.
func (s *Service) QueryCollection(
	ctx context.Context,
	collectionID string,
	query provider.FeatureQuery,
	fn func(Feature) error,
) (provider.FeatureQueryResult, error) {
	log.Logger().Debug("querying feature collection", "collection", collectionID, "limit", query.Limit)
	defer log.Logger().Debug("feature collection query finished", "collection", collectionID)
	if err := ctx.Err(); err != nil {
		return provider.FeatureQueryResult{}, err
	}
	collection, ok := s.collections[collectionID]
	if !ok {
		return provider.FeatureQueryResult{}, CollectionNotFoundError{CollectionID: collectionID}
	}
	if fn == nil {
		return provider.FeatureQueryResult{}, fmt.Errorf("features: nil callback")
	}
	if err := query.Validate(); err != nil {
		return provider.FeatureQueryResult{}, err
	}
	if query.Filter != nil {
		catalog, err := s.Queryables(collectionID)
		if err != nil {
			return provider.FeatureQueryResult{}, err
		}
		resolved, err := provider.ResolveFeatureFilter(*query.Filter, catalog)
		if err != nil {
			return provider.FeatureQueryResult{}, err
		}
		filter := resolved.Expression()
		query.Filter = &filter
	}
	if len(query.Bounds3D) != 0 && query.BoundsVerticalCRS != provider.CRS84h {
		return provider.FeatureQueryResult{}, fmt.Errorf("features: unsupported query height reference: %w", provider.ErrUnsupported)
	}
	if collection.heightProjection != nil && (len(query.Bounds) != 0 || len(query.Bounds3D) != 0) {
		if _, err := crsconfig.NewHeightProjection(query.BoundsSRID); err != nil {
			return provider.FeatureQueryResult{}, fmt.Errorf("features: unsupported height-preserving query CRS: %w: %w", provider.ErrUnsupported, err)
		}
	}
	var delivered uint64
	var callbackError error
	result, err := collection.querier.QueryFeatures(ctx, collection.layer, query, func(source *provider.Feature) error {
		fail := func(err error) error { callbackError = err; return err }
		if callbackError != nil {
			return callbackError
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if delivered >= uint64(query.Limit) {
			return fail(fmt.Errorf("features: provider exceeded page limit"))
		}
		if source == nil {
			return fail(fmt.Errorf("features: provider delivered nil feature"))
		}
		if source.SRID != collection.srid {
			return fail(fmt.Errorf("features: source SRID differs from frozen collection SRID"))
		}
		feature, err := encodeFeature(ctx, source, collection)
		if err != nil {
			return fail(err)
		}
		if err := fn(feature); err != nil {
			return fail(err)
		}
		delivered++
		return nil
	})
	if callbackError != nil {
		return result, fmt.Errorf("features: collection %q callback: %w", collectionID, callbackError)
	}
	if err != nil {
		return result, fmt.Errorf("features: collection %q query: %w", collectionID, err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if result.NumberReturned != delivered {
		return result, fmt.Errorf("features: provider callback/count disagreement")
	}
	if result.NumberMatched != nil {
		count := *result.NumberMatched
		if count < delivered {
			return result, fmt.Errorf("features: provider matched count below returned count")
		}
		result.NumberMatched = &count
	}
	return result, nil
}

// QueryFeature queries one ID without a tile or a collection scan fallback.
func (s *Service) QueryFeature(ctx context.Context, collectionID string, featureID uint64) (Feature, error) {
	var feature Feature
	var found bool
	_, err := s.QueryCollection(ctx, collectionID, provider.FeatureQuery{IDs: []uint64{featureID}, Limit: 1}, func(value Feature) error {
		if value.ID != featureID {
			return fmt.Errorf("features: provider returned an unexpected feature ID")
		}
		feature, found = value, true
		return nil
	})
	if err != nil {
		return Feature{}, err
	}
	if !found {
		return Feature{}, FeatureNotFoundError{CollectionID: collectionID, FeatureID: featureID}
	}
	return feature, nil
}

// QueryCollectionPage buffers at most Limit features and discards partial pages on error.
func (s *Service) QueryCollectionPage(
	ctx context.Context,
	collectionID string,
	query provider.FeatureQuery,
) (FeatureCollection, error) {
	page := FeatureCollection{Type: "FeatureCollection", Features: []Feature{}}
	result, err := s.QueryCollection(ctx, collectionID, query, func(feature Feature) error {
		page.Features = append(page.Features, feature)
		return nil
	})
	if err != nil {
		return FeatureCollection{}, err
	}
	page.NumberReturned, page.NumberMatched, page.HasMore = result.NumberReturned, result.NumberMatched, result.HasMore
	return page, nil
}

func encodeFeature(ctx context.Context, source *provider.Feature, collection resolvedCollection) (Feature, error) {
	if err := codec.ValidateFeatureSpatialGeometry(source.Geometry); err != nil {
		return Feature{}, serviceSpatialError(err)
	}
	if err := validateGeometryDimension(source.Geometry, collection.spatial.Dimension); err != nil {
		return Feature{}, err
	}
	var geometry geom.Geometry
	var err error
	if collection.heightProjection == nil {
		geometry, err = transformGeometry(ctx, source.Geometry, source.SRID)
	} else {
		geometry, err = codec.TransformFeatureSpatialGeometry(source.Geometry, func(point [2]float64) ([2]float64, error) {
			if err := ctx.Err(); err != nil {
				return [2]float64{}, err
			}
			output, err := collection.heightProjection.Inverse([]float64{point[0], point[1]})
			if err != nil {
				return [2]float64{}, err
			}
			if len(output) != 2 || !finite(output[0]) || !finite(output[1]) || output[0] < -180 || output[0] > 180 || output[1] < -90 || output[1] > 90 {
				return [2]float64{}, fmt.Errorf("coordinate outside CRS84 domain")
			}
			return [2]float64{output[0], output[1]}, nil
		})
	}
	if err != nil {
		return Feature{}, fmt.Errorf("features: transform geometry: %w", serviceSpatialError(err))
	}
	if collection.heightProjection != nil {
		geometry = normalizeDimensionalAbsence(geometry)
	}
	encoded, err := encodeGeometry(geometry)
	if err != nil {
		return Feature{}, fmt.Errorf("features: encode geometry: %w", serviceSpatialError(err))
	}
	// A JSON-domain snapshot preserves numeric precision through UseNumber,
	// snapshots custom marshalers and detaches every nested mutable property.
	properties := map[string]any{}
	if source.Tags != nil {
		raw, err := json.Marshal(source.Tags)
		if err != nil {
			return Feature{}, fmt.Errorf("features: encode properties: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&properties); err != nil {
			return Feature{}, fmt.Errorf("features: snapshot properties: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Feature{}, err
	}
	return Feature{Type: "Feature", ID: source.ID, Geometry: encoded, Properties: properties}, nil
}

func transformGeometry(ctx context.Context, geometry geom.Geometry, srid uint64) (geom.Geometry, error) {
	if geometry == nil {
		return nil, nil
	}
	transformed, err := basic.ApplyToPoints(geometry, func(coords ...float64) ([]float64, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(coords) != 2 || !finite(coords[0]) || !finite(coords[1]) {
			return nil, fmt.Errorf("nonfinite or unsupported coordinate")
		}
		output := []float64{coords[0], coords[1]}
		if srid != 4326 {
			var err error
			output, err = proj.Inverse(proj.EPSGCode(srid), output)
			if err != nil {
				return nil, fmt.Errorf("source CRS transform: %w", err)
			}
		}
		if len(output) != 2 || !finite(output[0]) || !finite(output[1]) {
			return nil, fmt.Errorf("nonfinite transform result")
		}
		if output[0] < -180 || output[0] > 180 || output[1] < -90 || output[1] > 90 {
			return nil, fmt.Errorf("coordinate outside CRS84 domain")
		}
		return output, nil
	})
	// Older shared traversal helpers format nested errors with %v. Preserve the
	// context sentinel independently when cancellation occurs within a collection.
	if cancellation := ctx.Err(); cancellation != nil {
		return nil, cancellation
	}
	return transformed, err
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func serviceSpatialError(err error) error {
	if errors.Is(err, codec.ErrUnsupportedFeatureSpatialGeometry) {
		return fmt.Errorf("%w: %w", provider.ErrUnsupported, err)
	}
	return err
}

// Validate each nonempty child against frozen declarations, even without bbox.
// Empty geometry remains absence; a mixed profile never synthesizes Z for XY.
func validateGeometryDimension(g geom.Geometry, dimension provider.CoordinateDimension) error {
	var xyz bool
	switch v := g.(type) {
	case nil:
		return nil
	case geom.Collection:
		for _, child := range v {
			if err := validateGeometryDimension(child, dimension); err != nil {
				return err
			}
		}
		return nil
	case geom.Point:
		xyz = false
	case geom.PointZ:
		xyz = true
	case geom.MultiPoint:
		if len(v) == 0 {
			return nil
		}
	case geom.MultiPointZ:
		if len(v) == 0 {
			return nil
		}
		xyz = true
	case geom.LineString:
		if len(v) == 0 {
			return nil
		}
	case geom.LineStringZ:
		if len(v) == 0 {
			return nil
		}
		xyz = true
	case geom.Polygon:
		if len(v) == 0 {
			return nil
		}
	case geom.PolygonZ:
		if len(v) == 0 {
			return nil
		}
		xyz = true
	case geom.MultiLineString:
		for _, line := range v {
			if err := validateGeometryDimension(geom.LineString(line), dimension); err != nil {
				return err
			}
		}
		return nil
	case geom.MultiLineStringZ:
		for _, line := range v {
			if err := validateGeometryDimension(geom.LineStringZ(line), dimension); err != nil {
				return err
			}
		}
		return nil
	case geom.MultiPolygon:
		for _, polygon := range v {
			if err := validateGeometryDimension(geom.Polygon(polygon), dimension); err != nil {
				return err
			}
		}
		return nil
	case codec.MultiPolygonZ:
		for _, polygon := range v {
			if err := validateGeometryDimension(geom.PolygonZ(polygon), dimension); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("features: unsupported geometry dimension: %w", provider.ErrUnsupported)
	}
	if dimension == provider.DimensionMixedXYXYZ || dimension == provider.DimensionXYZ && xyz || dimension == provider.DimensionXY && !xyz {
		return nil
	}
	return fmt.Errorf("features: geometry dimension differs from frozen source profile")
}

// Only the new XYZ/mixed response path normalizes decoded absence. Traversal
// has already validated every child and detached storage; malformed shapes
// cannot be hidden here. Nonempty family, order and coordinates stay intact.
func normalizeDimensionalAbsence(g geom.Geometry) geom.Geometry {
	switch v := g.(type) {
	case nil:
		return nil
	case geom.MultiPoint:
		if len(v) == 0 {
			return nil
		}
	case geom.MultiPointZ:
		if len(v) == 0 {
			return nil
		}
	case geom.LineString:
		if len(v) == 0 {
			return nil
		}
	case geom.LineStringZ:
		if len(v) == 0 {
			return nil
		}
	case geom.Polygon:
		if len(v) == 0 {
			return nil
		}
	case geom.PolygonZ:
		if len(v) == 0 {
			return nil
		}
	case geom.MultiLineString:
		out := v[:0]
		for _, line := range v {
			if len(line) != 0 {
				out = append(out, line)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case geom.MultiLineStringZ:
		out := v[:0]
		for _, line := range v {
			if len(line) != 0 {
				out = append(out, line)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case geom.MultiPolygon:
		out := v[:0]
		for _, p := range v {
			if len(p) != 0 {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case codec.MultiPolygonZ:
		out := v[:0]
		for _, p := range v {
			if len(p) != 0 {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case geom.Collection:
		out := v[:0]
		for _, child := range v {
			if normalized := normalizeDimensionalAbsence(child); normalized != nil {
				out = append(out, normalized)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return g
}
