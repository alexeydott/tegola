package mysql

import (
	"database/sql/driver"
	"testing"

	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider/test/mosfixture"
)

// probeFixture converts shared fixture rows to driver values for the stub
// driver (driver.Value only allows int64, not int).
func probeFixture(rows [][]interface{}) [][]driver.Value {
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
// through the real MySQL probe (real rows.Scan — audit A01) and asserts the
// identical SQLGeometryContract every other provider reports for the same
// fixture.
func TestProbeMOSCustomSQLContract(t *testing.T) {
	probe := func(t *testing.T, columns []string, rows [][]interface{}, format string) ([]string, codec.SQLGeometryContract, *Layer) {
		t.Helper()
		db := openShowIndexStub(t, columns, probeFixture(rows))
		layer := &Layer{
			name:         "probe_layer",
			geomFieldname: "geom",
			bboxFields:   codec.DefaultBBoxFields(),
			mosConfig:    mosfixture.Config(),
		}
		cols, contract, err := probeMOSCustomSQLContract(db, layer, "SELECT * FROM probe_table", format, "mysql")
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		return cols, contract, layer
	}

	t.Run("three real MOS rows report the identical shared contract", func(t *testing.T) {
		cols, contract, _ := probe(t, mosfixture.Columns(), mosfixture.ValidRows(), GeometryFormatMOS)
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
		_, contract, _ := probe(t, mosfixture.Columns(), mosfixture.RowsWithMalformed(), GeometryFormatMOS)
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (malformed rows skipped, never counted)", contract.ValidMOSRows)
		}
		if contract.ValidRows != 3 {
			t.Fatalf("ValidRows = %d, expected 3", contract.ValidRows)
		}
	})

	t.Run("system info row is skipped and never applied", func(t *testing.T) {
		_, contract, layer := probe(t, mosfixture.Columns(), mosfixture.RowsWithSystemInfo(), GeometryFormatMOS)
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (system info row skipped)", contract.ValidMOSRows)
		}
		// zero config mutation: the probe never applies the system-info row
		if layer.mosConfig != mosfixture.Config() {
			t.Fatalf("mosConfig mutated by probe: %+v", layer.mosConfig)
		}
	})

	t.Run("lower-case bounds columns persist actual names", func(t *testing.T) {
		_, contract, _ := probe(t, mosfixture.LowerCaseColumns(), mosfixture.ValidRows(), GeometryFormatMOS)
		if contract != mosfixture.ExpectedLowerCaseContract() {
			t.Fatalf("contract %+v, expected %+v (actual result-column names)", contract, mosfixture.ExpectedLowerCaseContract())
		}
	})

	// audit part11 7.1.3 regression: native rows never count as MOS rows,
	// so native-geometry tables with bounds columns are never detected as
	// MapplGIS sql-sample and never switch to mos.
	t.Run("native geometry rows never count as MOS rows", func(t *testing.T) {
		_, contract, _ := probe(t, mosfixture.Columns(), mosfixture.NativeRows(), "")
		if contract.ValidMOSRows != 0 {
			t.Fatalf("ValidMOSRows = %d, expected 0 (native rows must not count as MOS)", contract.ValidMOSRows)
		}
		if contract.ValidMOSRows >= codec.MinValidMOSRows {
			t.Fatal("native fixture must not produce MOS evidence")
		}
	})

	// audit A04: the inference (auto) probe decode closure must fall back
	// to codec.DecodeMOS so MOS rows are recognized even when the native
	// decoder rejects them.
	t.Run("auto format probe falls back to MOS decode", func(t *testing.T) {
		_, contract, _ := probe(t, mosfixture.Columns(), mosfixture.ValidRows(), "")
		if contract.ValidMOSRows != 3 {
			t.Fatalf("ValidMOSRows = %d, expected 3 (auto decode must fall back to DecodeMOS)", contract.ValidMOSRows)
		}
	})
}
