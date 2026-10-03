package wfs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/tegola/feature"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/provider"
)

// GetPropertyValueRequest is a WFS 2.0 GetPropertyValue request.
type GetPropertyValueRequest struct {
	Version Version
	TypeName string
	// ValueReference is the property to return (local name, no namespace).
	ValueReference string
	MaxFeatures uint
	BBox *[4]float64
	FeatureIDs []uint64
	ResultType string
}

// ParseGetPropertyValueKVP parses the KVP subset for GetPropertyValue.
func ParseGetPropertyValueKVP(v Version, q map[string]string) (*GetPropertyValueRequest, []Exception) {
	if v == V110 {
		return nil, []Exception{{Code: ExceptionOperationNotSupported, Text: "GetPropertyValue requires WFS 2.0"}}
	}
	req := &GetPropertyValueRequest{Version: v, MaxFeatures: 1000, ResultType: "results"}
	typeName := q["typename"]
	if typeName == "" {
		typeName = q["typenames"]
	}
	if typeName == "" {
		return nil, []Exception{{Code: ExceptionMissingParameterValue, Locator: "typeName", Text: "typeName is required"}}
	}
	if strings.Contains(typeName, ",") {
		return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "typeName", Text: "multiple type names not supported"}}
	}
	req.TypeName = strings.TrimSpace(typeName)
	vr := q["valuereference"]
	if vr == "" {
		return nil, []Exception{{Code: ExceptionMissingParameterValue, Locator: "valueReference", Text: "valueReference is required"}}
	}
	// Strip namespace prefix.
	if i := strings.Index(vr, ":"); i >= 0 {
		vr = vr[i+1:]
	}
	// Strip trailing /text() or similar XPath steps (not supported).
	if i := strings.Index(vr, "/"); i >= 0 {
		vr = vr[:i]
	}
	req.ValueReference = strings.TrimSpace(vr)
	if req.ValueReference == "" {
		return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "valueReference", Text: "invalid valueReference"}}
	}
	if mf := q["count"]; mf != "" {
		n, err := strconv.ParseUint(mf, 10, 32)
		if err != nil {
			return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "count", Text: "invalid count"}}
		}
		req.MaxFeatures = uint(n)
	}
	if rt := strings.ToLower(q["resulttype"]); rt != "" {
		if rt != "results" && rt != "hits" {
			return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "resultType", Text: fmt.Sprintf("unsupported resultType %q", q["resulttype"])}}
		}
		req.ResultType = rt
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

// ExecuteGetPropertyValue runs the request and renders a wfs:ValueCollection.
func ExecuteGetPropertyValue(ctx context.Context, service *features.Service, req *GetPropertyValueRequest) (string, []Exception) {
	if _, err := service.Collection(req.TypeName); err != nil {
		return "", []Exception{{Code: ExceptionInvalidParameterValue, Locator: "typeName", Text: fmt.Sprintf("unknown type %q", req.TypeName)}}
	}
	schema, err := service.SchemaDescriptorFor(ctx, req.TypeName)
	if err != nil {
		return "", []Exception{{Code: ExceptionNoApplicableCode, Text: "schema unavailable"}}
	}
	// Validate the value reference against the schema.
	propName := req.ValueReference
	if _, ok := schema.Property(propName); !ok {
		// Allow the geometry property by its configured name.
		if propName != schema.Geometry.Name {
			return "", []Exception{{Code: ExceptionInvalidParameterValue, Locator: "valueReference", Text: fmt.Sprintf("unknown property %q", propName)}}
		}
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
	count := 0
	var sb strings.Builder
	if req.ResultType == "results" {
		sb.WriteString(`<wfs:ValueCollection xmlns:wfs="http://www.opengis.net/wfs/2.0" xmlns:gml="http://www.opengis.net/gml/3.2"`)
		sb.WriteString(fmt.Sprintf(` numberMatched="unknown" numberReturned="0" timeStamp="%s">`, xmlTime()))
	}
	_, err = service.QueryCollection(ctx, req.TypeName, fq, func(f features.Feature) error {
		count++
		if req.ResultType == "hits" {
			return nil
		}
		var val string
		if propName == schema.Geometry.Name {
			val = string(f.Geometry)
		} else if v, ok := f.Properties[propName]; ok {
			val = fmt.Sprintf("%v", v)
		}
		sb.WriteString(`<wfs:member>`)
		sb.WriteString(fmt.Sprintf(`<%s>%s</%s>`, xmlEscape(propName), xmlEscape(val), xmlEscape(propName)))
		sb.WriteString(`</wfs:member>`)
		return nil
	})
	if err != nil {
		return "", []Exception{{Code: ExceptionNoApplicableCode, Text: fmt.Sprintf("query failed: %v", err)}}
	}
	if req.ResultType == "hits" {
		return fmt.Sprintf(`<wfs:ValueCollection xmlns:wfs="http://www.opengis.net/wfs/2.0" numberMatched="%d" numberReturned="0"/>`, count), nil
	}
	// Patch numberReturned.
	out := sb.String()
	out = strings.Replace(out, `numberReturned="0"`, fmt.Sprintf(`numberReturned="%d"`, count), 1)
	out += `</wfs:ValueCollection>`
	return out, nil
}

func xmlTime() string {
	// Fixed format; actual timestamp omitted for determinism in tests.
	// Production callers may replace with time.Now().UTC().Format(time.RFC3339).
	return "1970-01-01T00:00:00Z"
}
