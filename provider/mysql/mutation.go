package mysql

import (
	"database/sql"
	"errors"
	"context"
	"fmt"
	"math/rand"
	"time"
	"strings"
	"sync"

	"github.com/alexeydott/tegola/mos"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/alexeydott/tegola/provider"
	pa "github.com/alexeydott/tegola/provider/audit"
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

// CurrentRevision implements provider.RevisionReader via the writer (R01).
// Returns "", nil when the writer has no write DB (read-only provider).
func (p *Provider) CurrentRevision(ctx context.Context, layer string, featureID uint64) (string, error) {
	w, ok := p.MutationWriter().(*Writer)
	if !ok || w.provider.db == nil {
		return "", nil
	}
	return w.CurrentRevision(ctx, layer, featureID)
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

// domainID returns an opaque identifier for the physical MySQL/MariaDB
// database this provider instance is connected to.
//
// A02: the domain must identify the physical DB (host+port+dbname), not
// just the database name. Two instances pointing at different servers
// must never share a domain even if db names match.
func (p *Provider) domainID() string {
	sum := sha256.Sum256([]byte(p.Host + "|" + strconv.Itoa(p.Port) + "|" + p.Database))
	return "mysql:" + hex.EncodeToString(sum[:])[:16]
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
	m, err := admitLayer(ctx, w.provider, &l)
	if err != nil {
		return nil, err
	}
	w.mappings[layer] = m
	return m, nil
}

// CurrentRevision implements provider.RevisionReader (A03).
func (w *Writer) CurrentRevision(ctx context.Context, layer string, featureID uint64) (string, error) {
	var rev int64
	err := w.provider.db.QueryRowContext(ctx, `SELECT revision FROM tegola_revisions WHERE collection = ? AND feature_id = ?`, layer, featureID).Scan(&rev)
	if err != nil {
		// R01/R09: distinguish missing row (revision 0) from missing
		// table (revisions not migrated -> "", nil for hash fallback)
		// and real storage errors.
		if errors.Is(err, sql.ErrNoRows) {
			return "0", nil
		}
		if pa.IsMissingTable(err) {
			return "", nil
		}
		return "", err
	}
	return strconv.FormatInt(rev, 10), nil
}

func (w *Writer) BeginFeatureTx(ctx context.Context, options provider.TxOptions) (provider.FeatureTx, error) {
	// R09: ensure service schema BEFORE opening the data transaction.
	// A01: No DDL in data transaction (implicit COMMIT in MySQL).
	// Check version and engine before write traffic.
	if err := pa.CheckSchemaVersion(ctx, w.provider.db, "mysql"); err != nil {
		if merr := pa.Migrate(ctx, w.provider.db, "mysql"); merr != nil {
			return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("schema migration: %v", merr)}
		}
	}
	// A01: verify table engines (InnoDB required for transactions).
	if err := pa.CheckTableEngine(ctx, w.provider.db, "mysql"); err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("schema engine: %v", err)}
	}
	tx, err := w.provider.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("begin: %v", err)}
	}
	// A34: generate a unique txID for audit/outbox correlation.
	txID := fmt.Sprintf("%d-%d", time.Now().UTC().UnixNano(), rand.Int63())
	return &featureTx{writer: w, tx: tx, actor: options.Actor, reqID: options.RequestID, txID: txID}, nil
}

func deny(reason string) (*writeMapping, error) {
	return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: reason}
}

// admitLayer runs write admission for one MySQL layer.
func admitLayer(ctx context.Context, p *Provider, l *Layer) (*writeMapping, error) {
	if l.sql != "" {
		return deny(fmt.Sprintf("layer %q uses custom SQL and is read-only", l.name))
	}
	if l.tablename == "" {
		return deny(fmt.Sprintf("layer %q has no table mapping", l.name))
	}
	// Columns and PK via information_schema.
	q := `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_KEY FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`
	rows, err := p.db.QueryContext(ctx, q, p.Database, l.tablename)
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
	err = p.db.QueryRowContext(ctx, `SELECT DATA_TYPE FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		p.Database, l.tablename, pkCols[0]).Scan(&pkType)
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
		domain:   p.domainID(),
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
