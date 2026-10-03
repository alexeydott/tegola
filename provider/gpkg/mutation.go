package gpkg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/tegola/internal/log"
	"github.com/alexeydott/tegola/provider"
)

// sqliteWritableDSN opens the GeoPackage for writing. busy_timeout keeps
// writers patient; synchronous/foreign_keys are left at their safe
// defaults (never disabled for speed).
func sqliteWritableDSN(path string) string {
	return "file:" + path + "?_busy_timeout=10000"
}

// Writer adds the provider.MutationProvider contract to the GPKG provider.
// It is created lazily and only used when a collection passes write
// admission; the read provider stays read-only.
type Writer struct {
	provider *Provider
	mu       sync.Mutex
	db       *sql.DB
	// mappings caches admission results per layer.
	mappings map[string]*writeMapping
}

// MutationWriter returns the provider.MutationProvider contract for this
// provider. No connection is opened until BeginFeatureTx; the read path
// stays on its read-only handle.
func (p *Provider) MutationWriter() provider.MutationProvider {
	return p.writer()
}

// DescribeWritable implements provider.MutationProvider via the writer.
func (p *Provider) DescribeWritable(ctx context.Context, layer string) (provider.WriteDescriptor, error) {
	return p.MutationWriter().DescribeWritable(ctx, layer)
}

// BeginFeatureTx implements provider.MutationProvider via the writer.
func (p *Provider) BeginFeatureTx(ctx context.Context, options provider.TxOptions) (provider.FeatureTx, error) {
	return p.MutationWriter().BeginFeatureTx(ctx, options)
}

// DescribeSchema implements provider.SchemaProvider via the writer.
func (p *Provider) DescribeSchema(ctx context.Context, layer string) (provider.SchemaDescriptor, error) {
	w, ok := p.MutationWriter().(*Writer)
	if !ok {
		return provider.SchemaDescriptor{}, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: "schema not available"}
	}
	return w.DescribeSchema(ctx, layer)
}

func (p *Provider) writer() *Writer {
	p.writerMu.Lock()
	defer p.writerMu.Unlock()
	if p.cachedWriter == nil {
		p.cachedWriter = &Writer{provider: p, mappings: make(map[string]*writeMapping)}
	}
	return p.cachedWriter
}

// writeMapping is the admission result for one layer.
type writeMapping struct {
	layer      *Layer
	table      string
	idColumn   string
	geomColumn string
	geomFormat string // gpkg, wkb, wkt
	geomType   string // point, linestring, polygon, ...
	geomSRID   uint64
	columns    map[string]provider.ColumnDescriptor // by column name
	writable   map[string]string                    // public name -> column
	readOnly   []string
	domain     string
}

// DescribeWritable implements provider.MutationProvider admission
// (ADR-0013). Only plain tablename layers with a single INTEGER PRIMARY
// KEY, a supported geometry encoding and no custom SQL are admitted.
func (w *Writer) DescribeWritable(ctx context.Context, layer string) (provider.WriteDescriptor, error) {
	m, err := w.mapping(ctx, layer)
	if err != nil {
		return provider.WriteDescriptor{}, err
	}
	// Defensive copies: callers must not be able to mutate the cached
	// admission result through the returned descriptor.
	writable := make(map[string]string, len(m.writable))
	for k, v := range m.writable {
		writable[k] = v
	}
	readOnly := append([]string(nil), m.readOnly...)
	wd := provider.WriteDescriptor{
		Layer:           layer,
		Table:           m.table,
		IDColumn:        m.idColumn,
		GeometryColumn:  m.geomColumn,
		GeometryType:    m.geomType,
		GeometrySRID:    m.geomSRID,
		WritableColumns: writable,
		ReadOnlyColumns: readOnly,
		Domain:          m.domain,
	}
	return wd, nil
}

// DescribeSchema implements provider.SchemaProvider.
func (w *Writer) DescribeSchema(ctx context.Context, layer string) (provider.SchemaDescriptor, error) {
	m, err := w.mapping(ctx, layer)
	if err != nil {
		return provider.SchemaDescriptor{}, err
	}
	sd := provider.SchemaDescriptor{
		Layer:    layer,
		Table:    m.table,
		IDColumn: m.idColumn,
		Geometry: provider.GeometryColumnDescriptor{
			Name: m.geomColumn,
			Type: m.geomType,
			SRID: m.geomSRID,
		},
	}
	for _, col := range m.columns {
		sd.Columns = append(sd.Columns, col)
	}
	return sd, nil
}

func (w *Writer) mapping(ctx context.Context, layer string) (*writeMapping, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if m, ok := w.mappings[layer]; ok {
		return m, nil
	}
	l, ok := w.provider.layers[layer]
	if !ok {
		return nil, &provider.MutationError{Kind: provider.MutationErrNotFound, Reason: fmt.Sprintf("layer %q not registered", layer)}
	}
	m, err := admitLayer(w.provider.Filepath, l)
	if err != nil {
		return nil, err
	}
	w.mappings[layer] = m
	return m, nil
}

// admitLayer runs write admission for one GPKG layer.
func admitLayer(filepath string, l *Layer) (*writeMapping, error) {
	deny := func(reason string) (*writeMapping, error) {
		return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: reason}
	}
	if l.sql != "" {
		return deny(fmt.Sprintf("layer %q uses custom SQL and is read-only", l.name))
	}
	if l.tablename == "" {
		return deny(fmt.Sprintf("layer %q has no table mapping", l.name))
	}
	if l.isMapplGIS {
		return deny(fmt.Sprintf("layer %q is a MapplGIS system table", l.name))
	}
	format := l.geometryFormat
	if format == "" {
		format = "gpkg"
	}
	if format == "mos" {
		return deny(fmt.Sprintf("layer %q uses MOS encoding (write not admitted)", l.name))
	}
	if format != "gpkg" && format != "wkb" && format != "wkt" {
		return deny(fmt.Sprintf("layer %q has unsupported geometry format %q", l.name, format))
	}
	db, err := sql.Open(featureSQLiteDriver, sqliteWritableDSN(filepath))
	if err != nil {
		return nil, fmt.Errorf("write admission: open: %w", err)
	}
	defer func() { _ = db.Close() }()

	cols, pkCols, err := tableColumnsAndPK(db, l.tablename)
	if err != nil {
		return deny(fmt.Sprintf("layer %q: %v", l.name, err))
	}
	// Starting profile: single INTEGER PRIMARY KEY (rowid alias).
	if len(pkCols) != 1 {
		return deny(fmt.Sprintf("layer %q: write requires a single-column primary key", l.name))
	}
	if err := checkIntegerPK(db, l.tablename, pkCols[0]); err != nil {
		return nil, err
	}
	colDesc, err := describeColumns(db, l.tablename, cols)
	if err != nil {
		return nil, err
	}
	geomCol := l.geomFieldname
	if _, ok := colDesc[geomCol]; !ok {
		return deny(fmt.Sprintf("layer %q: geometry column %q not found", l.name, geomCol))
	}
	geomType := normalizeGeomType(l.geomType)
	m := &writeMapping{
		layer:      l,
		table:      l.tablename,
		idColumn:   pkCols[0],
		geomColumn: geomCol,
		geomFormat: format,
		geomType:   geomType,
		geomSRID:   l.srid,
		columns:    colDesc,
		writable:   make(map[string]string),
		domain:     "gpkg:" + filepath,
	}
	// Public writable properties: every non-PK, non-geometry column.
	// Bounds backing columns are excluded (derived data).
	boundsCols := map[string]bool{}
	if l.boundFieldnames != nil {
		for _, b := range l.boundFieldnames {
			boundsCols[b] = true
		}
	}
	// Public writable properties: every non-PK, non-geometry column.
	// Bounds backing columns are excluded (derived data). Iterate the
	// table_xinfo descriptors (not table_info) so generated columns are
	// seen and forced read-only.
	for c, d := range colDesc {
		if c == m.idColumn || c == m.geomColumn || boundsCols[c] {
			continue
		}
		if d.IsGenerated {
			m.readOnly = append(m.readOnly, c)
			continue
		}
		m.writable[c] = c
	}
	m.readOnly = append(m.readOnly, m.idColumn)
	return m, nil
}

func checkIntegerPK(db *sql.DB, table, pk string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%v);", quoteIdent(table)))
	if err != nil {
		return &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: fmt.Sprintf("table %q: %v", table, err)}
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pkPos int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pkPos); err != nil {
			return err
		}
		if name == pk {
			// Rowid alias rule: declared type exactly INTEGER, sole PK.
			if strings.ToUpper(strings.TrimSpace(ctype)) != "INTEGER" {
				return &provider.MutationError{
					Kind:   provider.MutationErrUnsupportedCapability,
					Reason: fmt.Sprintf("table %q: write requires INTEGER PRIMARY KEY, got %q", table, ctype),
				}
			}
			return nil
		}
	}
	return &provider.MutationError{Kind: provider.MutationErrNotFound, Reason: fmt.Sprintf("table %q: pk column %q not found", table, pk)}
}

func describeColumns(db *sql.DB, table string, cols []string) (map[string]provider.ColumnDescriptor, error) {
	out := make(map[string]provider.ColumnDescriptor, len(cols))
	// table_xinfo exposes hidden generated columns (hidden=2 virtual,
	// hidden=3 stored) that table_info hides. Generated columns are
	// always read-only: INSERT/UPDATE must reject them.
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_xinfo(%v);", quoteIdent(table)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk, hidden int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk, &hidden); err != nil {
			return nil, err
		}
		out[name] = provider.ColumnDescriptor{
			Name:        name,
			Type:        ctype,
			Nullable:    notNull == 0,
			IsDefault:   dflt.Valid,
			IsGenerated: hidden >= 2,
		}
	}
	return out, rows.Err()
}

func normalizeGeomType(g geom.Geometry) string {
	switch g.(type) {
	case geom.Point:
		return "point"
	case geom.LineString:
		return "linestring"
	case geom.Polygon:
		return "polygon"
	case geom.MultiPoint:
		return "multipoint"
	case geom.MultiLineString:
		return "multilinestring"
	case geom.MultiPolygon:
		return "multipolygon"
	default:
		return "geometry"
	}
}

// BeginFeatureTx implements provider.MutationProvider.
func (w *Writer) BeginFeatureTx(ctx context.Context, options provider.TxOptions) (provider.FeatureTx, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.db == nil {
		db, err := sql.Open(featureSQLiteDriver, sqliteWritableDSN(w.provider.Filepath))
		if err != nil {
			return nil, fmt.Errorf("begin tx: open: %w", err)
		}
		db.SetMaxOpenConns(1)
		w.db = db
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	return &featureTx{writer: w, tx: tx}, nil
}

// featureTx is one native SQLite transaction.
type featureTx struct {
	writer *Writer
	tx     *sql.Tx
	done   bool
	// modified tracks tables touched by this transaction for GeoPackage
	// metadata maintenance (gpkg_contents.last_change, RTree) at commit.
	modified map[string]*tableModification
}

// tableModification records row-level changes for one table.
type tableModification struct {
	table   string
	geomCol string
	// geomChanged maps row id -> new bounds; nil bounds means the row was
	// deleted (drop the RTree entry).
	geomChanged map[uint64]*[4]float64
}

func (t *featureTx) mapping(layer string) (*writeMapping, error) {
	return t.writer.mapping(context.Background(), layer)
}

// Apply implements provider.FeatureTx.
func (t *featureTx) Apply(ctx context.Context, m provider.Mutation) (provider.MutationOutcome, error) {
	if t.done {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "transaction already finished"}
	}
	mp, err := t.mapping(m.Collection)
	if err != nil {
		return provider.MutationOutcome{}, err
	}
	switch m.Op {
	case provider.MutationInsert:
		return t.insert(ctx, mp, m)
	case provider.MutationReplace:
		return t.replace(ctx, mp, m)
	case provider.MutationUpdate:
		return t.update(ctx, mp, m)
	case provider.MutationDelete:
		return t.delete(ctx, mp, m)
	default:
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "unknown op"}
	}
}

func (t *featureTx) Commit(ctx context.Context) (provider.CommitReceipt, error) {
	if t.done {
		return provider.CommitReceipt{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "transaction already finished"}
	}
	t.done = true
	if err := t.maintainGPKGMetadata(ctx); err != nil {
		_ = t.tx.Rollback()
		return provider.CommitReceipt{Status: provider.CommitUnknown}, &provider.MutationError{
			Kind:   provider.MutationErrCommitUnknown,
			Reason: fmt.Sprintf("gpkg metadata maintenance failed: %v", err),
		}
	}
	if err := t.tx.Commit(); err != nil {
		return provider.CommitReceipt{Status: provider.CommitUnknown}, &provider.MutationError{
			Kind:   provider.MutationErrCommitUnknown,
			Reason: fmt.Sprintf("commit failed: %v", err),
		}
	}
	return provider.CommitReceipt{Status: provider.CommitCommitted}, nil
}

// maintainGPKGMetadata updates GeoPackage bookkeeping for tables touched
// by this transaction: gpkg_contents.last_change and the RTree spatial
// index (when present). Runs inside the transaction, before commit.
func (t *featureTx) maintainGPKGMetadata(ctx context.Context) error {
	for _, mod := range t.modified {
		if err := t.touchContents(ctx, mod.table); err != nil {
			return err
		}
		if err := t.maintainRTree(ctx, mod); err != nil {
			return err
		}
	}
	return nil
}

// touchContents bumps gpkg_contents.last_change for a feature table.
// Tables without a gpkg_contents row (plain SQLite tables) are skipped.
func (t *featureTx) touchContents(ctx context.Context, table string) error {
	var n int
	if err := t.tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM gpkg_contents WHERE table_name = ?`, table).Scan(&n); err != nil {
		// No gpkg_contents table at all: plain SQLite, nothing to do.
		return nil
	}
	if n == 0 {
		return nil
	}
	_, err := t.tx.ExecContext(ctx,
		`UPDATE gpkg_contents SET last_change = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE table_name = ?`, table)
	return err
}

// maintainRTree syncs the RTree index for changed rows. Tables without an
// RTree index table are skipped. Row-level triggers (created by
// GeoPackage writers) would already have fired; this covers the
// trigger-less case by reconciling entries for the touched rows.
func (t *featureTx) maintainRTree(ctx context.Context, mod *tableModification) error {
	rtree := "rtree_" + mod.table + "_" + mod.geomCol
	var name string
	if err := t.tx.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, rtree).Scan(&name); err != nil {
		return nil // no RTree index: nothing to do
	}
	for id, bounds := range mod.geomChanged {
		if _, err := t.tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, quoteIdent(rtree)), id); err != nil {
			return err
		}
		if bounds == nil {
			continue // deleted row: entry removed above
		}
		if _, err := t.tx.ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO %s (id, minx, maxx, miny, maxy) VALUES (?, ?, ?, ?, ?)`, quoteIdent(rtree)),
			id, bounds[0], bounds[1], bounds[2], bounds[3]); err != nil {
			return err
		}
	}
	return nil
}

// recordMod marks a table as touched by this transaction.
func (t *featureTx) recordMod(mp *writeMapping, id uint64, bounds *[4]float64, geomTouched bool) {
	if t.modified == nil {
		t.modified = make(map[string]*tableModification)
	}
	mod, ok := t.modified[mp.table]
	if !ok {
		mod = &tableModification{table: mp.table, geomCol: mp.geomColumn, geomChanged: make(map[uint64]*[4]float64)}
		t.modified[mp.table] = mod
	}
	if geomTouched {
		mod.geomChanged[id] = bounds
	}
}

// geometryBounds computes the 2D envelope [minx, maxx, miny, maxy] of a
// decoded geometry for RTree maintenance.
func geometryBounds(g geom.Geometry) ([4]float64, error) {
	var b [4]float64
	pts, err := geom.GetCoordinates(g)
	if err != nil {
		return b, err
	}
	if len(pts) == 0 {
		return b, fmt.Errorf("empty geometry has no bounds")
	}
	b = [4]float64{pts[0][0], pts[0][0], pts[0][1], pts[0][1]}
	for _, p := range pts[1:] {
		if p[0] < b[0] {
			b[0] = p[0]
		}
		if p[0] > b[1] {
			b[1] = p[0]
		}
		if p[1] < b[2] {
			b[2] = p[1]
		}
		if p[1] > b[3] {
			b[3] = p[1]
		}
	}
	return b, nil
}

func (t *featureTx) Rollback(ctx context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	if err := t.tx.Rollback(); err != nil {
		log.Logger().Error("wfs tx rollback failed", "error", err)
		return err
	}
	return nil
}

// selectRow reads the current stored row inside the transaction for
// existence, revision checks and post-image validation.
func (t *featureTx) selectRow(ctx context.Context, mp *writeMapping, id uint64) (map[string]interface{}, bool, error) {
	cols := []string{quoteIdent(mp.idColumn), quoteIdent(mp.geomColumn)}
	for pub := range mp.writable {
		cols = append(cols, quoteIdent(mp.writable[pub]))
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", strings.Join(cols, ", "), quoteIdent(mp.table), quoteIdent(mp.idColumn))
	rows, err := t.tx.QueryContext(ctx, q, id)
	if err != nil {
		return nil, false, fmt.Errorf("select row: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, false, nil
	}
	names := append([]string{mp.idColumn, mp.geomColumn}, writablePublicNames(mp)...)
	vals := make([]interface{}, len(names))
	ptrs := make([]interface{}, len(names))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, false, fmt.Errorf("scan row: %w", err)
	}
	out := make(map[string]interface{}, len(names))
	for i, n := range names {
		out[n] = vals[i]
	}
	return out, true, rows.Err()
}

func writablePublicNames(mp *writeMapping) []string {
	names := make([]string, 0, len(mp.writable))
	for pub := range mp.writable {
		names = append(names, pub)
	}
	return names
}
