package hana

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"sync/atomic"
	"testing"

	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider/test/mosfixture"
)

// contractStubDriver emulates probe query results: every query returns the
// same column names and rows, so probeMOSCustomSQLContract can be exercised
// against real sql.Rows without a live HANA server (audit A01).
type contractStubDriver struct {
	columns []string
	rows    [][]driver.Value
}

func (d *contractStubDriver) Open(string) (driver.Conn, error) {
	return &contractStubConn{columns: d.columns, rows: d.rows}, nil
}

type contractStubConn struct {
	columns []string
	rows    [][]driver.Value
}

func (c *contractStubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *contractStubConn) Close() error                        { return nil }
func (c *contractStubConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (c *contractStubConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &contractStubRows{columns: c.columns, rows: c.rows}, nil
}

type contractStubRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (r *contractStubRows) Columns() []string { return r.columns }
func (r *contractStubRows) Close() error      { return nil }
func (r *contractStubRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

var contractDriverSeq uint64

func openContractStub(t *testing.T, columns []string, rows [][]driver.Value) *sql.DB {
	t.Helper()
	driverName := "tegola_hana_contract_test_" + strconv.FormatUint(atomic.AddUint64(&contractDriverSeq, 1), 10)
	sql.Register(driverName, &contractStubDriver{columns: columns, rows: rows})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// hanaProbeFixture converts shared fixture rows to driver values.
func hanaProbeFixture(rows [][]interface{}) [][]driver.Value {
	out := make([][]driver.Value, len(rows))
	for i, row := range rows {
		vals := make([]driver.Value, len(row))
		for j, v := range row {
			switch t := v.(type) {
			case int:
				vals[j] = int64(t)
			default:
				vals[j] = v
			}
		}
		out[i] = vals
	}
	return out
}

// TestProbeMOSCustomSQLContract runs the shared bounds-contract fixture
// through the real HANA probe (standard row scan via setupRowValues + raw
// value unwrap — audit A01) and asserts the identical SQLGeometryContract
// the other providers report for the same fixture.
func TestProbeMOSCustomSQLContract(t *testing.T) {
	probe := func(t *testing.T, columns []string, rows [][]interface{}, format string) ([]string, codec.SQLGeometryContract, *Layer) {
		t.Helper()
		db := openContractStub(t, columns, hanaProbeFixture(rows))
		p := Provider{pool: &connectionPoolCollector{pool: db}}
		layer := &Layer{
			name:          "probe_layer",
			geomField:     "geom",
			bboxFields:    codec.DefaultBBoxFields(),
			mosConfig:     mosfixture.Config(),
			geometryFormat: format,
		}
		cols, contract, err := p.probeMOSCustomSQLContract(layer, "SELECT * FROM probe_table")
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		return cols, contract, layer
	}

	t.Run("three real MOS rows report the identical shared contract", func(t *testing.T) {
		cols, contract, _ := probe(t, mosfixture.Columns(), mosfixture.ValidRows(), codec.FormatMOS)
		for i, c := range mosfixture.Columns() {
			if cols[i] != c {
				t.Fatalf("column %d = %q, expected %q", i, cols[i], c)
			}
		}
		if contract != mosfixture.ExpectedContract() {
			t.Fatalf("contract %+v, expected identical fixture contract %+v", contract, mosfixture.ExpectedContract())
		}
	})

	t.Run("one malformed row plus three valid rows counts three", func(t *testing.T) {
		_, contract, _ := probe(t, mosfixture.Columns(), mosfixture.RowsWithMalformed(), codec.FormatMOS)
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (malformed rows skipped, never counted)", contract.ValidMOSRows)
		}
	})

	t.Run("system info row is skipped and never applied", func(t *testing.T) {
		_, contract, layer := probe(t, mosfixture.Columns(), mosfixture.RowsWithSystemInfo(), codec.FormatMOS)
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (system info row skipped)", contract.ValidMOSRows)
		}
		if layer.mosConfig != mosfixture.Config() {
			t.Fatalf("mosConfig mutated by probe: %+v", layer.mosConfig)
		}
	})

	t.Run("lower-case bounds columns persist actual names", func(t *testing.T) {
		_, contract, _ := probe(t, mosfixture.LowerCaseColumns(), mosfixture.ValidRows(), codec.FormatMOS)
		if contract != mosfixture.ExpectedLowerCaseContract() {
			t.Fatalf("contract %+v, expected %+v (actual result-column names)", contract, mosfixture.ExpectedLowerCaseContract())
		}
	})

	// audit part11 7.1.3 regression: native rows never count as MOS rows.
	t.Run("native geometry rows never count as MOS rows", func(t *testing.T) {
		_, contract, _ := probe(t, mosfixture.Columns(), mosfixture.NativeRows(), "")
		if contract.ValidMOSRows != 0 {
			t.Fatalf("ValidMOSRows = %d, expected 0 (native rows must not count as MOS)", contract.ValidMOSRows)
		}
	})

	// audit A04: the inference (auto) probe decode closure must fall back
	// to codec.DecodeMOS so MOS rows are recognized.
	t.Run("auto format probe falls back to MOS decode", func(t *testing.T) {
		_, contract, _ := probe(t, mosfixture.Columns(), mosfixture.ValidRows(), "")
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (auto decode must fall back to DecodeMOS)", contract.ValidMOSRows)
		}
	})
}
