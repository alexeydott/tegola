package postgis

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/provider"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider/test/mosfixture"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// probeFakeRows is an in-memory pgx.Rows over REAL row values for the
// bounds-contract probe (audit A01).
type probeFakeRows struct {
	columns []string
	rows    [][]any
	next    int
}

func (r *probeFakeRows) Close() {}
func (r *probeFakeRows) Err() error {
	return nil
}
func (r *probeFakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}
func (r *probeFakeRows) FieldDescriptions() []pgconn.FieldDescription {
	fds := make([]pgconn.FieldDescription, len(r.columns))
	for i, c := range r.columns {
		fds[i] = pgconn.FieldDescription{Name: c}
	}
	return fds
}
func (r *probeFakeRows) Next() bool {
	if r.next >= len(r.rows) {
		return false
	}
	r.next++
	return true
}
func (r *probeFakeRows) Scan(dest ...any) error {
	return errors.New("probe consumes rows.Values, not Scan")
}
func (r *probeFakeRows) Values() ([]any, error) {
	return r.rows[r.next-1], nil
}
func (r *probeFakeRows) RawValues() [][]byte { return nil }
func (r *probeFakeRows) Conn() *pgx.Conn      { return nil }

// TestProbeSQLContractRows runs the shared bounds-contract fixture through
// the PostGIS probe row inspection and asserts the identical
// SQLGeometryContract the other providers report for the same fixture.
func TestProbeSQLContractRows(t *testing.T) {
	probe := func(t *testing.T, columns []string, rows [][]any, format string) (codec.SQLGeometryContract, *Layer) {
		t.Helper()
		layer := &Layer{
			name:           "probe_layer",
			geomField:      "geom",
			bboxFields:     codec.DefaultBBoxFields(),
			mosConfig:      mosfixture.Config(),
			geometryFormat: format,
		}
		_, contract, err := probeSQLContractRows(layer, &probeFakeRows{columns: columns, rows: rows})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		return contract, layer
	}

	t.Run("three real MOS rows report the identical shared contract", func(t *testing.T) {
		contract, _ := probe(t, mosfixture.Columns(), mosfixture.ValidRows(), codec.FormatMOS)
		if contract != mosfixture.ExpectedContract() {
			t.Fatalf("contract %+v, expected identical fixture contract %+v", contract, mosfixture.ExpectedContract())
		}
	})

	t.Run("one malformed row plus three valid rows counts three", func(t *testing.T) {
		contract, _ := probe(t, mosfixture.Columns(), mosfixture.RowsWithMalformed(), codec.FormatMOS)
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (malformed rows skipped, never counted)", contract.ValidMOSRows)
		}
	})

	t.Run("system info row is skipped and never applied", func(t *testing.T) {
		contract, layer := probe(t, mosfixture.Columns(), mosfixture.RowsWithSystemInfo(), codec.FormatMOS)
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (system info row skipped)", contract.ValidMOSRows)
		}
		if layer.mosConfig != mosfixture.Config() {
			t.Fatalf("mosConfig mutated by probe: %+v", layer.mosConfig)
		}
	})

	t.Run("lower-case bounds columns persist actual names", func(t *testing.T) {
		contract, _ := probe(t, mosfixture.LowerCaseColumns(), mosfixture.ValidRows(), codec.FormatMOS)
		if contract != mosfixture.ExpectedLowerCaseContract() {
			t.Fatalf("contract %+v, expected %+v (actual result-column names)", contract, mosfixture.ExpectedLowerCaseContract())
		}
	})

	// audit part11 7.1.3 regression: native rows never count as MOS rows.
	t.Run("native geometry rows never count as MOS rows", func(t *testing.T) {
		contract, _ := probe(t, mosfixture.Columns(), mosfixture.NativeRows(), "")
		if contract.ValidMOSRows != 0 {
			t.Fatalf("ValidMOSRows = %d, expected 0 (native rows must not count as MOS)", contract.ValidMOSRows)
		}
	})

	// audit A04: the inference (auto) probe decode closure must fall back
	// to codec.DecodeMOS so MOS rows are recognized.
	t.Run("auto format probe falls back to MOS decode", func(t *testing.T) {
		contract, _ := probe(t, mosfixture.Columns(), mosfixture.ValidRows(), "")
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (auto decode must fall back to DecodeMOS)", contract.ValidMOSRows)
		}
	})
}

// adversarialProbeSQL pins audit R1: TWO zoom tokens plus a bbox token
// inside a SQL function call — the historical preparation replaced only the
// first !ZOOM! occurrence and neutralized !BBOX! to TRUE, which breaks
// ST_Intersects(geom, !BBOX!) at the parenthesis level.
const adversarialProbeSQL = `SELECT * FROM t WHERE min_zoom <= !ZOOM! AND max_zoom >= !ZOOM! AND geom && !BBOX! AND ST_Intersects(geom, !BBOX!) AND cx = !X! AND cy = !Y!`

// TestProbeSQLPreparationR1 asserts both PostGIS inspection probes follow
// the shared codec.PrepareProbeSQL contract (audit R1): every zoom/position
// token occurrence is neutralized, !BBOX! becomes "1=1" (never "TRUE", so
// it stays syntactically valid inside SQL function arguments) and the
// query is wrapped in the shared InspectionSampleLimit sample window
// (docs/provider-contract.md).
func TestProbeSQLPreparationR1(t *testing.T) {
	newLayer := func() *Layer {
		return &Layer{
			name:       "probe_layer",
			sql:        adversarialProbeSQL,
			idField:    "gid",
			geomField:  "geom",
			bboxFields: codec.DefaultBBoxFields(),
		}
	}

	check := func(t *testing.T, probeSQL string) {
		t.Helper()
		for _, tok := range []string{"!ZOOM!", "!BBOX!", "!X!", "!Y!"} {
			if strings.Contains(probeSQL, tok) {
				t.Errorf("token %s left in probe SQL: %s", tok, probeSQL)
			}
		}
		if !strings.Contains(probeSQL, "1=1") {
			t.Errorf("bbox token must neutralize to 1=1: %s", probeSQL)
		}
		if strings.Contains(probeSQL, "TRUE") {
			t.Errorf("bbox token must never neutralize to TRUE (breaks function arguments): %s", probeSQL)
		}
		if !strings.Contains(probeSQL, "ST_Intersects(geom, 1=1)") {
			t.Errorf("ST_Intersects(geom, !BBOX!) must probe as ST_Intersects(geom, 1=1): %s", probeSQL)
		}
		if !strings.Contains(probeSQL, fmt.Sprintf("LIMIT %v", codec.InspectionSampleLimit)) {
			t.Errorf("probe SQL must use the shared sample window LIMIT %v: %s", codec.InspectionSampleLimit, probeSQL)
		}
	}

	t.Run("MOS probe neutralizes every token occurrence", func(t *testing.T) {
		check(t, mosProbeSQL(newLayer()))
	})

	t.Run("native probe neutralizes every token occurrence", func(t *testing.T) {
		probeSQL, args := geomTypeProbeSQL(newLayer(), provider.Params{})
		if len(args) != 0 {
			t.Fatalf("args = %v, expected none without custom parameters", args)
		}
		check(t, probeSQL)
	})

	// The coordinator-verified live path: custom parameters substitute
	// with $N placeholders and their values stay bound as query arguments.
	t.Run("custom parameters keep live arguments", func(t *testing.T) {
		l := newLayer()
		l.sql = adversarialProbeSQL + " AND region = !REGION!"
		params := provider.Params{"!REGION!": {Token: "!REGION!", SQL: "?", Value: "west"}}
		probeSQL, args := geomTypeProbeSQL(l, params)
		if strings.Contains(probeSQL, "!REGION!") {
			t.Fatalf("parameter token must be substituted: %s", probeSQL)
		}
		if !strings.Contains(probeSQL, "$1") {
			t.Fatalf("parameter placeholder must be generated: %s", probeSQL)
		}
		if len(args) != 1 || args[0] != "west" {
			t.Fatalf("args = %v, expected live [west]", args)
		}
	})

	t.Run("MOS probe flow succeeds over fixture rows", func(t *testing.T) {
		l := newLayer()
		l.geometryFormat = codec.FormatMOS
		l.mosConfig = mosfixture.Config()
		probeSQL := mosProbeSQL(l)
		if err := inspectMOSGeomTypeRows(l, probeSQL, &probeFakeRows{columns: mosfixture.Columns(), rows: mosfixture.ValidRows()}); err != nil {
			t.Fatalf("probe over fixture rows: %v", err)
		}
		// DecodeMOS promotes the fixture WKB point to its multi-geometry
		// representation (MOS geometries decode as multi types).
		if _, ok := l.geomType.(geom.MultiPoint); !ok {
			t.Fatalf("geomType = %T, expected geom.MultiPoint", l.geomType)
		}
	})

	t.Run("native probe flow sniffs geometry type from sample rows", func(t *testing.T) {
		l := newLayer()
		l.sql = "SELECT ST_AsBinary(geom) AS geom, gid FROM t WHERE min_zoom <= !ZOOM! AND max_zoom >= !ZOOM!"
		probeSQL, _ := geomTypeProbeSQL(l, provider.Params{})
		if strings.Contains(probeSQL, "!ZOOM!") {
			t.Fatalf("both !ZOOM! occurrences must be replaced: %s", probeSQL)
		}
		if err := inspectGeomTypeRows(l, probeSQL, &probeFakeRows{columns: []string{"st_geometrytype"}, rows: [][]any{{"ST_Point"}}}); err != nil {
			t.Fatalf("probe over sample rows: %v", err)
		}
		if _, ok := l.geomType.(geom.Point); !ok {
			t.Fatalf("geomType = %T, expected geom.Point", l.geomType)
		}
	})
}
