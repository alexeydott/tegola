package wfs

import (
	"fmt"
	"strings"

	"github.com/alexeydott/tegola/feature"
)

// Stored queries (WFS 2.0, WFS 1.1 does not define them).
//
// The reference profile ships one mandatory stored query:
//   urn:ogc:def:query:OGC-WFS::GetFeatureById
// Additional queries can be registered via RegisterStoredQuery.

// StoredQuery is a named, parameterized query.
type StoredQuery struct {
	// ID is the stored query identifier (URN).
	ID string
	// Title is a human-readable title.
	Title string
	// Abstract describes the query.
	Abstract string
	// Parameters lists the parameter names (for DescribeStoredQueries).
	Parameters []StoredQueryParam
	// ToGetFeature converts KVP parameters to a GetFeature request.
	ToGetFeature func(params map[string]string) (*GetFeatureRequest, []Exception)
}

// StoredQueryParam describes one stored query parameter.
type StoredQueryParam struct {
	Name     string
	Type     string
	Required bool
}

var storedQueries = map[string]*StoredQuery{}

// RegisterStoredQuery adds a stored query to the registry.
func RegisterStoredQuery(sq *StoredQuery) {
	storedQueries[sq.ID] = sq
}

// GetStoredQuery returns a registered stored query.
func GetStoredQuery(id string) (*StoredQuery, bool) {
	sq, ok := storedQueries[id]
	return sq, ok
}

// ListStoredQueries renders the ListStoredQueries response.
func ListStoredQueries(v Version) string {
	var sb strings.Builder
	sb.WriteString(`<wfs:ListStoredQueriesResponse xmlns:wfs="http://www.opengis.net/wfs/2.0">`)
	for _, sq := range storedQueries {
		sb.WriteString(fmt.Sprintf(`<wfs:StoredQuery id="%s"><wfs:Title>%s</wfs:Title></wfs:StoredQuery>`,
			xmlEscape(sq.ID), xmlEscape(sq.Title)))
	}
	sb.WriteString(`</wfs:ListStoredQueriesResponse>`)
	return sb.String()
}

// DescribeStoredQueries renders the DescribeStoredQueries response.
func DescribeStoredQueries(v Version, ids []string) (string, []Exception) {
	var sb strings.Builder
	sb.WriteString(`<wfs:DescribeStoredQueriesResponse xmlns:wfs="http://www.opengis.net/wfs/2.0" xmlns:xsd="http://www.w3.org/2001/XMLSchema">`)
	targets := ids
	if len(targets) == 0 {
		for id := range storedQueries {
			targets = append(targets, id)
		}
	}
	for _, id := range targets {
		sq, ok := storedQueries[id]
		if !ok {
			return "", []Exception{{Code: ExceptionInvalidParameterValue, Locator: "storedQueryId", Text: fmt.Sprintf("unknown stored query %q", id)}}
		}
		sb.WriteString(fmt.Sprintf(`<wfs:StoredQueryDescription id="%s"><wfs:Title>%s</wfs:Title><wfs:Abstract>%s</wfs:Abstract>`,
			xmlEscape(sq.ID), xmlEscape(sq.Title), xmlEscape(sq.Abstract)))
		for _, p := range sq.Parameters {
			sb.WriteString(fmt.Sprintf(`<wfs:Parameter name="%s" type="xsd:%s"/>`, xmlEscape(p.Name), xmlEscape(p.Type)))
		}
		sb.WriteString(`</wfs:StoredQueryDescription>`)
	}
	sb.WriteString(`</wfs:DescribeStoredQueriesResponse>`)
	return sb.String(), nil
}

func init() {
	RegisterStoredQuery(&StoredQuery{
		ID:       "urn:ogc:def:query:OGC-WFS::GetFeatureById",
		Title:    "Get feature by identifier",
		Abstract: "Returns the feature with the given ID from the given type.",
		Parameters: []StoredQueryParam{
			{Name: "typeName", Type: "string", Required: true},
			{Name: "id", Type: "string", Required: true},
		},
		ToGetFeature: func(params map[string]string) (*GetFeatureRequest, []Exception) {
			typeName := params["typename"]
			if typeName == "" {
				return nil, []Exception{{Code: ExceptionMissingParameterValue, Locator: "typeName", Text: "typeName is required"}}
			}
			id := params["id"]
			if id == "" {
				return nil, []Exception{{Code: ExceptionMissingParameterValue, Locator: "id", Text: "id is required"}}
			}
			// The id may be a full WFS FID or a bare numeric ID.
			var fid uint64
			if _, parsed, err := parseFeatureID(id); err == nil {
				fid = parsed
			} else {
				// Try bare integer.
				var n uint64
				if _, err := fmt.Sscanf(id, "%d", &n); err != nil {
					return nil, []Exception{{Code: ExceptionInvalidParameterValue, Locator: "id", Text: fmt.Sprintf("invalid id %q", id)}}
				}
				fid = n
			}
			return &GetFeatureRequest{
				TypeName:   strings.TrimSpace(typeName),
				FeatureIDs: []uint64{fid},
				MaxFeatures: 1,
				ResultType: "results",
			}, nil
		},
	})
}

// parseFeatureID extracts the numeric ID from a WFS FID.
func parseFeatureID(fid string) (string, uint64, error) {
	return feature.DecodeWFSFID(fid)
}
