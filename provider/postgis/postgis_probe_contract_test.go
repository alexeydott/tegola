package postgis

import (
	"errors"
	"testing"

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
