package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/alexeydott/tegola/mos"
	"github.com/alexeydott/tegola/provider"
)

// Writer implements provider.MutationProvider for MySQL/MariaDB.
type Writer struct {
	provider *Provider
	mu       sync.Mutex
	mappings map[string]*writeMapping
}

func (p *Provider) MutationWriter() provider.MutationProvider {
	return p.writer()
}

func (p *Provider) DescribeWritable(ctx context.Context, layer string) (provider.WriteDescriptor, error) {
	return p.MutationWriter().DescribeWritable(ctx, layer)
}

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

func (p *Provider) writer() *Writer {
	p.writerMu.Lock()
	defer p.writerMu.Unlock()
	if p.cachedWriter == nil {
		p.cachedWriter = &Writer{provider: p, mappings: make(map[string]*writeMapping)}
	}
	return p.cachedWriter
}

type writeMapping struct {
	table      string
	idColumn   string
	geomColumn string
	geomFormat string // mos, wkb, wkt, mysql, mariadb, auto
	geomType   string
	geomSRID   uint64
	mosOpts    mos.Options
	columns    map[string]provider.ColumnDescriptor
	writable   map[string]string
	readOnly   []string
	domain     string
}

func (w *Writer) DescribeWritable(ctx context.Context, layer string) (provider.WriteDescriptor, error) {
	m, err := w.mapping(ctx, layer)
	if err != nil {
		return provider.WriteDescriptor{}, err
	}
	writable := make(map[string]string, len(m.writable))
	for k, v := range m.writable {
		writable[k] = v
	}
	return provider.WriteDescriptor{
		Layer:           layer,
		Table:           m.table,
		IDColumn:        m.idColumn,
		GeometryColumn:  m.geomColumn,
		GeometryType:    m.geomType,
		GeometrySRID:    m.geomSRID,
		WritableColumns: writable,
		ReadOnlyColumns: append([]string(nil), m.readOnly...),
		Domain:          m.domain,
	}, nil
}

func (w *Writer) mapping(ctx context.Context, layer string) (*writeMapping, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if m, ok := w.mappings[layer]; ok {
		return m, nil
	}
	l, ok := w.provider.layers[layer]
	if !ok {
		return nil, &provider.MutationError{Kind: provider.MutationErrNotFound, Reason: fmt.Sprintf("layer %q not found", layer)}
	}
	m, err := admitLayer(ctx, w.provider.db, w.provider.Database, &l)
	if err != nil {
		return nil, err
	}
	w.mappings[layer] = m
	return m, nil
}

func (w *Writer) BeginFeatureTx(ctx context.Context, options provider.TxOptions) (provider.FeatureTx, error) {
	tx, err := w.provider.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("begin: %v", err)}
	}
	// A01: No DDL in data transaction. Audit tables must be created via
	// migration before write traffic (see provider/audit/sql.go).
	// DDL causes implicit COMMIT in MySQL, breaking atomicity.
	return &featureTx{writer: w, tx: tx, actor: options.Actor, reqID: options.RequestID}, nil
}

func deny(reason string) (*writeMapping, error) {
	return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: reason}
}

// admitLayer runs write admission for one MySQL layer.
func admitLayer(ctx context.Context, db *sql.DB, database string, l *Layer) (*writeMapping, error) {
	if l.sql != "" {
		return deny(fmt.Sprintf("layer %q uses custom SQL and is read-only", l.name))
	}
	if l.tablename == "" {
		return deny(fmt.Sprintf("layer %q has no table mapping", l.name))
	}
	// Columns and PK via information_schema.
	q := `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_KEY FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`
	rows, err := db.QueryContext(ctx, q, database, l.tablename)
	if err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect: %v", err)}
	}
	defer rows.Close()
	var pkCols []string
	cols := map[string]provider.ColumnDescriptor{}
	var order []string
	for rows.Next() {
		var name, dtype, key string
		if err := rows.Scan(&name, &dtype, &key); err != nil {
			return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect: %v", err)}
		}
		cols[name] = provider.ColumnDescriptor{Name: name, Type: dtype}
		order = append(order, name)
		if key == "PRI" {
			pkCols = append(pkCols, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect: %v", err)}
	}
	if len(pkCols) != 1 {
		return deny(fmt.Sprintf("layer %q: write requires a single-column primary key", l.name))
	}
	// Integer PK check.
	var pkType string
	err = db.QueryRowContext(ctx, `SELECT DATA_TYPE FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		database, l.tablename, pkCols[0]).Scan(&pkType)
	if err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect pk: %v", err)}
	}
	upper := strings.ToUpper(pkType)
	isInt := false
	for _, t := range []string{"TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT"} {
		if upper == t || strings.HasPrefix(upper, t+"(") {
			isInt = true
			break
		}
	}
	if !isInt {
		return deny(fmt.Sprintf("layer %q: primary key %q is not integer", l.name, pkCols[0]))
	}
	geomCol := l.geomFieldname
	if _, ok := cols[geomCol]; !ok {
		return deny(fmt.Sprintf("layer %q: geometry column %q not found", l.name, geomCol))
	}
	format := l.geometryFormat
	if format == "" || format == "auto" {
		format = l.serverFlavor
		if format == "" {
			format = "mysql"
		}
	}
	m := &writeMapping{
		table:      l.tablename,
		idColumn:   pkCols[0],
		geomColumn: geomCol,
		geomFormat: format,
		geomType:   normalizeGeomType(l.geomType),
		geomSRID:   l.srid,
		mosOpts: mos.Options{
			Precision:  l.mosConfig.Precision,
			UnitFactor: l.mosConfig.UnitFactor,
		},
		columns:  cols,
		writable: make(map[string]string),
		domain:   "mysql:" + database,  // BUG-2 fix: per-database, not per-table
	}
	_ = order
	for name := range cols {
		if name == m.idColumn || name == m.geomColumn {
			continue
		}
		m.writable[name] = name
	}
	return m, nil
}

func normalizeGeomType(g interface{}) string {
	if g == nil {
		return ""
	}
	s := fmt.Sprintf("%T", g)
	s = strings.ToLower(s)
	for _, t := range []string{"multipolygon", "multilinestring", "multipoint", "polygon", "linestring", "point"} {
		if strings.Contains(s, t) {
			return t
		}
	}
	return ""
}

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// splitStmts splits DDL on semicolons.
func splitStmts(ddl string) []string {
	var out []string
	for _, s := range strings.Split(ddl, ";") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
