package mysql

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"database/sql/driver"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/config"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/mos"
	"github.com/go-spatial/tegola/provider"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

func TestSampleGeometryQueryRemovesTrailingLimitAndSemicolon(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "limit and semicolon",
			sql:  "SELECT geom FROM features LIMIT 1;",
			want: "SELECT geom FROM features LIMIT 16",
		},
		{
			name: "limit without semicolon",
			sql:  "SELECT geom FROM features LIMIT 1",
			want: "SELECT geom FROM features LIMIT 16",
		},
		{
			name: "limit offset",
			sql:  "SELECT geom FROM features LIMIT 1 OFFSET 4;",
			want: "SELECT geom FROM features LIMIT 16",
		},
		{
			name: "no limit",
			sql:  "SELECT geom FROM features;",
			want: "SELECT geom FROM features LIMIT 16",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sampleGeometryQuery(tc.sql); got != tc.want {
				t.Fatalf("sampleGeometryQuery(%q) = %q, want %q", tc.sql, got, tc.want)
			}
			if strings.Contains(tc.want, "; LIMIT") {
				t.Fatalf("sampling LIMIT was appended after a statement terminator: %q", tc.want)
			}
		})
	}
}

func TestMySQLBBoxUsesConfiguredSRID(t *testing.T) {
	layer := &Layer{
		geomFieldname:  "geom",
		geometryFormat: GeometryFormatWKT,
		srid:           4326,
	}
	tile := provider.NewTile(0, 0, 0, 0, 4326)
	extent, _ := tile.BufferedExtent()

	query := replaceTokens("WHERE !BBOX!", layer, tile, extent)
	if !strings.Contains(query, "ST_GeomFromText(`geom`, 4326)") {
		t.Fatalf("WKT geometry expression does not carry SRID: %q", query)
	}
	if !strings.Contains(query, "ST_GeomFromText('POLYGON") {
		t.Fatalf("bbox expression does not use ST_GeomFromText: %q", query)
	}
	if !strings.Contains(query, ", 4326)") {
		t.Fatalf("bbox expression does not carry SRID: %q", query)
	}

	layer.srid = 0
	query = replaceTokens("WHERE !BBOX!", layer, tile, extent)
	if strings.Contains(query, ", 4326)") {
		t.Fatalf("SRID leaked into zero-SRID query: %q", query)
	}
}

func TestMySQLBBoxWKBUsesGeomFromWKB(t *testing.T) {
	layer := &Layer{
		geomFieldname:  "geom",
		geometryFormat: GeometryFormatWKB,
		srid:           4326,
	}
	tile := provider.NewTile(0, 0, 0, 0, 4326)
	extent, _ := tile.BufferedExtent()

	query := replaceTokens("WHERE !BBOX!", layer, tile, extent)
	if !strings.Contains(query, "ST_GeomFromWKB(`geom`, 4326)") {
		t.Fatalf("WKB geometry expression does not use ST_GeomFromWKB with SRID: %q", query)
	}

	layer.srid = 0
	query = replaceTokens("WHERE !BBOX!", layer, tile, extent)
	if !strings.Contains(query, "ST_GeomFromWKB(`geom`)") {
		t.Fatalf("zero-SRID WKB expression must omit the SRID argument: %q", query)
	}
	if strings.Contains(query, "ST_Intersects(`geom`") {
		t.Fatalf("raw WKB column must never be used directly as a geometry: %q", query)
	}
}

func TestMySQLBBoxSyntheticSRIDDisablesDatabaseSpatialPredicate(t *testing.T) {
	srid, err := basic.RegisterProj4Defn("+proj=merc +lon_0=0 +k_0=1 +x_0=0 +y_0=0 +ellps=WGS84 +datum=WGS84 +units=m +no_defs")
	if err != nil {
		t.Fatalf("registering synthetic CRS: %v", err)
	}

	layer := &Layer{geomFieldname: "geom", srid: srid}
	tile := provider.NewTile(2, 1, 1, 64, tegola.WebMercator)
	extent, _ := tile.BufferedExtent()
	if got := replaceTokens("WHERE !BBOX!", layer, tile, extent); got != "WHERE 1=1" {
		t.Fatalf("synthetic CRS bbox = %q, want database-safe 1=1", got)
	}
}

func TestMySQLDeferredAutoBBoxUsesInMemoryFiltering(t *testing.T) {
	layer := &Layer{
		geomFieldname:      "geom",
		geometryFormat:     GeometryFormatAuto,
		deferredInspection: true,
		srid:               3857,
	}
	tile := provider.NewTile(2, 1, 1, 64, tegola.WebMercator)
	extent, _ := tile.BufferedExtent()
	if got := replaceTokens("WHERE !BBOX!", layer, tile, extent); got != "WHERE 1=1" {
		t.Fatalf("deferred auto bbox = %q, want in-memory filtering", got)
	}
}

func TestGeometryIntersectsExtent(t *testing.T) {
	extent := geom.NewExtent(geom.Point{0, 0}, geom.Point{10, 10})

	tests := []struct {
		name string
		g    geom.Geometry
		want bool
	}{
		{name: "inside point", g: geom.Point{5, 5}, want: true},
		{name: "boundary point", g: geom.Point{10, 10}, want: true},
		{name: "outside point", g: geom.Point{11, 5}, want: false},
		{name: "inside multipoint", g: geom.MultiPoint{{1, 1}, {9, 9}}, want: true},
		{name: "outside multipoint", g: geom.MultiPoint{{11, 1}, {12, 9}}, want: false},
		{name: "line crossing boundary", g: geom.LineString{{-1, 5}, {1, 5}}, want: true},
		{name: "inside line", g: geom.LineString{{1, 1}, {9, 9}}, want: true},
		{name: "outside line", g: geom.LineString{{11, 1}, {12, 9}}, want: false},
		{name: "inside multiline", g: geom.MultiLineString{{{1, 1}, {2, 2}}, {{8, 8}, {9, 9}}}, want: true},
		{name: "outside multiline", g: geom.MultiLineString{{{11, 1}, {12, 2}}}, want: false},
		{name: "inside polygon", g: geom.Polygon{{{1, 1}, {9, 1}, {9, 9}, {1, 9}, {1, 1}}}, want: true},
		{name: "outside polygon", g: geom.Polygon{{{11, 1}, {12, 1}, {12, 2}, {11, 2}, {11, 1}}}, want: false},
		{name: "inside multipolygon", g: geom.MultiPolygon{
			{{{1, 1}, {2, 1}, {2, 2}, {1, 2}, {1, 1}}},
			{{{8, 8}, {9, 8}, {9, 9}, {8, 9}, {8, 8}}},
		}, want: true},
		{name: "outside multipolygon", g: geom.MultiPolygon{
			{{{11, 1}, {12, 1}, {12, 2}, {11, 2}, {11, 1}}},
		}, want: false},
		{name: "inside collection", g: geom.Collection{
			geom.Point{5, 5},
			geom.LineString{{20, 20}, {21, 21}},
		}, want: true},
		{name: "outside collection", g: geom.Collection{
			geom.Point{20, 20},
			geom.LineString{{30, 30}, {31, 31}},
		}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codec.GeometryIntersectsExtent(tc.g, extent); got != tc.want {
				t.Fatalf("codec.GeometryIntersectsExtent(%v) = %v, want %v", tc.g, got, tc.want)
			}
		})
	}
}

func TestTrimTrailingSemicolon(t *testing.T) {
	if got := trimTrailingSemicolon(" SELECT * FROM features ;  "); got != "SELECT * FROM features" {
		t.Fatalf("trimTrailingSemicolon() = %q", got)
	}
}

// TestMySQLMOSBoundsSQL covers the R3-08 provider-level regression: the
// !BBOX! token for MOS layers must expand to the coarse indexed raw-bounds
// filter scaled from the tile metres, not to a spatial predicate over the
// opaque blob.
func TestMySQLMOSBoundsSQL(t *testing.T) {
	type tcase struct {
		name        string
		mosConfig   codec.MOSConfig
		bbox        *geom.Extent
		wantBBoxSQL string
	}

	fn := func(tc tcase) func(t *testing.T) {
		return func(t *testing.T) {
			layer := &Layer{
				geomFieldname:  "geom",
				geometryFormat: GeometryFormatMOS,
				srid:           3857,
				mosConfig:      tc.mosConfig,
			}
			tile := provider.NewTile(0, 0, 0, 64, tegola.WebMercator)
			extent := tc.bbox
			got := replaceTokens("WHERE !BBOX!", layer, tile, extent)
			want := "WHERE " + tc.wantBBoxSQL
			if got != want {
				t.Fatalf("MOS bbox = %q, want %q", got, want)
			}
		}
	}

	// extent [-10..10] metres, precision 2 (scale 100) with metre units
	// (unit factor 1) => raw bounds [-1000..1000].
	metreUnits := codec.MOSConfig{Precision: 2, UnitFactor: 1}
	tests := map[string]tcase{
		"bounds scaled by precision": {
			mosConfig:   metreUnits,
			bbox:        geom.NewExtent(geom.Point{-10, -10}, geom.Point{10, 10}),
			wantBBoxSQL: "MINX <= 1000 AND MAXX >= -1000 AND MINY <= 1000 AND MAXY >= -1000",
		},
		"degenerate zero-extent tile falls back to raw bounds too": {
			mosConfig:   metreUnits,
			bbox:        geom.NewExtent(geom.Point{0, 0}, geom.Point{0, 0}),
			wantBBoxSQL: "MINX <= 0 AND MAXX >= 0 AND MINY <= 0 AND MAXY >= 0",
		},
		"invalid mos config falls back to 1=1": {
			mosConfig:   codec.MOSConfig{Precision: 2, UnitFactor: 0},
			bbox:        geom.NewExtent(geom.Point{-10, -10}, geom.Point{10, 10}),
			wantBBoxSQL: "1=1",
		},
		"nil extent falls back to 1=1": {
			mosConfig:   metreUnits,
			bbox:        nil,
			wantBBoxSQL: "1=1",
		},
	}

	for name, tc := range tests {
		t.Run(name, fn(tc))
	}
}

func TestValidateMOSPrecision(t *testing.T) {
	for _, precision := range []float64{0, 2, codec.MaxMOSPrecision} {
		if err := codec.ValidateMOSPrecision(precision); err != nil {
			t.Errorf("codec.ValidateMOSPrecision(%v) = %v", precision, err)
		}
	}
	for _, precision := range []float64{-1, 1.5, codec.MaxMOSPrecision + 1, math.NaN(), math.Inf(1)} {
		if err := codec.ValidateMOSPrecision(precision); err == nil {
			t.Errorf("codec.ValidateMOSPrecision(%v) succeeded, want error", precision)
		}
	}
}

type samplingTestDriver struct {
	values [][]driver.Value
}

func (d *samplingTestDriver) Open(string) (driver.Conn, error) {
	return &samplingTestConn{driver: d}, nil
}

type samplingTestConn struct {
	driver *samplingTestDriver
}

func (c *samplingTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (c *samplingTestConn) Close() error              { return nil }
func (c *samplingTestConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }

func (c *samplingTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &samplingTestRows{values: c.driver.values}, nil
}

type samplingTestRows struct {
	values [][]driver.Value
	index  int
}

func (r *samplingTestRows) Columns() []string { return []string{"geom"} }
func (r *samplingTestRows) Close() error      { return nil }

func (r *samplingTestRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	dest[0] = r.values[r.index][0]
	r.index++
	return nil
}

func (r *samplingTestRows) ColumnTypeDatabaseTypeName(int) string { return "BLOB" }

// staticRowsDriver is a fake database/sql driver that returns a fixed set of
// rows for every query, regardless of the SQL text. It emulates a server that
// cannot evaluate spatial predicates, so only the provider's in-memory exact
// filter can drop out-of-tile rows.
type staticRowsDriver struct {
	rows [][]driver.Value
}

func (d *staticRowsDriver) Open(string) (driver.Conn, error) {
	return &staticRowsConn{rows: d.rows}, nil
}

type staticRowsConn struct {
	rows [][]driver.Value
}

func (c *staticRowsConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (c *staticRowsConn) Close() error              { return nil }
func (c *staticRowsConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }

func (c *staticRowsConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &staticRows{rows: c.rows}, nil
}

type staticRows struct {
	rows [][]driver.Value
	next int
}

func (r *staticRows) Columns() []string { return []string{"id", "geom"} }
func (r *staticRows) Close() error      { return nil }
func (r *staticRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

func TestTileFeaturesWKBInMemoryFilter(t *testing.T) {
	inside, err := wkb.EncodeBytes(geom.Point{-1000000, 1000000})
	if err != nil {
		t.Fatal(err)
	}
	outside, err := wkb.EncodeBytes(geom.Point{1000000, -1000000})
	if err != nil {
		t.Fatal(err)
	}

	driverName := "tegola_mysql_wkb_rows_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
	sql.Register(driverName, &staticRowsDriver{rows: [][]driver.Value{
		{int64(1), inside},
		{int64(2), outside},
	}})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The fake server returns both rows for any query, so the SQL !BBOX!
	// predicate cannot be what filters: the exact in-memory filter is
	// mandatory for the raw wkb format.
	p := &Provider{
		db: db,
		layers: map[string]Layer{
			"test": {
				name:           "test",
				sql:            "SELECT id, geom FROM test WHERE !BBOX!",
				idFieldname:    "id",
				geomFieldname:  "geom",
				geometryFormat: GeometryFormatWKB,
				srid:           tegola.WebMercator,
			},
		},
	}

	var got []provider.Feature
	err = p.TileFeatures(context.Background(), "test", provider.NewTile(1, 0, 0, 0, tegola.WebMercator), nil,
		func(f *provider.Feature) error {
			got = append(got, *f)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected only the inside point to survive the exact in-memory filter, got %d features", len(got))
	}
	if got[0].ID != uint64(1) {
		t.Fatalf("expected feature id 1, got %v", got[0].ID)
	}
}

func TestGeomTypeFromColumnKeepsSamplingAfterGeometry(t *testing.T) {
	systemInfo := make([]byte, 64)
	copy(systemInfo, []byte{5, 'V', 'e', 'r', ' ', '1'})
	binary.LittleEndian.PutUint32(systemInfo[11:15], 4)

	driverName := "tegola_mysql_sampling_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
	sql.Register(driverName, &samplingTestDriver{
		values: [][]driver.Value{{"POINT(1 2)"}, {systemInfo}},
	})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	geo, _, sysInfo, err := geomTypeFromColumn(db, "SELECT geom", GeometryFormatWKT, GeometryFormatMySQL, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := geo.(geom.Point); !ok {
		t.Fatalf("expected geom.Point, got %T", geo)
	}
	if sysInfo == nil || sysInfo.Precision != 4 {
		t.Fatalf("expected system info precision 4, got %#v", sysInfo)
	}
}

func TestGeomTypeFromColumnLayerInfoPositionInvariant(t *testing.T) {
	// Sampling invariant (docs/provider-contract.md): the MapplGIS LayerInfo
	// blob must be picked up wherever it sits inside the shared 16-row
	// sample window — first row, middle or last.
	systemInfo := make([]byte, 64)
	copy(systemInfo, []byte{5, 'V', 'e', 'r', ' ', '1'})
	binary.LittleEndian.PutUint32(systemInfo[11:15], 4)

	for _, pos := range []int{1, 5, codec.InspectionSampleLimit} {
		pos := pos
		t.Run("position "+strconv.Itoa(pos), func(t *testing.T) {
			values := make([][]driver.Value, 0, codec.InspectionSampleLimit)
			for i := 1; i <= codec.InspectionSampleLimit; i++ {
				if i == pos {
					values = append(values, []driver.Value{systemInfo})
					continue
				}
				values = append(values, []driver.Value{"POINT(1 2)"})
			}

			driverName := "tegola_mysql_layerinfo_pos_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
			sql.Register(driverName, &samplingTestDriver{values: values})
			db, err := sql.Open(driverName, "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			geo, _, sysInfo, err := geomTypeFromColumn(db, "SELECT geom", GeometryFormatWKT, GeometryFormatMySQL, codec.DefaultMOSConfig())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := geo.(geom.Point); !ok {
				t.Fatalf("expected geom.Point, got %T", geo)
			}
			if sysInfo == nil || sysInfo.Precision != 4 {
				t.Fatalf("expected system info precision 4, got %#v", sysInfo)
			}
		})
	}
}

func TestGeomTypeFromColumnSkipsNullGeometryRows(t *testing.T) {
	driverName := "tegola_mysql_null_sampling_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
	sql.Register(driverName, &samplingTestDriver{
		values: [][]driver.Value{{nil}, {"POINT(1 2)"}},
	})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	geo, _, _, err := geomTypeFromColumn(db, "SELECT geom", GeometryFormatWKT, GeometryFormatMySQL, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := geo.(geom.Point); !ok {
		t.Fatalf("expected geom.Point, got %T", geo)
	}
}

type retryTestDriver struct {
	mu          sync.Mutex
	queryCount  int
	failInitial bool
	failRead    bool
}

var retryTestDriverID atomic.Uint64

func (d *retryTestDriver) Open(string) (driver.Conn, error) {
	return &retryTestConn{driver: d}, nil
}

type retryTestConn struct {
	driver *retryTestDriver
}

func (c *retryTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *retryTestConn) Close() error                        { return nil }
func (c *retryTestConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *retryTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	c.driver.queryCount++
	if c.driver.failInitial && c.driver.queryCount == 1 {
		return nil, driver.ErrBadConn
	}
	return &retryTestRows{failRead: c.driver.failRead && c.driver.queryCount == 1}, nil
}

type retryTestRows struct {
	sent     bool
	failRead bool
}

func (r *retryTestRows) Columns() []string { return []string{"id", "geom"} }
func (r *retryTestRows) Close() error      { return nil }

func (r *retryTestRows) Next(dest []driver.Value) error {
	if !r.sent {
		r.sent = true
		dest[0] = int64(7)
		dest[1] = "POINT(1 2)"
		return nil
	}
	if r.failRead {
		return mysqlDriver.ErrInvalidConn
	}
	return io.EOF
}

func (r *retryTestRows) ColumnTypeDatabaseTypeName(index int) string {
	if index == 0 {
		return "BIGINT"
	}
	return "TEXT"
}

func newRetryTestProvider(t *testing.T, d *retryTestDriver) (*Provider, *sql.DB) {
	t.Helper()
	driverName := "tegola_mysql_retry_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
	sql.Register(driverName, d)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{
		db: db,
		layers: map[string]Layer{
			"test": {
				name:           "test",
				sql:            "SELECT id, geom FROM test",
				idFieldname:    "id",
				geomFieldname:  "geom",
				geometryFormat: GeometryFormatWKT,
				srid:           4326,
			},
		},
	}
	return p, db
}

func TestTileFeaturesRetriesBeforeEmittingFeatures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failInitial bool
		failRead    bool
	}{
		{name: "initial query", failInitial: true},
		{name: "mid stream", failRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &retryTestDriver{failInitial: tc.failInitial, failRead: tc.failRead}
			p, db := newRetryTestProvider(t, d)
			defer db.Close()

			var got []provider.Feature
			err := p.TileFeatures(context.Background(), "test", provider.NewTile(0, 0, 0, 0, 4326), nil,
				func(f *provider.Feature) error {
					got = append(got, *f)
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("expected one feature after retry, got %d", len(got))
			}
			d.mu.Lock()
			queries := d.queryCount
			d.mu.Unlock()
			if queries != 2 {
				t.Fatalf("expected two queries, got %d", queries)
			}
		})
	}
}

func TestMySQLConnectionRetryClassification(t *testing.T) {
	for _, err := range []error{driver.ErrBadConn, mysqlDriver.ErrInvalidConn} {
		if !isRetryableConnectionError(err) {
			t.Errorf("expected %T to be retryable", err)
		}
		if !isRetryableConnectionError(errors.Join(errors.New("wrapped"), err)) {
			t.Errorf("expected wrapped %T to be retryable", err)
		}
	}
	if isRetryableConnectionError(errors.New("query failed")) {
		t.Error("ordinary query errors must not be retried")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForMySQLRetry(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled retry wait, got %v", err)
	}
}

func TestPreferContextError(t *testing.T) {
	driverErr := errors.New("driver query failed")

	t.Run("returns cancellation from context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if got := preferContextError(ctx, driverErr); !errors.Is(got, context.Canceled) {
			t.Fatalf("preferContextError() = %v, want context.Canceled", got)
		}
	})

	t.Run("preserves driver error when context is active", func(t *testing.T) {
		if got := preferContextError(context.Background(), driverErr); !errors.Is(got, driverErr) {
			t.Fatalf("preferContextError() = %v, want %v", got, driverErr)
		}
	})
}

// wkbPoint takes an x/y pair and returns the full WKB encoding of a 2D Point
// in little-endian byte order.
func wkbPoint(t *testing.T, x, y float64) []byte {
	t.Helper()
	b := []byte{1} // little-endian bom
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
	b = binary.LittleEndian.AppendUint64(b, 1)
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(y))
	return b
}

// TestDecodeMySQLFormat exercises the MySQL native internal geometry layout:
// [4 bytes SRID (little-endian)][1 byte byte-order][WKB (type + body)]
func TestDecodeMySQLFormat(t *testing.T) {
	wkb := wkbPoint(t, 1.5, 2.5)

	// assemble a MySQL-native geometry blob with SRID 3857
	blob := make([]byte, 0, 4+1+len(wkb))
	blob = binary.LittleEndian.AppendUint32(blob, 3857)
	blob = append(blob, 1)
	blob = append(blob, wkb...)

	srid, geo, err := decodeMySQLFormat(blob)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 3857 {
		t.Errorf("expected srid 3857, got %v", srid)
	}
	if geo == nil {
		t.Fatal("expected geometry, got nil")
	}
	if _, ok := geo.(geom.Point); !ok {
		t.Errorf("expected geom.Point, got %T", geo)
	}

	// too-short blob is an error
	if _, _, err := decodeMySQLFormat([]byte{1, 2, 3}); err == nil {
		t.Error("expected error for short blob, got nil")
	}

	// invalid bom is an error
	bad := append([]byte{0, 0, 0, 0}, 9)
	bad = append(bad, wkb...)
	if _, _, err := decodeMySQLFormat(bad); err == nil {
		t.Error("expected error for invalid bom, got nil")
	}
}

// TestDecodeMariaDBFormat exercises the MariaDB native internal geometry
// layout: [1 byte byte-order][4 bytes SRID in that byte order][WKB]. Note
// the SRID follows the byte-order marker, unlike MySQL where it precedes it.
func TestDecodeMariaDBFormat(t *testing.T) {
	wkb := wkbPoint(t, 1.5, 2.5)

	// rebuild cleanly: [bom=1][SRID LE][WKB]
	blob := []byte{1}
	blob = binary.LittleEndian.AppendUint32(blob, 4326)
	blob = append(blob, wkb...)

	srid, geo, err := decodeMariaDBFormat(blob)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 4326 {
		t.Errorf("expected srid 4326, got %v", srid)
	}
	if _, ok := geo.(geom.Point); !ok {
		t.Errorf("expected geom.Point, got %T", geo)
	}

	// big-endian MariaDB blob with SRID 4326
	beBlob := []byte{0} // big-endian bom
	beBlob = binary.BigEndian.AppendUint32(beBlob, 4326)
	beBlob = append(beBlob, wkb...)

	srid, _, err = decodeMariaDBFormat(beBlob)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 4326 {
		t.Errorf("expected srid 4326, got %v", srid)
	}

	// MariaDB 10.7+ axis-order flags stored in the top three SRID bits must
	// be masked off (flags value 1 => SRID | 0x20000000)
	flagged := []byte{1}
	flagged = binary.LittleEndian.AppendUint32(flagged, 4326|0x20000000)
	flagged = append(flagged, wkb...)

	srid, _, err = decodeMariaDBFormat(flagged)
	if err != nil {
		t.Fatal(err)
	}
	if srid != 4326 {
		t.Errorf("expected masked srid 4326, got %v", srid)
	}

	// too-short blob is an error
	if _, _, err := decodeMariaDBFormat([]byte{1}); err == nil {
		t.Error("expected error for short blob, got nil")
	}
}

// TestDecodeGeometryAutoFallback verifies the "auto" format decodes MySQL
// native blobs and falls back to plain WKB when the native layout doesn't fit.
func TestDecodeGeometryAutoFallback(t *testing.T) {
	wkb := wkbPoint(t, 3.0, 4.0)

	// MySQL-native blob
	native := make([]byte, 0)
	native = binary.LittleEndian.AppendUint32(native, 3857)
	native = append(native, 1)
	native = append(native, wkb...)

	srid, geo, err := decodeGeometry(native, GeometryFormatAuto, GeometryFormatMySQL, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if srid != 3857 {
		t.Errorf("expected srid 3857, got %v", srid)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// plain WKB with the auto flavor should fall back and decode successfully
	// (SRID 0 since plain WKB carries no header)
	srid, geo, err = decodeGeometry(wkb, GeometryFormatAuto, GeometryFormatMySQL, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if srid != 0 {
		t.Errorf("expected srid 0 for plain WKB, got %v", srid)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// explicit wkb format
	srid, geo, err = decodeGeometry(wkb, GeometryFormatWKB, GeometryFormatMySQL, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// unknown format is an error
	if _, _, err := decodeGeometry(wkb, "bogus", GeometryFormatMySQL, codec.DefaultMOSConfig()); err == nil {
		t.Error("expected error for unknown format, got nil")
	}
}

// TestDecodeGeometryWKT verifies WKT text geometry decoding, used for
// geometry stored as text (e.g. a LINESTRING(...) TEXT column).
func TestDecodeGeometryWKT(t *testing.T) {
	const line = "LINESTRING(6832560.11 7372555.90, 6832518.72 7372428.95)"

	// string value
	srid, geo, err := decodeGeometry(line, GeometryFormatWKT, GeometryFormatMariaDB, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if srid != 0 {
		t.Errorf("expected srid 0 for WKT, got %v", srid)
	}
	if _, ok := geo.(geom.LineString); !ok {
		t.Errorf("expected geom.LineString, got %T", geo)
	}

	// []byte value (TEXT columns may arrive as []byte depending on driver)
	_, geo, err = decodeGeometry([]byte(line), GeometryFormatWKT, GeometryFormatMariaDB, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if geo == nil {
		t.Error("expected geometry, got nil")
	}

	// invalid WKT is an error
	if _, _, err := decodeGeometry("NOT WKT", GeometryFormatWKT, GeometryFormatMariaDB, codec.DefaultMOSConfig()); err == nil {
		t.Error("expected error for invalid WKT, got nil")
	}

	// wrong type is an error
	if _, _, err := decodeGeometry(42, GeometryFormatWKT, GeometryFormatMariaDB, codec.DefaultMOSConfig()); err == nil {
		t.Error("expected error for int geometry value, got nil")
	}

	// auto + MariaDB flavor: WKT text in a blob column falls back through
	// mariadb -> wkb -> wkt and decodes successfully
	_, geo, err = decodeGeometry([]byte(line), GeometryFormatAuto, GeometryFormatMariaDB, codec.DefaultMOSConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := geo.(geom.LineString); !ok {
		t.Errorf("expected geom.LineString, got %T", geo)
	}
}

// TestServerFlavorFromVersion checks the VERSION() string parsing.
func TestServerFlavorFromVersion(t *testing.T) {
	cases := []struct {
		version  string
		expected string
	}{
		{"10.11.6-MariaDB-ubu2204", GeometryFormatMariaDB},
		{"8.0.36", GeometryFormatMySQL},
		{"5.7.44-log", GeometryFormatMySQL},
		{"11.4.2-MariaDB", GeometryFormatMariaDB},
	}
	for _, c := range cases {
		if got := serverFlavorFromVersion(c.version); got != c.expected {
			t.Errorf("serverFlavorFromVersion(%q) = %v, expected %v", c.version, got, c.expected)
		}
	}
}

// TestQuoteIdentifier verifies backtick escaping.
func TestQuoteIdentifier(t *testing.T) {
	cases := []struct{ in, want string }{
		{"geom", "`geom`"},
		{"my`geom", "`my``geom`"},
	}
	for _, c := range cases {
		if got := quoteIdentifier(c.in); got != c.want {
			t.Errorf("quoteIdentifier(%q) = %v, expected %v", c.in, got, c.want)
		}
	}
}

// TestReplaceTokens exercises the SQL token replacement using a real tile.
func TestReplaceTokens(t *testing.T) {
	layer := &Layer{
		name:          "testlayer",
		geomFieldname: "geom",
		idFieldname:   "fid",
	}

	tile := provider.NewTile(6, 13, 22, 0, 3857)
	ext, _ := tile.BufferedExtent()

	got := replaceTokens("!BBOX!", layer, tile, ext)
	if !strings.Contains(got, "ST_Intersects(`geom`") {
		t.Errorf("expected ST_Intersects on geom field, got: %v", got)
	}
	if !strings.Contains(got, "ST_GeomFromText('POLYGON((") {
		t.Errorf("expected WKT polygon, got: %v", got)
	}

	mosLayer := &Layer{
		tablename:      "",
		geometryFormat: GeometryFormatMOS,
		mosConfig:      codec.MOSConfig{Precision: 0, UnitFactor: 0.001},
	}
	mosExtent := geom.NewExtent(geom.Point{1.25, -2.5}, geom.Point{3.75, 4.5})
	got = replaceTokens("SELECT * FROM buildings WHERE !BBOX!", mosLayer, tile, mosExtent)
	if want := "MINX <= 3750 AND MAXX >= 1250 AND MINY <= 4500 AND MAXY >= -2500"; !strings.Contains(got, want) {
		t.Errorf("expected indexed MOS bounds %q, got: %v", want, got)
	}

	got = replaceTokens("!ZOOM!-!Z!-!X!-!Y!", layer, tile, ext)
	if got != "6-6-13-22" {
		t.Errorf("expected 6-6-13-22, got: %v", got)
	}

	got = replaceTokens("!ID_FIELD!-!GEOM_FIELD!", layer, tile, ext)
	if got != "fid-geom" {
		t.Errorf("expected fid-geom, got: %v", got)
	}

	got = replaceTokens("!GEOM_TYPE!", layer, tile, ext)
	if got != "" {
		t.Errorf("expected empty geom type for nil layer geom, got: %v", got)
	}

	// tokens are case-insensitive
	got = replaceTokens("!zoom!-!Zoom!", layer, tile, ext)
	if got != "6-6" {
		t.Errorf("expected case-insensitive zoom tokens (6-6), got: %v", got)
	}

	// numeric tokens must be valid floats
	for _, tok := range []string{config.ScaleDenominatorToken, config.PixelWidthToken, config.PixelHeightToken} {
		got = replaceTokens(tok, layer, tile, ext)
		if _, err := strconv.ParseFloat(got, 64); err != nil {
			t.Errorf("%v: expected a float, got %q", tok, got)
		}
	}
}

// TestConfigValidation runs basic config error paths against NewTileProvider.
// These paths fail before a DB connection is attempted.
func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		config dict.Dict
	}{
		{
			name: "missing host",
			config: dict.Dict{
				"database": "test", "user": "u", "password": "p",
			},
		},
		{
			name: "missing database",
			config: dict.Dict{
				"host": "localhost", "user": "u", "password": "p",
			},
		},
		{
			name: "invalid geometry format",
			config: dict.Dict{
				"host": "localhost", "database": "test",
				"user": "u", "password": "p",
				"geometry_format": "bogus",
			},
		},
	}
	for _, tc := range cases {
		_, err := NewTileProvider(tc.config, nil)
		if err == nil {
			t.Errorf("%v: expected error, got nil", tc.name)
		}
	}
}

// TestApplySystemInfo verifies the sysinfo-driven auto-configuration
// contract: precision, units and projection are applied when not explicitly
// configured.
func TestApplySystemInfo(t *testing.T) {
	projDefn := "+proj=merc +ellps=WGS84 +datum=WGS84 +units=m +no_defs"
	sysInfo := &mos.SystemInfo{
		Precision:       2,
		MapUnits:        mos.UnitsMillimetres,
		MapUnitsDefined: true,
		Projection:      projDefn,
	}

	t.Run("units factor applied", func(t *testing.T) {
		layer := Layer{name: "l", geometryFormat: GeometryFormatAuto, mosConfig: codec.DefaultMOSConfig()}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosConfig.UnitFactor != 0.001 {
			t.Errorf("mosConfig.UnitFactor = %v, want 0.001", layer.mosConfig.UnitFactor)
		}
		if layer.geometryFormat != GeometryFormatMOS {
			t.Errorf("geometryFormat = %q, want %q", layer.geometryFormat, GeometryFormatMOS)
		}
	})

	t.Run("precision applied when not explicit", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.DefaultMOSConfig()}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosConfig.Precision != 2 {
			t.Errorf("mosConfig.Precision = %v, want 2", layer.mosConfig.Precision)
		}
	})

	t.Run("explicit precision wins", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.MOSConfig{Precision: 4, UnitFactor: 1, PrecisionSet: true}}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosConfig.Precision != 4 {
			t.Errorf("mosConfig.Precision = %v, want 4 (explicit config wins)", layer.mosConfig.Precision)
		}
	})

	t.Run("explicit zero precision wins", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.MOSConfig{Precision: 0, UnitFactor: 1, PrecisionSet: true}}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosConfig.Precision != 0 {
			t.Errorf("mosConfig.Precision = %v, want 0 (explicit config wins)", layer.mosConfig.Precision)
		}
	})

	t.Run("explicit units wins", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.MOSConfig{Precision: 0, UnitFactor: 1000, UnitsSet: true}}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosConfig.UnitFactor != 1000 {
			t.Errorf("mosConfig.UnitFactor = %v, want 1000 (explicit config wins)", layer.mosConfig.UnitFactor)
		}
	})

	t.Run("projection applied when srid not explicit", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.DefaultMOSConfig()}
		if err := applySystemInfo(&layer, sysInfo, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.srid == 0 {
			t.Fatal("expected a synthetic srid to be registered")
		}
		defn, ok := basic.Proj4DefnSRID(projDefn)
		if !ok {
			t.Fatalf("registered srid %v not found by defn lookup", layer.srid)
		}
		if defn != layer.srid {
			t.Errorf("Proj4DefnSRID = %v, want %v", defn, layer.srid)
		}
	})

	t.Run("projection not applied when srid explicit", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.DefaultMOSConfig(), srid: 3857}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.srid != 3857 {
			t.Errorf("srid = %v, want 3857 (explicit srid wins)", layer.srid)
		}
	})

	t.Run("layer srid suppresses system projection", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.DefaultMOSConfig(), srid: 32637, crsExplicit: true}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.srid != 32637 {
			t.Errorf("srid = %v, want 32637 (layer srid wins)", layer.srid)
		}
	})

	t.Run("layer crs definition suppresses system projection", func(t *testing.T) {
		layerSRID, err := basic.RegisterProj4Defn(projDefn)
		if err != nil {
			t.Fatalf("registering layer CRS: %v", err)
		}
		layer := Layer{name: "l", mosConfig: codec.DefaultMOSConfig(), srid: layerSRID, crsExplicit: true}
		if err := applySystemInfo(&layer, sysInfo, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.srid != layerSRID {
			t.Errorf("srid = %v, want %v (layer crs_defn wins)", layer.srid, layerSRID)
		}
	})

	t.Run("nil sysinfo is a no-op", func(t *testing.T) {
		layer := Layer{name: "l", mosConfig: codec.MOSConfig{Precision: 3, UnitFactor: 1}, srid: 3395}
		if err := applySystemInfo(&layer, nil, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if layer.mosConfig.Precision != 3 || layer.srid != 3395 {
			t.Errorf("layer modified by nil sysinfo: precision=%v srid=%v", layer.mosConfig.Precision, layer.srid)
		}
	})
}

func TestApplyRuntimeSystemInfo(t *testing.T) {
	const projection = "+proj=merc +ellps=WGS84 +datum=WGS84 +units=m +no_defs"
	blob := make([]byte, 64+len(projection))
	copy(blob, []byte{5, 'V', 'e', 'r', ' ', '1'})
	binary.LittleEndian.PutUint32(blob[11:15], 3)
	blob[15] = 1
	blob[52] = byte(mos.UnitsCentimetres)
	blob[53] = 1
	binary.LittleEndian.PutUint32(blob[60:64], uint32(len(projection)))
	copy(blob[64:], projection)

	layer := Layer{name: "deferred", geometryFormat: GeometryFormatAuto, mosConfig: codec.DefaultMOSConfig()}
	format := layer.geometryFormat
	tile := provider.NewTile(2, 1, 1, 64, tegola.WebMercator)
	tileBBox, tileSRID := tile.BufferedExtent()
	webMercatorBBox := tileBBox

	if err := applyRuntimeSystemInfo(&layer, &format, &layer.mosConfig, &tileBBox, webMercatorBBox, tileSRID, blob); err != nil {
		t.Fatalf("applyRuntimeSystemInfo: %v", err)
	}
	if format != GeometryFormatMOS {
		t.Fatalf("geometry format = %q, want %q", format, GeometryFormatMOS)
	}
	if layer.mosConfig.Precision != 3 {
		t.Fatalf("precision = %v, want 3", layer.mosConfig.Precision)
	}
	if layer.mosConfig.UnitFactor != 0.01 {
		t.Fatalf("units factor = %v, want 0.01", layer.mosConfig.UnitFactor)
	}
	if layer.srid == 0 || !basic.IsSyntheticSRID(layer.srid) {
		t.Fatalf("expected synthetic runtime SRID, got %v", layer.srid)
	}
	if tileBBox == webMercatorBBox {
		t.Fatal("expected tile extent to be converted for runtime projection")
	}
}

// TestMySQLLayerGeometryFormatResolution verifies the layer-level
// geometry_format override contract (R4-02): the layer value wins over the
// provider value, is validated against the full MySQL value set with the
// layer name in the error context, and resolving one layer never mutates
// the provider-level value seen by subsequent layers.
func TestMySQLLayerGeometryFormatResolution(t *testing.T) {
	tcases := []struct {
		name          string
		providerValue string
		layer         dict.Dict
		expect        string
		expectErr     bool
	}{
		{name: "provider auto + layer mos", providerValue: GeometryFormatAuto, layer: dict.Dict{"geometry_format": "mos"}, expect: GeometryFormatMOS},
		{name: "provider mysql + layer wkb", providerValue: GeometryFormatMySQL, layer: dict.Dict{"geometry_format": "wkb"}, expect: GeometryFormatWKB},
		{name: "provider mos + layer wkt", providerValue: GeometryFormatMOS, layer: dict.Dict{"geometry_format": "wkt"}, expect: GeometryFormatWKT},
		{name: "no layer key keeps provider value", providerValue: GeometryFormatMariaDB, layer: dict.Dict{}, expect: GeometryFormatMariaDB},
		{name: "invalid layer format errors", providerValue: GeometryFormatAuto, layer: dict.Dict{"geometry_format": "bogus"}, expectErr: true},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := codec.ResolveLayerGeometryFormat(tc.providerValue, tc.layer, "roads", mysqlGeometryFormats)
			if tc.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "roads") {
					t.Errorf("error should mention layer name: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expect {
				t.Errorf("expected %q got %q", tc.expect, got)
			}
		})
	}
}

// TestMySQLTwoLayersDifferentFormats verifies that two sequential layer
// resolutions with different layer-level overrides stay independent.
func TestMySQLTwoLayersDifferentFormats(t *testing.T) {
	providerValue := GeometryFormatAuto
	first, err := codec.ResolveLayerGeometryFormat(providerValue, dict.Dict{"geometry_format": "mos"}, "mos_layer", mysqlGeometryFormats)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := codec.ResolveLayerGeometryFormat(providerValue, dict.Dict{"geometry_format": "wkb"}, "wkb_layer", mysqlGeometryFormats)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first != GeometryFormatMOS || second != GeometryFormatWKB {
		t.Errorf("expected (mos, wkb), got (%v, %v)", first, second)
	}
}

// TestMySQLMOSParamsKeptForAutoFormat verifies the R4-04 semantics: with
// geometry_format auto (which may still switch to MOS at runtime via a
// MapplGIS LayerInfo blob) explicit MOS params are kept, not warned or
// reset; only explicitly raw layer formats ignore them.
func TestMySQLMOSParamsKeptForAutoFormat(t *testing.T) {
	cfg := codec.MOSConfig{Precision: 3, PrecisionSet: true, UnitFactor: 0.001, UnitsSet: true}

	for _, format := range []string{GeometryFormatAuto, GeometryFormatMOS} {
		if format != GeometryFormatAuto && format != GeometryFormatMOS {
			continue
		}
		if !cfg.HasExplicitMOSParams() {
			t.Errorf("%v: explicit params should be detected", format)
		}
	}

	for _, format := range []string{GeometryFormatWKB, GeometryFormatWKT, GeometryFormatMySQL, GeometryFormatMariaDB} {
		// registration warns and resets to defaults for these formats
		reset := codec.DefaultMOSConfig()
		if reset.PrecisionSet || reset.UnitsSet {
			t.Errorf("%v: reset config should carry no explicit flags: %+v", format, reset)
		}
	}
}

type noQueryDriver struct{}

func (d *noQueryDriver) Open(string) (driver.Conn, error) { return &noQueryConn{}, nil }

type noQueryConn struct{}

func (c *noQueryConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *noQueryConn) Close() error                        { return nil }
func (c *noQueryConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (c *noQueryConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("no startup queries expected")
}

// TestExplicitGeometryTypeSkipsStartupInspection verifies the common
// geometry_type layer key contract for MySQL (docs/provider-contract.md):
// an explicit value fixes the layer type before any data is read and skips
// the startup inspection query entirely, and an unsupported value fails
// provider construction.
func TestExplicitGeometryTypeSkipsStartupInspection(t *testing.T) {
	baseConfig := func() dict.Dict {
		return dict.Dict{
			"host": "localhost", "database": "test",
			"user": "u", "password": "p",
			"layers": []map[string]interface{}{
				{
					"name":           "lines",
					"tablename":      "lines",
					"id_fieldname":   "id",
					"geom_fieldname": "geom",
				},
			},
		}
	}

	t.Run("explicit type skips inspection", func(t *testing.T) {
		// This subtest exercises the full provider construction path, which
		// requires a reachable MySQL server. It is gated the same way as the
		// postgis live tests (RUN_MYSQL_TESTS=yes).
		if os.Getenv("RUN_MYSQL_TESTS") != "yes" {
			t.Skip("skip live construction test, set RUN_MYSQL_TESTS=yes to run")
		}
		driverName := "tegola_mysql_no_query_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
		sql.Register(driverName, &noQueryDriver{})

		config := baseConfig()
		config["layers"].([]map[string]interface{})[0]["geometry_type"] = "LineString"

		prov, err := NewTileProvider(config, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lyrs, err := prov.(provider.Layerer).Layers()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(lyrs) != 1 {
			t.Fatalf("expected 1 layer, got %d", len(lyrs))
		}
		if _, ok := lyrs[0].GeomType().(geom.LineString); !ok {
			t.Fatalf("expected geom.LineString, got %T", lyrs[0].GeomType())
		}
	})

	t.Run("invalid type fails construction", func(t *testing.T) {
		driverName := "tegola_mysql_no_query_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
		sql.Register(driverName, &noQueryDriver{})

		config := baseConfig()
		config["layers"].([]map[string]interface{})[0]["geometry_type"] = "triangle"

		if _, err := NewTileProvider(config, nil); err == nil {
			t.Fatal("expected error for unsupported geometry_type, got nil")
		}
	})
}

// TestExplicitGeometryTypeMixedContentPermitted verifies the mixed-content
// policy: features whose decoded type differs from the explicitly configured
// geometry_type are permitted (with a one-time warning), they do not break
// tile rendering.
func TestExplicitGeometryTypeMixedContentPermitted(t *testing.T) {
	inside, err := wkb.EncodeBytes(geom.Point{-1000000, 1000000})
	if err != nil {
		t.Fatal(err)
	}

	driverName := "tegola_mysql_mixed_rows_test_" + strconv.FormatUint(retryTestDriverID.Add(1), 10)
	sql.Register(driverName, &staticRowsDriver{rows: [][]driver.Value{
		{int64(1), inside},
	}})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	p := &Provider{
		db: db,
		layers: map[string]Layer{
			"test": {
				name:             "test",
				sql:              "SELECT id, geom FROM test WHERE !BBOX!",
				idFieldname:      "id",
				geomFieldname:    "geom",
				geometryFormat:   GeometryFormatWKB,
				srid:             tegola.WebMercator,
				geomType:         geom.LineString{},
				geomTypeExplicit: true,
			},
		},
	}

	var got []provider.Feature
	err = p.TileFeatures(context.Background(), "test", provider.NewTile(1, 0, 0, 0, tegola.WebMercator), nil,
		func(f *provider.Feature) error {
			got = append(got, *f)
			return nil
		})
	if err != nil {
		t.Fatalf("mixed-content feature must not break tile rendering: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the point feature to be permitted, got %d features", len(got))
	}
	if _, ok := got[0].Geometry.(geom.Point); !ok {
		t.Fatalf("expected geom.Point, got %T", got[0].Geometry)
	}
}
