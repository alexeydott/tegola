package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/tegola/internal/log"
	"github.com/alexeydott/tegola/ogc/cql2"
	"github.com/alexeydott/tegola/ogc/features"
	"github.com/alexeydott/tegola/provider"
	"github.com/dimfeld/httptreemux"
)

func (api *FeatureAPI) serveItems(w http.ResponseWriter, r *http.Request) {
	query, parameters, err := api.parseItemsQuery(r.URL.RawQuery)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid query parameter")
		return
	}
	id := httptreemux.ContextParams(r.Context())["collection"]
	page, err := api.service.QueryCollectionPage(r.Context(), id, query)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	if page.HasMore && page.NumberReturned == 0 {
		api.writeError(w, r, http.StatusInternalServerError, "InternalError", "Invalid provider paging result")
		return
	}
	parameters.Set("limit", strconv.FormatUint(uint64(query.Limit), 10))
	parameters.Set("offset", strconv.FormatUint(query.Offset, 10))
	suffix := "/collections/" + id + "/items"
	links := []featureLink{api.pageLink(r, suffix, "self", parameters, query.Offset)}
	if page.HasMore {
		links = append(links, api.pageLink(r, suffix, "next", parameters, query.Offset+page.NumberReturned))
	}
	if query.Offset != 0 {
		previous := uint64(0)
		if query.Offset > uint64(query.Limit) {
			previous = query.Offset - uint64(query.Limit)
		}
		links = append(links, api.pageLink(r, suffix, "prev", parameters, previous))
	}
	response := struct {
		features.FeatureCollection
		Links []featureLink `json:"links"`
	}{FeatureCollection: page, Links: links}
	api.writeJSON(w, r, http.StatusOK, "application/geo+json", response)
}

func (api *FeatureAPI) serveItem(w http.ResponseWriter, r *http.Request) {
	if !api.discoveryQueryValid(w, r) {
		return
	}
	parameters := httptreemux.ContextParams(r.Context())
	id := parameters["collection"]
	rawID := parameters["feature"]
	if !decimalDigits(rawID) {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid feature ID")
		return
	}
	featureID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid feature ID")
		return
	}
	feature, err := api.service.QueryFeature(r.Context(), id, featureID)
	if err != nil {
		api.writeQueryError(w, r, err)
		return
	}
	response := struct {
		features.Feature
		Links []featureLink `json:"links"`
	}{Feature: feature, Links: []featureLink{api.link(r, "/collections/"+id+"/items/"+strconv.FormatUint(featureID, 10), "self", "application/geo+json"), api.link(r, "/collections/"+id, "collection", "application/json")}}
	api.writeJSON(w, r, http.StatusOK, "application/geo+json", response)
}

func (api *FeatureAPI) pageLink(r *http.Request, suffix, relation string, parameters url.Values, offset uint64) featureLink {
	link := api.link(r, suffix, relation, "application/geo+json")
	// Inputs have been parsed and validated; URL encoding preserves datetime/bbox
	// values and prevents arbitrary query text from becoming link syntax.
	values := make(url.Values, len(parameters))
	for key, value := range parameters {
		values[key] = append([]string{}, value...)
	}
	values.Set("offset", strconv.FormatUint(offset, 10))
	link.Href += "?" + values.Encode()
	return link
}

func (api *FeatureAPI) writeQueryError(w http.ResponseWriter, r *http.Request, err error) {
	var collection features.CollectionNotFoundError
	var feature features.FeatureNotFoundError
	var invalid provider.InvalidFeatureQueryError
	var data provider.FeatureDataError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		api.writeError(w, r, http.StatusRequestTimeout, "RequestTimeout", "Request did not complete")
	case errors.As(err, &data):
		log.Error("feature source data failed", "error", err)
		api.writeError(w, r, http.StatusInternalServerError, "InternalError", "Feature query failed")
	case errors.As(err, &collection), errors.As(err, &feature):
		api.writeError(w, r, http.StatusNotFound, "NotFound", "Resource not found")
	case errors.As(err, &invalid):
		api.writeError(w, r, http.StatusBadRequest, "InvalidParameter", "Invalid query parameter")
	case errors.Is(err, provider.ErrUnsupported):
		api.writeError(w, r, http.StatusNotImplemented, "UnsupportedQuery", "Query operation is unsupported")
	default:
		log.Error("feature query failed", "error", err)
		api.writeError(w, r, http.StatusInternalServerError, "InternalError", "Feature query failed")
	}
}

func (api *FeatureAPI) parseItemsQuery(raw string) (provider.FeatureQuery, url.Values, error) {
	parameters, err := url.ParseQuery(raw)
	if err != nil {
		return provider.FeatureQuery{}, nil, fmt.Errorf("parse query: %w", err)
	}
	for key, values := range parameters {
		switch key {
		case "limit", "offset", "bbox", "datetime", "filter", "filter-lang":
		default:
			return provider.FeatureQuery{}, nil, fmt.Errorf("unknown query parameter")
		}
		if len(values) != 1 {
			return provider.FeatureQuery{}, nil, fmt.Errorf("repeated query parameter")
		}
	}
	query := provider.FeatureQuery{Limit: api.cfg.DefaultLimit}
	if value, ok := parameters["limit"]; ok {
		query.Limit, err = parsePageLimit(value[0], api.cfg.MaxLimit)
		if err != nil {
			return provider.FeatureQuery{}, nil, err
		}
	}
	if value, ok := parameters["offset"]; ok {
		if !decimalDigits(value[0]) {
			return provider.FeatureQuery{}, nil, fmt.Errorf("invalid offset")
		}
		query.Offset, err = strconv.ParseUint(value[0], 10, 64)
		if err != nil {
			return provider.FeatureQuery{}, nil, fmt.Errorf("invalid offset: %w", err)
		}
	}
	if value, ok := parameters["bbox"]; ok {
		if err := parseFeatureBounds(value[0], &query); err != nil {
			return provider.FeatureQuery{}, nil, err
		}
	}
	if value, ok := parameters["datetime"]; ok {
		query.Temporal, err = parseFeatureDatetime(value[0])
		if err != nil {
			return provider.FeatureQuery{}, nil, err
		}
	}
	if language, present := parameters["filter-lang"]; present {
		if _, filtered := parameters["filter"]; !filtered || language[0] != "cql2-text" {
			return provider.FeatureQuery{}, nil, fmt.Errorf("invalid filter language")
		}
	}
	if value, present := parameters["filter"]; present {
		if strings.TrimSpace(value[0]) == "" {
			return provider.FeatureQuery{}, nil, fmt.Errorf("blank filter")
		}
		filter, err := cql2.Parse(value[0])
		if err != nil {
			return provider.FeatureQuery{}, nil, err
		}
		query.Filter = &filter
	}
	if err := query.Validate(); err != nil {
		return provider.FeatureQuery{}, nil, err
	}
	return query, parameters, nil
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func parsePageLimit(raw string, maximum uint) (uint, error) {
	if !decimalDigits(raw) {
		return 0, fmt.Errorf("invalid limit")
	}
	value := strings.TrimLeft(raw, "0")
	if value == "" {
		return 0, fmt.Errorf("limit must be positive")
	}
	bound := strconv.FormatUint(uint64(maximum), 10)
	if len(value) > len(bound) || (len(value) == len(bound) && value > bound) {
		return maximum, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid limit: %w", err)
	}
	return uint(parsed), nil
}

func parseFeatureBounds(raw string, query *provider.FeatureQuery) error {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 && len(parts) != 6 {
		return fmt.Errorf("bbox requires four or six coordinates")
	}
	values := make([]float64, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("bbox coordinates must be finite")
		}
		values[i] = value
	}
	maximum := len(parts) / 2
	west, south, east, north := values[0], values[1], values[maximum], values[maximum+1]
	if west < -180 || west > 180 || east < -180 || east > 180 || south < -90 || south > 90 || north < -90 || north > 90 || south > north {
		return fmt.Errorf("bbox outside geographic domain")
	}
	query.BoundsSRID = 4326
	if len(parts) == 4 {
		if west <= east {
			query.Bounds = []geom.Extent{{west, south, east, north}}
		} else {
			query.Bounds = []geom.Extent{{west, south, 180, north}, {-180, south, east, north}}
		}
	} else {
		bottom, top := values[2], values[5]
		if bottom > top {
			return fmt.Errorf("bbox minimum height exceeds maximum")
		}
		query.BoundsVerticalCRS = provider.CRS84h
		if west <= east {
			query.Bounds3D = []provider.Extent3D{{west, south, bottom, east, north, top}}
		} else {
			query.Bounds3D = []provider.Extent3D{{west, south, bottom, 180, north, top}, {-180, south, bottom, east, north, top}}
		}
	}
	return nil
}

func parseFeatureDatetime(raw string) (*provider.TemporalConstraint, error) {
	parts := strings.Split(raw, "/")
	if len(parts) > 2 {
		return nil, fmt.Errorf("invalid datetime interval")
	}
	constraint := &provider.TemporalConstraint{}
	if len(parts) == 1 {
		timestamp, tail, leap, err := parseFeatureTimestamp(parts[0])
		if err != nil {
			return nil, err
		}
		end := *timestamp
		constraint.Start, constraint.End = timestamp, &end
		constraint.StartSubNanosecond, constraint.EndSubNanosecond = tail, tail
		constraint.StartLeapSecond, constraint.EndLeapSecond = leap, leap
	} else {
		for i, part := range parts {
			if part == "" || part == ".." {
				continue
			}
			timestamp, tail, leap, err := parseFeatureTimestamp(part)
			if err != nil {
				return nil, err
			}
			if i == 0 {
				constraint.Start = timestamp
				constraint.StartSubNanosecond = tail
				constraint.StartLeapSecond = leap
			} else {
				constraint.End = timestamp
				constraint.EndSubNanosecond = tail
				constraint.EndLeapSecond = leap
			}
		}
	}
	if err := constraint.Validate(); err != nil {
		return nil, err
	}
	return constraint, nil
}

var featureTimestampSyntax = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?([Zz]|[+-][0-9]{2}:[0-9]{2})$`)

func parseFeatureTimestamp(raw string) (*time.Time, string, bool, error) {
	if !featureTimestampSyntax.MatchString(raw) {
		return nil, "", false, fmt.Errorf("invalid datetime syntax")
	}
	if raw[len(raw)-1] != 'Z' && raw[len(raw)-1] != 'z' {
		zone := raw[len(raw)-6:]
		if zone[1:3] > "23" || zone[4:6] > "59" {
			return nil, "", false, fmt.Errorf("invalid datetime offset")
		}
	}
	normalized := raw[:10] + "T" + raw[11:]
	if strings.HasSuffix(normalized, "z") {
		normalized = normalized[:len(normalized)-1] + "Z"
	}
	leap := normalized[17:19] == "60"
	if leap {
		normalized = normalized[:17] + "59" + normalized[19:]
	}
	tail := ""
	if normalized[19] == '.' {
		end := 20
		for end < len(normalized) && normalized[end] >= '0' && normalized[end] <= '9' {
			end++
		}
		if end-20 > 9 {
			tail = strings.TrimRight(normalized[29:end], "0")
			normalized = normalized[:29] + normalized[end:]
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, normalized)
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid datetime: %w", err)
	}
	if leap && !provider.IsPositiveLeapSecondPredecessor(parsed) {
		return nil, "", false, fmt.Errorf("datetime is not an announced leap second")
	}
	return &parsed, tail, leap, nil
}
