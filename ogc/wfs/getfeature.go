package wfs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/tegola/feature"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/ogc/wfs/gml"
	"github.com/alexeydott/tegola/provider"
)

// GetFeatureRequest is the parsed GetFeature input (KVP subset).
type GetFeatureRequest struct {
	Version    Version
	TypeName   string
	MaxFeatures uint
	// BBox is an optional spatial filter in lon,lat order (CRS84).
	BBox      *[4]float64
	FeatureIDs []uint64
	// OutputFormat: only "application/gml+xml" variants are supported.
	OutputFormat string
}

// ParseGetFeatureKVP parses the KVP subset for GetFeature.
func ParseGetFeatureKVP(v Version, q map[string]string) (*GetFeatureRequest, []Exception) {
	req := &GetFeatureRequest{Version: v, MaxFeatures: 1000}
	typeName := q["typename"]
	if typeName == "" {
		typeName = q["typenames"]
	}
	if typeName == "" {
		return nil, []Exception{{Code: ExceptionMissingParameterValue, Locator: "typeName", Text: "typeName is required"}}
	}
	// Only single type names in this profile.
	if strings.Contains(typeName, ",") {
		return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "typeName", Text: "multiple type names not supported"}}
	}
	req.TypeName = strings.TrimSpace(typeName)
	if mf := q["maxfeatures"]; mf == "" {
		mf = q["count"]
		if mf != "" {
			n, err := strconv.ParseUint(mf, 10, 32)
			if err != nil {
				return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "count", Text: "invalid count"}}
			}
			req.MaxFeatures = uint(n)
		}
	} else {
		n, err := strconv.ParseUint(mf, 10, 32)
		if err != nil {
			return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "maxFeatures", Text: "invalid maxFeatures"}}
		}
		req.MaxFeatures = uint(n)
	}
	if bbox := q["bbox"]; bbox != "" {
		parts := strings.Split(bbox, ",")
		if len(parts) != 4 {
			return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "bbox", Text: "bbox needs 4 ordinates"}}
		}
		var b [4]float64
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "bbox", Text: "invalid bbox ordinate"}}
			}
			b[i] = f
		}
		req.BBox = &b
	}
	if fids := q["featureid"]; fids != "" {
		for _, fid := range strings.Split(fids, ",") {
			_, id, err := feature.DecodeWFSFID(strings.TrimSpace(fid))
			if err != nil {
				return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "featureId", Text: fmt.Sprintf("invalid feature ID %q", fid)}}
			}
			req.FeatureIDs = append(req.FeatureIDs, id)
		}
	}
	return req, nil
}

// ExecuteGetFeature runs the request and renders a GML FeatureCollection.
func ExecuteGetFeature(ctx context.Context, service *features.Service, req *GetFeatureRequest) (string, []Exception) {
	// Collection must be published.
	if _, err := service.Collection(req.TypeName); err != nil {
		return "", []Exception{{Code: ExceptionInvalidParameterValue, Locator: "typeName", Text: fmt.Sprintf("unknown type %q", req.TypeName)}}
	}
	fq := provider.FeatureQuery{Limit: req.MaxFeatures}
	if fq.Limit == 0 {
		fq.Limit = 1
	}
	if req.BBox != nil {
		b := req.BBox
		fq.Bounds = []geom.Extent{*geom.NewExtent([2]float64{b[0], b[1]}, [2]float64{b[2], b[3]})}
		fq.BoundsSRID = 4326
	}
	if len(req.FeatureIDs) > 0 {
		fq.IDs = req.FeatureIDs
	}
	schema, err := service.SchemaDescriptorFor(ctx, req.TypeName)
	if err != nil {
		return "", []Exception{{Code: ExceptionNoApplicableCode, Text: "schema unavailable"}}
	}
	var gv gml.Version
	if req.Version == V110 {
		gv = gml.V311
	} else {
		gv = gml.V321
	}
	enc := &gml.Encoder{Version: gv, SRID: 4326}
	// Query via the service and encode each feature as GML.
	count := 0
	_, err = service.QueryCollection(ctx, req.TypeName, fq, func(f features.Feature) error {
		fid, err := feature.EncodeWFSFID(req.TypeName, f.ID)
		if err != nil {
			return err
		}
		gf := gml.Feature{
			ID:           fid,
			TypeName:     req.TypeName,
			GeometryName: "geometry",
			Properties:   map[string]string{},
		}
		// Geometry: parse the GeoJSON geometry from the service feature.
		if len(f.Geometry) > 0 && string(f.Geometry) != "null" {
			g, err := parseServiceGeometry(f.Geometry)
			if err != nil {
				return err
			}
			gf.Geometry = g
		}
		for k, v := range f.Properties {
			gf.Properties[k] = fmt.Sprintf("%v", v)
		}
		if err := enc.EncodeFeature(gf); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return "", []Exception{{Code: ExceptionNoApplicableCode, Text: fmt.Sprintf("query failed: %v", err)}}
	}
	_ = schema
	return featureCollectionEnvelope(req.Version, enc.String(), count), nil
}

func featureCollectionEnvelope(v Version, members string, count int) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	if v == V110 {
		sb.WriteString(`<wfs:FeatureCollection xmlns:wfs="http://www.opengis.net/wfs" xmlns:gml="http://www.opengis.net/gml" xmlns:ogc="http://www.opengis.net/ogc" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="http://www.opengis.net/wfs http://schemas.opengis.net/wfs/1.1.0/wfs.xsd" numberOfFeatures="` + strconv.Itoa(count) + `">` + "\n")
	} else {
		sb.WriteString(`<wfs:FeatureCollection xmlns:wfs="http://www.opengis.net/wfs/2.0" xmlns:gml="http://www.opengis.net/gml/3.2" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="http://www.opengis.net/wfs/2.0 http://schemas.opengis.net/wfs/2.0/wfs.xsd" numberMatched="` + strconv.Itoa(count) + `" numberReturned="` + strconv.Itoa(count) + `">` + "\n")
	}
	sb.WriteString(members)
	sb.WriteString("</wfs:FeatureCollection>\n")
	return sb.String()
}
