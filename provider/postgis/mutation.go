package postgis

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/alexeydott/tegola/mos"
	"github.com/alexeydott/tegola/provider"
	"github.com/jackc/pgx/v5"
)

// Writer implements provider.MutationProvider for PostGIS.
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

func (p *Provider) writer() *Writer {
	// Provider has no writerMu; guard with a package-level approach via pool.
	// Use a simple mutex on Writer creation (idempotent).
	return &Writer{provider: p, mappings: make(map[string]*writeMapping)}
}

type writeMapping struct {
	schema     string
	table      string
	idColumn   string
	geomColumn string
	geomFormat string // mos, wkb, wkt, "" (postgis native)
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
	m, err := admitLayer(ctx, w.provider, &l)
	if err != nil {
		return nil, err
	}
	w.mappings[layer] = m
	return m, nil
}

func (w *Writer) BeginFeatureTx(ctx context.Context, options provider.TxOptions) (provider.FeatureTx, error) {
	tx, err := w.provider.pool.Begin(ctx)
	if err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("begin: %v", err)}
	}
	return &featureTx{writer: w, tx: tx}, nil
}

func deny(reason string) (*writeMapping, error) {
	return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: reason}
}

func admitLayer(ctx context.Context, p *Provider, l *Layer) (*writeMapping, error) {
	if l.sql != "" {
		return deny(fmt.Sprintf("layer %q uses custom SQL and is read-only", l.name))
	}
	// Table name may be schema-qualified.
	schema, table := "public", ""
	parts := strings.Split(l.name, ".")
	_ = parts
	// Layer doesn't carry tablename directly in this struct; use name lookup.
	// PostGIS layers are keyed by name; tablename comes from config.
	// We inspect via information_schema.
	table = l.name
	if i := strings.LastIndex(table, "."); i >= 0 {
		schema, table = table[:i], table[i+1:]
	}
	q := `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`
	rows, err := p.pool.Query(ctx, q, schema, table)
	if err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect: %v", err)}
	}
	defer rows.Close()
	cols := map[string]provider.ColumnDescriptor{}
	for rows.Next() {
		var name, dtype string
		if err := rows.Scan(&name, &dtype); err != nil {
			return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect: %v", err)}
		}
		cols[name] = provider.ColumnDescriptor{Name: name, Type: dtype}
	}
	if err := rows.Err(); err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect: %v", err)}
	}
	// PK via pg_constraint.
	var pkCol string
	err = p.pool.QueryRow(ctx, `
		SELECT a.attname FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary`, schema+"."+table).Scan(&pkCol)
	if err != nil {
		return deny(fmt.Sprintf("layer %q: no single-column primary key found", l.name))
	}
	// Integer PK check.
	var pkType string
	err = p.pool.QueryRow(ctx, `SELECT data_type FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 AND column_name=$3`,
		schema, table, pkCol).Scan(&pkType)
	if err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("inspect pk: %v", err)}
	}
	pt := strings.ToLower(pkType)
	isInt := pt == "integer" || pt == "bigint" || pt == "smallint" || strings.HasPrefix(pt, "int")
	if !isInt {
		return deny(fmt.Sprintf("layer %q: primary key %q is not integer", l.name, pkCol))
	}
	geomCol := l.geomField
	if geomCol == "" {
		geomCol = "geom"
	}
	if _, ok := cols[geomCol]; !ok {
		return deny(fmt.Sprintf("layer %q: geometry column %q not found", l.name, geomCol))
	}
	idCol := l.idField
	if idCol == "" {
		idCol = "gid"
	}
	if idCol != pkCol {
		// id_fieldname should match the PK; warn via deny if mismatch.
		return deny(fmt.Sprintf("layer %q: id field %q is not the primary key %q", l.name, idCol, pkCol))
	}
	format := l.geometryFormat
	if format == "" {
		format = "postgis"
	}
	m := &writeMapping{
		schema:     schema,
		table:      table,
		idColumn:   pkCol,
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
		domain:   "postgis:" + schema + "." + table,
	}
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
	s := strings.ToLower(fmt.Sprintf("%T", g))
	for _, t := range []string{"multipolygon", "multilinestring", "multipoint", "polygon", "linestring", "point"} {
		if strings.Contains(s, t) {
			return t
		}
	}
	return ""
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

type featureTx struct {
	writer *Writer
	tx     pgx.Tx
}

func (t *featureTx) Apply(ctx context.Context, m provider.Mutation) (provider.MutationOutcome, error) {
	mp, err := t.writer.mapping(ctx, m.Collection)
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
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: "unknown op"}
	}
}

func (t *featureTx) Commit(ctx context.Context) (provider.CommitReceipt, error) {
	if err := t.tx.Commit(ctx); err != nil {
		return provider.CommitReceipt{Status: provider.CommitUnknown}, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("commit: %v", err)}
	}
	return provider.CommitReceipt{Status: provider.CommitCommitted}, nil
}

func (t *featureTx) Rollback(ctx context.Context) error {
	_ = t.tx.Rollback(ctx)
	return nil
}
