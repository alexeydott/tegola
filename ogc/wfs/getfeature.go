package wfs

import (
	"context"
	"fmt"
	"sort"
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
	Version     Version
	TypeName    string
	MaxFeatures uint
	// BBox is an optional spatial filter in lon,lat order (CRS84).
	BBox *[4]float64
	FeatureIDs []uint64
	// OutputFormat: only "application/gml+xml" variants are supported.
	OutputFormat string
	// ResultType is "results" (default) or "hits" (count only).
	ResultType string
	// PropertyNames restricts returned properties (projection). Empty
	// means all properties.
	PropertyNames []string
	// SortBy is a list of "property [ASC|DESC]" sort criteria.
	SortBy []SortCriterion
}

// SortCriterion is one sortBy term.
type SortCriterion struct {
	Property   string
	Descending bool
}

// ParseGetFeatureKVP parses the KVP subset for GetFeature.
func ParseGetFeatureKVP(v Version, q map[string]string) (*GetFeatureRequest, []Exception) {
	// Stored query dispatch (WFS 2.0).
	if sqID := q["storedquery_id"]; sqID != "" {
		if v == V110 {
			return nil, []Exception{{Code: ExceptionOperationNotSupported, Text: "stored queries require WFS 2.0"}}
		}
		sq, ok := GetStoredQuery(sqID)
		if !ok {
			return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "storedQueryId", Text: fmt.Sprintf("unknown stored query %q", sqID)}}
		}
		return sq.ToGetFeature(q)
	}
	// A26 (fail-closed): FILTER/FES is not implemented for KVP GetFeature.
	// Silently ignoring it would return unfiltered data. Reject explicitly.
	if f := q["filter"]; f != "" {
		return nil, []Exception{{Code: ExceptionOperationNotSupported, Locator: "filter", Text: "FILTER parameter is not supported in this profile; omit it or use featureId"}}
	}
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
	if rt := strings.ToLower(q["resulttype"]); rt != "" {
		if rt != "results" && rt != "hits" {
			return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "resultType", Text: fmt.Sprintf("unsupported resultType %q", q["resulttype"])}}
		}
		req.ResultType = rt
	} else {
		req.ResultType = "results"
	}
	if pn := q["propertyname"]; pn != "" {
		for _, p := range strings.Split(pn, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// Strip namespace prefix if present.
			if i := strings.Index(p, ":"); i >= 0 {
				p = p[i+1:]
			}
			req.PropertyNames = append(req.PropertyNames, p)
		}
	}
	if sb := q["sortby"]; sb != "" {
		for _, term := range strings.Split(sb, ",") {
			term = strings.TrimSpace(term)
			if term == "" {
				continue
			}
			crit := SortCriterion{}
			parts := strings.Fields(term)
			prop := parts[0]
			if i := strings.Index(prop, ":"); i >= 0 {
				prop = prop[i+1:]
			}
			crit.Property = prop
			if len(parts) > 1 {
				switch strings.ToUpper(parts[1]) {
				case "DESC", "D":
					crit.Descending = true
				case "ASC", "A":
					// default
				default:
					return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "sortBy", Text: fmt.Sprintf("invalid sort direction %q", parts[1])}}
				}
			}
			req.SortBy = append(req.SortBy, crit)
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
	// Validate property names and sort criteria against the schema.
	if len(req.PropertyNames) > 0 {
		for _, pn := range req.PropertyNames {
			if _, ok := schema.Property(pn); !ok && pn != schema.Geometry.Name {
				return "", []Exception{{Code: ExceptionInvalidParameterValue, Locator: "propertyName", Text: fmt.Sprintf("unknown property %q", pn)}}
			}
		}
	}
	for _, sc := range req.SortBy {
		if _, ok := schema.Property(sc.Property); !ok {
			return "", []Exception{{Code: ExceptionInvalidParameterValue, Locator: "sortBy", Text: fmt.Sprintf("unknown sort property %q", sc.Property)}}
		}
	}
	// resultType=hits: count only, no feature encoding.
	// W27: numberMatched must be the global matching count, not the page
	// count. Do not apply MaxFeatures limit here; use max int32 as the
	// effective "no limit" (provider requires Limit > 0).
	if req.ResultType == "hits" {
		hitsQ := fq
		hitsQ.Limit = 1<<31 - 1
		count := 0
		_, err = service.QueryCollection(ctx, req.TypeName, hitsQ, func(f features.Feature) error {
			count++
			return nil
		})
		if err != nil {
			return "", []Exception{{Code: ExceptionNoApplicableCode, Text: fmt.Sprintf("query failed: %v", err)}}
		}
		return featureCollectionEnvelope(req.Version, "", count), nil
	}
	// Buffer for sorting; streams directly when no sortBy.
	var buffered []gml.Feature
	encodeOne := func(f features.Feature) error {
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
		if len(f.Geometry) > 0 && string(f.Geometry) != "null" {
			g, err := parseServiceGeometry(f.Geometry)
			if err != nil {
				return err
			}
			gf.Geometry = g
		}
		// Projection: only requested properties.
		props := f.Properties
		if len(req.PropertyNames) > 0 {
			props = map[string]interface{}{}
			for _, pn := range req.PropertyNames {
				if v, ok := f.Properties[pn]; ok {
					props[pn] = v
				}
			}
		}
		for k, v := range props {
			gf.Properties[k] = fmt.Sprintf("%v", v)
		}
		// Keep sort keys alongside for post-query sorting.
		if len(req.SortBy) > 0 {
			gf.SortKeys = make([]string, len(req.SortBy))
			for i, sc := range req.SortBy {
				if v, ok := f.Properties[sc.Property]; ok {
					gf.SortKeys[i] = fmt.Sprintf("%v", v)
				}
			}
			buffered = append(buffered, gf)
		} else {
			if err := enc.EncodeFeature(gf); err != nil {
				return err
			}
		}
		return nil
	}
	count := 0
	_, err = service.QueryCollection(ctx, req.TypeName, fq, func(f features.Feature) error {
		count++
		return encodeOne(f)
	})
	if err != nil {
		return "", []Exception{{Code: ExceptionNoApplicableCode, Text: fmt.Sprintf("query failed: %v", err)}}
	}
	if len(req.SortBy) > 0 {
		sortFeatures(buffered, req.SortBy)
		for _, gf := range buffered {
			gf.SortKeys = nil
			if err := enc.EncodeFeature(gf); err != nil {
				return "", []Exception{{Code: ExceptionNoApplicableCode, Text: fmt.Sprintf("encode failed: %v", err)}}
			}
		}
	}
	return featureCollectionEnvelope(req.Version, enc.String(), count), nil
}

// sortFeatures orders buffered GML features by the sort criteria.
// Numeric strings compare numerically; otherwise lexicographically.
func sortFeatures(fs []gml.Feature, criteria []SortCriterion) {
	less := func(a, b string) bool {
		af, aerr := strconv.ParseFloat(a, 64)
		bf, berr := strconv.ParseFloat(b, 64)
		if aerr == nil && berr == nil {
			return af < bf
		}
		return a < b
	}
	sort.SliceStable(fs, func(i, j int) bool {
		for k, sc := range criteria {
			a, b := fs[i].SortKeys[k], fs[j].SortKeys[k]
			if a == b {
				continue
			}
			if sc.Descending {
				return less(b, a)
			}
			return less(a, b)
		}
		return false
	})
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
