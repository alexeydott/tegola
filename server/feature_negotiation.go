package server

import (
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var featureQualitySyntax = regexp.MustCompile(`^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$`)

func (api *FeatureAPI) negotiate(next http.Handler, mediaType string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !featureAccepts(r.Header.Values("Accept"), mediaType) {
			api.writeError(w, r, http.StatusNotAcceptable, "NotAcceptable", "Requested representation is unavailable")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func featureAccepts(headers []string, representation string) bool {
	if len(headers) == 0 {
		return true
	}
	ranges, ok := splitFeatureMediaRanges(strings.Join(headers, ","))
	if !ok {
		return false
	}
	actual, actualParameters, err := mime.ParseMediaType(representation)
	if err != nil {
		return false
	}
	best, quality := -1, 0.0
	for _, raw := range ranges {
		media, parameters, err := mime.ParseMediaType(strings.TrimSpace(raw))
		if err != nil {
			return false
		}
		parts := strings.Split(media, "/")
		if len(parts) != 2 || (parts[0] == "*" && parts[1] != "*") {
			return false
		}
		if strings.Contains(parts[0], "*") && parts[0] != "*" || strings.Contains(parts[1], "*") && parts[1] != "*" {
			return false
		}
		q := 1.0
		rawParts, ok := splitFeatureDelimited(raw, ';')
		if !ok {
			return false
		}
		weightIndex := -1
		for i, part := range rawParts[1:] {
			name, value, present := strings.Cut(part, "=")
			if !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			// Validate the raw token: MIME normalization unquotes values and
			// can collapse repeated identical parameters.
			if weightIndex >= 0 || !present || !featureQualitySyntax.MatchString(strings.TrimSpace(value)) {
				return false
			}
			weightIndex = i + 1
			q, err = strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				return false
			}
		}
		if weightIndex >= 0 {
			// Parameters after q are Accept extensions, not representation constraints.
			_, parameters, err = mime.ParseMediaType(strings.Join(rawParts[:weightIndex], ";"))
			if err != nil {
				return false
			}
		}
		specificity := -1
		switch {
		case media == actual:
			specificity = 200
		case media == strings.SplitN(actual, "/", 2)[0]+"/*":
			specificity = 100
		case media == "*/*":
			specificity = 0
		}
		if specificity < 0 {
			continue
		}
		matches := true
		for key, value := range parameters {
			if actualParameters[key] != value {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		specificity += len(parameters)
		if specificity > best {
			best, quality = specificity, q
		} else if specificity == best && q > quality {
			quality = q
		}
	}
	return best >= 0 && quality > 0
}

// Commas inside quoted parameter values are part of a media range.
func splitFeatureMediaRanges(raw string) ([]string, bool) {
	return splitFeatureDelimited(raw, ',')
}

func splitFeatureDelimited(raw string, delimiter rune) ([]string, bool) {
	var ranges []string
	start := 0
	quoted, escaped := false, false
	appendRange := func(part string) bool {
		if strings.TrimSpace(part) == "" {
			return delimiter == ','
		}
		ranges = append(ranges, part)
		return true
	}
	for i, ch := range raw {
		if escaped {
			escaped = false
			continue
		}
		if quoted && ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			quoted = !quoted
			continue
		}
		if ch == delimiter && !quoted {
			if !appendRange(raw[start:i]) {
				return nil, false
			}
			start = i + 1
		}
	}
	if quoted || escaped || !appendRange(raw[start:]) {
		return nil, false
	}
	return ranges, true
}
