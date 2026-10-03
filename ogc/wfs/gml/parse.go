package gml

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/alexeydott/geom"
)

// ParseGeometry parses a GML geometry element (Point, LineString,
// Polygon, and their Multi* forms) from raw XML. Coordinates are
// expected in the document's axis order; the caller swaps axes when the
// version/SRS requires it.
func ParseGeometry(raw string, swapXY bool) (geom.Geometry, error) {
	dec := xml.NewDecoder(strings.NewReader(raw))
	var geomType string
	var coords []string
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				geomType = t.Name.Local
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" {
				coords = append(coords, text)
			}
		}
	}
	nums, err := parseNumbers(strings.Join(coords, " "))
	if err != nil {
		return nil, err
	}
	pts := toPoints(nums, swapXY)
	switch geomType {
	case "Point":
		if len(pts) != 1 {
			return nil, fmt.Errorf("Point needs 1 coordinate")
		}
		return geom.Point(pts[0]), nil
	case "LineString":
		return geom.LineString(pts), nil
	case "Polygon":
		return geom.Polygon{pts}, nil
	case "MultiPoint":
		return geom.MultiPoint(pts), nil
	case "MultiLineString", "MultiCurve":
		// Without sub-geometry boundaries we treat the flat list as one part.
		return geom.MultiLineString{pts}, nil
	case "MultiPolygon", "MultiSurface":
		return geom.MultiPolygon{geom.Polygon{pts}}, nil
	default:
		return nil, fmt.Errorf("unsupported GML geometry %q", geomType)
	}
}

func parseNumbers(s string) ([]float64, error) {
	var out []float64
	for _, f := range strings.Fields(strings.ReplaceAll(strings.ReplaceAll(s, ",", " "), "\n", " ")) {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid coordinate %q", f)
		}
		out = append(out, v)
	}
	return out, nil
}

func toPoints(nums []float64, swapXY bool) [][2]float64 {
	var out [][2]float64
	for i := 0; i+1 < len(nums); i += 2 {
		x, y := nums[i], nums[i+1]
		if swapXY {
			x, y = y, x
		}
		out = append(out, [2]float64{x, y})
	}
	return out
}
