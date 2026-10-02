// Package features resolves published raw-feature collections and encodes CRS84 GeoJSON.
package features

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/alexeydott/proj"
	"github.com/alexeydott/tegola/provider"
	"github.com/alexeydott/tegola/provider/crsconfig"
)

// CollectionSource binds a public collection to an already initialized provider layer.
// Provider metadata must be final before service construction.
type CollectionSource struct {
	ID          string
	Title       string
	Description string
	Layer       provider.LayerInfo
	Querier     provider.FeatureQuerier
}

type resolvedCollection struct {
	metadata         CollectionMetadata
	layer            string
	srid             uint64
	temporal         provider.TemporalMapping
	spatial          provider.SpatialMetadata
	heightProjection *crsconfig.HeightProjection
	querier          provider.FeatureQuerier
}

// Service owns immutable collection metadata; provider implementations own their concurrency.
type Service struct{ collections map[string]resolvedCollection }

// CollectionMetadata contains detached public discovery information.
type CollectionMetadata struct{ ID, Title, Description string }

func (s *Service) Collections() []CollectionMetadata {
	metadata := make([]CollectionMetadata, 0, len(s.collections))
	for _, collection := range s.collections {
		metadata = append(metadata, collection.metadata)
	}
	sort.Slice(metadata, func(i, j int) bool { return metadata[i].ID < metadata[j].ID })
	return metadata
}

func (s *Service) Collection(id string) (CollectionMetadata, error) {
	collection, ok := s.collections[id]
	if !ok {
		return CollectionMetadata{}, CollectionNotFoundError{CollectionID: id}
	}
	return collection.metadata, nil
}

// CollectionNotFoundError identifies an unpublished collection.
type CollectionNotFoundError struct{ CollectionID string }

func (e CollectionNotFoundError) Error() string {
	return fmt.Sprintf("features: collection %q not found", e.CollectionID)
}

// FeatureNotFoundError identifies an absent feature in a published collection.
type FeatureNotFoundError struct {
	CollectionID string
	FeatureID    uint64
}

func (e FeatureNotFoundError) Error() string {
	return fmt.Sprintf("features: feature %d not found in collection %q", e.FeatureID, e.CollectionID)
}

// NewService validates publication eligibility and copies all layer metadata.
// It retains provider queriers, but never caller slices or mutable LayerInfo objects.
func NewService(sources []CollectionSource) (*Service, error) {
	service := &Service{collections: make(map[string]resolvedCollection, len(sources))}
	for _, source := range sources {
		if strings.TrimSpace(source.ID) == "" {
			return nil, fmt.Errorf("features: collection ID is blank")
		}
		if _, exists := service.collections[source.ID]; exists {
			return nil, fmt.Errorf("features: duplicate collection %q", source.ID)
		}
		if nilInterface(source.Layer) || nilInterface(source.Querier) {
			return nil, fmt.Errorf("features: collection %q has a nil layer or querier", source.ID)
		}
		layer := source.Layer.Name()
		if strings.TrimSpace(layer) == "" {
			return nil, fmt.Errorf("features: collection %q has a blank source layer", source.ID)
		}
		eligibility, ok := source.Layer.(provider.FeatureQueryLayerInfo)
		if !ok {
			return nil, fmt.Errorf("features: collection %q has unknown query eligibility: %w", source.ID, provider.ErrUnsupported)
		}
		if err := eligibility.FeatureQuerySupported(); err != nil {
			return nil, fmt.Errorf("features: collection %q query eligibility: %w", source.ID, err)
		}
		temporal, ok := source.Layer.(provider.TemporalLayerInfo)
		if !ok {
			return nil, fmt.Errorf("features: collection %q has unknown temporal metadata: %w", source.ID, provider.ErrUnsupported)
		}
		mapping, err := temporal.TemporalMapping()
		if err != nil {
			return nil, fmt.Errorf("features: collection %q temporal metadata: %w", source.ID, err)
		}
		if err := mapping.Validate(); err != nil {
			return nil, fmt.Errorf("features: collection %q temporal mapping: %w", source.ID, err)
		}
		var srid uint64
		if featureSource, ok := source.Layer.(provider.FeatureSourceLayerInfo); ok {
			srid = featureSource.FeatureSourceSRID()
		} else {
			srid = source.Layer.SRID()
		}
		spatialInfo, ok := source.Layer.(provider.SpatialLayerInfo)
		if !ok {
			return nil, fmt.Errorf("features: collection %q has unknown spatial metadata: %w", source.ID, provider.ErrUnsupported)
		}
		spatial, err := spatialInfo.SpatialMetadata()
		if err != nil {
			return nil, fmt.Errorf("features: collection %q spatial metadata: %w", source.ID, err)
		}
		if err := spatial.Validate(); err != nil {
			return nil, fmt.Errorf("features: collection %q spatial profile: %w", source.ID, err)
		}
		var heightProjection *crsconfig.HeightProjection
		if spatial.Dimension == provider.DimensionXY {
			if err := validateSRID(srid); err != nil {
				return nil, fmt.Errorf("features: collection %q CRS: %w", source.ID, err)
			}
		} else {
			// Provider eligibility establishes effective source provenance;
			// this owned canonical adapter never consults mutable registry state.
			heightProjection, err = crsconfig.NewHeightProjection(srid)
			if err != nil {
				return nil, fmt.Errorf("features: collection %q height-preserving CRS: %w: %w", source.ID, provider.ErrUnsupported, err)
			}
		}
		service.collections[source.ID] = resolvedCollection{metadata: CollectionMetadata{ID: source.ID, Title: source.Title, Description: source.Description}, layer: layer, srid: srid, temporal: mapping, spatial: spatial, heightProjection: heightProjection, querier: source.Querier}
	}
	return service, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	}
	return false
}

func validateSRID(srid uint64) error {
	if srid == 4326 {
		return nil
	}
	// EPSGCode is an int: reject narrowing before consulting the registered engine.
	if srid == 0 || uint64(proj.EPSGCode(srid)) != srid || proj.EPSGCode(srid) < 0 {
		return fmt.Errorf("invalid source SRID %d: %w", srid, provider.ErrUnsupported)
	}
	if !proj.IsKnownConversionSRID(proj.EPSGCode(srid)) {
		return fmt.Errorf("unknown source SRID %d: %w", srid, provider.ErrUnsupported)
	}
	// Construct the existing engine in both directions; a registered name alone
	// must not publish an unusable projection. Empty coordinates validate converter
	// construction without imposing an arbitrary location on local CRS domains.
	projected, err := proj.Convert(proj.EPSGCode(srid), []float64{})
	if err != nil {
		return fmt.Errorf("unsupported source CRS %d: %w: %v", srid, provider.ErrUnsupported, err)
	}
	if _, err := proj.Inverse(proj.EPSGCode(srid), projected); err != nil {
		return fmt.Errorf("unsupported inverse CRS %d: %w: %v", srid, provider.ErrUnsupported, err)
	}
	return nil
}
