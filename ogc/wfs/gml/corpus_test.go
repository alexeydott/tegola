package gml

import (
    "encoding/json"
    "os"
    "testing"

    "github.com/alexeydott/geom"
)

func TestCorpus(t *testing.T) {
    data, err := os.ReadFile("testdata/gml-corpus.json")
    if err != nil {
        t.Skip("corpus not available")
    }
    var cases []struct {
        ID    string          `json:"id"`
        XML   string          `json:"xml"`
        Want  json.RawMessage `json:"want"`
        Error bool            `json:"error"`
    }
    if err := json.Unmarshal(data, &cases); err != nil {
        t.Fatal(err)
    }
    for _, tc := range cases {
        g, err := ParseGeometry(tc.XML, "")
        if tc.Error {
            if err == nil {
                t.Errorf("%s: expected error, got %T", tc.ID, g)
            }
            continue
        }
        if err != nil {
            t.Errorf("%s: unexpected error: %v", tc.ID, err)
            continue
        }
        // Validate structure by checking geometry type
        switch g.(type) {
        case geom.Point, geom.LineString, geom.Polygon,
             geom.MultiPoint, geom.MultiLineString, geom.MultiPolygon:
            // OK
        default:
            t.Errorf("%s: unexpected geometry type %T", tc.ID, g)
        }
    }
}
