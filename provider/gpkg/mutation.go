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
	return &Writer{provider: p, mappings: make(map[string]*writeMapping)}
}

// writeMapping is the admission result for one layer.
type writeMapping struct {
	layer          *Layer
	table          string
	idColumn       string
	geomColumn     string
	geomFormat     string // gpkg, wkb, wkt
	geomType       string // point, linestring, polygon, ...
	geomSRID       uint64
	columns        map[string]provider.ColumnDescriptor // by column name
	writable       map[string]string                   // public name -> column
	readOnly       []string
	domain         string
}

// DescribeWritable implements provider.MutationProvider admission
// (ADR-0013). Only plain tablename layers with a single INTEGER PRIMARY
// KEY, a supported geometry encoding and no custom SQL are admitted.
func (w *Writer) DescribeWritable(ctx context.Context, layer string) (provider.WriteDescriptor, error) {
	m, err := w.mapping(ctx, layer)
	if err != nil {
		return provider.WriteDescriptor{}, err
	}
	wd := provider.WriteDescriptor{
		Layer:          layer,
		Table:          m.table,
		IDColumn:       m.idColumn,
		GeometryColumn: m.geomColumn,
		GeometryType:   m.geomType,
		GeometrySRID:   m.geomSRID,
		WritableColumns: m.writable,
		ReadOnlyColumns: m.readOnly,
		Domain:         m.domain,
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
	for _, c := range cols {
		if c == m.idColumn || c == m.geomColumn || boundsCols[c] {
			continue
		}
		d := colDesc[c]
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
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%v);", quoteIdent(table)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = provider.ColumnDescriptor{
			Name:       name,
			Type:       ctype,
			Nullable:   notNull == 0,
			IsDefault:  dflt.Valid,
			IsGenerated: isGeneratedColumn(ctype),
		}
	}
	return out, rows.Err()
}

func isGeneratedColumn(ctype string) bool {
	// SQLite reports generated columns via a separate pragma in newer
	// versions; declared types never mark generation. Conservative: no
	// column is treated as generated from the type alone.
	return false
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
	if err := t.tx.Commit(); err != nil {
		return provider.CommitReceipt{Status: provider.CommitUnknown}, &provider.MutationError{
			Kind:   provider.MutationErrCommitUnknown,
			Reason: fmt.Sprintf("commit failed: %v", err),
		}
	}
	return provider.CommitReceipt{Status: provider.CommitCommitted}, nil
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
