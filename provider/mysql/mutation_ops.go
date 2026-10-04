package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/geom/encoding/wkb"
	"github.com/alexeydott/geom/encoding/wkt"
	"github.com/alexeydott/proj"
	"github.com/alexeydott/tegola/basic"
	"github.com/alexeydott/tegola/mos"
	"github.com/alexeydott/tegola/provider"
	pa "github.com/alexeydott/tegola/provider/audit"
)

type featureTx struct {
	writer *Writer
	tx     *sql.Tx
	actor  string
	reqID  string
}

func (t *featureTx) Apply(ctx context.Context, m provider.Mutation) (provider.MutationOutcome, error) {
	mp, err := t.writer.mapping(ctx, m.Collection)
	if err != nil {
		return provider.MutationOutcome{}, err
	}
	var outcome provider.MutationOutcome
	switch m.Op {
	case provider.MutationInsert:
		outcome, err = t.insert(ctx, mp, m)
	case provider.MutationReplace:
		outcome, err = t.replace(ctx, mp, m)
	case provider.MutationUpdate:
		outcome, err = t.update(ctx, mp, m)
	case provider.MutationDelete:
		outcome, err = t.delete(ctx, mp, m)
	default:
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: fmt.Sprintf("unknown op %v", m.Op)}
	}
	if err != nil {
		return provider.MutationOutcome{}, err
	}
	// W13: audit in same transaction
	if aerr := t.recordAudit(ctx, m, outcome); aerr != nil {
		return provider.MutationOutcome{}, aerr
	}
	return outcome, nil
}

// recordAudit writes W13 audit/outbox entries in the transaction.
func (t *featureTx) recordAudit(ctx context.Context, m provider.Mutation, outcome provider.MutationOutcome) error {
	return pa.RecordTx(ctx, t.tx, m.Collection, m.Op, outcome, t.actor, t.reqID, "")
}

func (t *featureTx) Commit(ctx context.Context) (provider.CommitReceipt, error) {
	if err := t.tx.Commit(); err != nil {
		return provider.CommitReceipt{Status: provider.CommitUnknown}, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("commit: %v", err)}
	}
	return provider.CommitReceipt{Status: provider.CommitCommitted}, nil
}

func (t *featureTx) Rollback(ctx context.Context) error {
	_ = t.tx.Rollback()
	return nil
}

// encodeStorageGeometry validates the input WKB and encodes to the layer's
// storage format: mos blob, wkb bytes, wkt text, or MySQL/MariaDB native.
func encodeStorageGeometry(mp *writeMapping, wkbBytes []byte, inputSRID uint64) (enc []byte, encStr string, err error) {
	g, err := wkb.DecodeBytes(wkbBytes)
	if err != nil {
		return nil, "", &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("invalid WKB: %v", err)}
	}
	if err := checkGeometryType(g, mp.geomType); err != nil {
		return nil, "", err
	}
	srid := inputSRID
	if srid == 0 {
		srid = mp.geomSRID
	}
	if srid != mp.geomSRID {
		g, err = transformGeometry(g, srid, mp.geomSRID)
		if err != nil {
			return nil, "", err
		}
	}
	rawWKB, err := wkb.EncodeBytes(g)
	if err != nil {
		return nil, "", &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("WKB encode: %v", err)}
	}
	switch mp.geomFormat {
	case "mos":
		blob, err := mos.Encode(g, mp.mosOpts)
		if err != nil {
			return nil, "", &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("MOS encode: %v", err)}
		}
		return blob, "", nil
	case "wkb":
		return rawWKB, "", nil
	case "wkt":
		var sb strings.Builder
		if err := wkt.Encode(&sb, g); err != nil {
			return nil, "", &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("WKT encode: %v", err)}
		}
		return nil, sb.String(), nil
	case "mariadb", "mysql", "auto":
		// Native server-side construction via ST_GeomFromText: the Go
		// driver binds []byte as MYSQL_TYPE_STRING, which the GEOMETRY
		// column rejects (Error 1416). WKT text avoids the issue entirely
		// and lets the server assign the SRID.
		var sb strings.Builder
		if err := wkt.Encode(&sb, g); err != nil {
			return nil, "", &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("WKT encode: %v", err)}
		}
		return nil, fmt.Sprintf("ST_GeomFromText(%q,%d)", sb.String(), mp.geomSRID), nil
	default:
		return nil, "", &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: fmt.Sprintf("unsupported geometry format %q", mp.geomFormat)}
	}
}

func checkGeometryType(g geom.Geometry, want string) error {
	if want == "" {
		return nil
	}
	got := normalizeGeomType(g)
	if got != want {
		// Allow multi/single mismatch only if exact? Strict: reject.
		return &provider.MutationError{Kind: provider.MutationErrSchemaViolation, Reason: fmt.Sprintf("geometry type %q does not match layer type %q", got, want)}
	}
	return nil
}

func transformGeometry(g geom.Geometry, from, to uint64) (geom.Geometry, error) {
	if from == to {
		return g, nil
	}
	if (from == 4326 && to == 3857) || (from == 3857 && to == 4326) {
		return transform4326_3857(g, from == 4326)
	}
	// Custom proj4 CRS (e.g. etmerc) registered under a synthetic SRID:
	// use the vendored proj engine directly.
	if from == 4326 && basic.IsSyntheticSRID(to) {
		return transformViaProj(g, proj.EPSGCode(to), true)
	}
	if to == 4326 && basic.IsSyntheticSRID(from) {
		return transformViaProj(g, proj.EPSGCode(from), false)
	}
	return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: fmt.Sprintf("CRS transform %d -> %d not supported", from, to)}
}

// transformViaProj converts between 4326 and a custom proj4 CRS registered
// under a synthetic SRID. forward=true: 4326 -> custom; false: custom -> 4326.
func transformViaProj(g geom.Geometry, code proj.EPSGCode, forward bool) (geom.Geometry, error) {
	xform := func(x, y float64) ([2]float64, error) {
		var out []float64
		var err error
		if forward {
			out, err = proj.Convert(code, []float64{x, y})
		} else {
			out, err = proj.Inverse(code, []float64{x, y})
		}
		if err != nil {
			return [2]float64{}, err
		}
		return [2]float64{out[0], out[1]}, nil
	}
	return mapGeometryPoints(g, xform)
}

func transform4326_3857(g geom.Geometry, forward bool) (geom.Geometry, error) {
	xform := func(x, y float64) ([2]float64, error) {
		var out []float64
		var err error
		if forward {
			out, err = proj.Convert(proj.EPSG3857, []float64{x, y})
		} else {
			out, err = proj.Inverse(proj.EPSG3857, []float64{x, y})
		}
		if err != nil {
			return [2]float64{}, err
		}
		return [2]float64{out[0], out[1]}, nil
	}
	mapPts := func(pts [][2]float64) ([][2]float64, error) {
		out := make([][2]float64, len(pts))
		for i, p := range pts {
			q, err := xform(p[0], p[1])
			if err != nil {
				return nil, err
			}
			out[i] = q
		}
		return out, nil
	}
	switch t := g.(type) {
	case geom.Point:
		q, err := xform(t[0], t[1])
		return geom.Point(q), err
	case geom.LineString:
		pts, err := mapPts(t)
		return geom.LineString(pts), err
	case geom.Polygon:
		out := make(geom.Polygon, len(t))
		for i, r := range t {
			pts, err := mapPts(r)
			if err != nil {
				return nil, err
			}
			out[i] = pts
		}
		return out, nil
	case geom.MultiPoint:
		pts, err := mapPts([][2]float64(t))
		return geom.MultiPoint(pts), err
	case geom.MultiLineString:
		out := make(geom.MultiLineString, len(t))
		for i, l := range t {
			pts, err := mapPts(l)
			if err != nil {
				return nil, err
			}
			out[i] = pts
		}
		return out, nil
	case geom.MultiPolygon:
		out := make(geom.MultiPolygon, len(t))
		for i, p := range t {
			pp := make(geom.Polygon, len(p))
			for j, r := range p {
				pts, err := mapPts(r)
				if err != nil {
					return nil, err
				}
				pp[j] = pts
			}
			out[i] = pp
		}
		return out, nil
	default:
		return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: fmt.Sprintf("cannot reproject %T", g)}
	}
}

func valueToSQL(mv provider.MutationValue) (interface{}, error) {
	if mv.Null {
		return nil, nil
	}
	switch mv.Kind {
	case provider.MutationValueInteger:
		return mv.Integer, nil
	case provider.MutationValueDecimal:
		return mv.Decimal, nil
	case provider.MutationValueString:
		return mv.String, nil
	case provider.MutationValueBoolean:
		if mv.Boolean {
			return 1, nil
		}
		return 0, nil
	default:
		return nil, fmt.Errorf("unsupported value kind %v", mv.Kind)
	}
}

func mapSQLError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "Duplicate entry") {
		return &provider.MutationError{Kind: provider.MutationErrPreconditionFailed, Reason: msg}
	}
	return &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: msg}
}

func (t *featureTx) insert(ctx context.Context, mp *writeMapping, m provider.Mutation) (provider.MutationOutcome, error) {
	var cols, holders []string
	var args []interface{}
	for pub, mv := range m.Properties {
		col, ok := mp.writable[pub]
		if !ok {
			return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrSchemaViolation, Reason: fmt.Sprintf("property %q is not writable", pub)}
		}
		v, err := valueToSQL(mv)
		if err != nil {
			return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: err.Error()}
		}
		cols = append(cols, quoteIdent(col))
		holders = append(holders, "?")
		args = append(args, v)
	}
	if m.GeometryWKB != nil {
		enc, encStr, err := encodeStorageGeometry(mp, m.GeometryWKB, m.GeometrySRID)
		if err != nil {
			return provider.MutationOutcome{}, err
		}
		cols = append(cols, quoteIdent(mp.geomColumn))
		if strings.HasPrefix(encStr, "ST_GeomFromText(") {
			// server-side constructor: inline the expression
			holders = append(holders, encStr)
		} else {
			holders = append(holders, "?")
			if encStr != "" {
				args = append(args, encStr)
			} else {
				args = append(args, enc)
			}
		}
	}
	if len(cols) == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "insert carries no properties or geometry"}
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteIdent(mp.table), strings.Join(cols, ", "), strings.Join(holders, ", "))
	res, err := t.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return provider.MutationOutcome{}, mapSQLError(err)
	}
	id, err := res.LastInsertId()
	if err != nil || id <= 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: "insert did not return a row id"}
	}
	return provider.MutationOutcome{FeatureID: uint64(id), Affected: 1}, nil
}

func (t *featureTx) replace(ctx context.Context, mp *writeMapping, m provider.Mutation) (provider.MutationOutcome, error) {
	// Replace = delete + insert with explicit ID.
	if _, err := t.delete(ctx, mp, m); err != nil {
		return provider.MutationOutcome{}, err
	}
	m2 := m
	m2.Op = provider.MutationInsert
	// Insert with explicit ID: add ID column.
	var cols, holders []string
	var args []interface{}
	cols = append(cols, quoteIdent(mp.idColumn))
	holders = append(holders, "?")
	args = append(args, m.FeatureID)
	for pub, mv := range m.Properties {
		col, ok := mp.writable[pub]
		if !ok {
			return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrSchemaViolation, Reason: fmt.Sprintf("property %q is not writable", pub)}
		}
		v, err := valueToSQL(mv)
		if err != nil {
			return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: err.Error()}
		}
		cols = append(cols, quoteIdent(col))
		holders = append(holders, "?")
		args = append(args, v)
	}
	if m.GeometryWKB != nil {
		enc, encStr, err := encodeStorageGeometry(mp, m.GeometryWKB, m.GeometrySRID)
		if err != nil {
			return provider.MutationOutcome{}, err
		}
		cols = append(cols, quoteIdent(mp.geomColumn))
		holders = append(holders, "?")
		if encStr != "" {
			args = append(args, encStr)
		} else {
			args = append(args, enc)
		}
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteIdent(mp.table), strings.Join(cols, ", "), strings.Join(holders, ", "))
	if _, err := t.tx.ExecContext(ctx, q, args...); err != nil {
		return provider.MutationOutcome{}, mapSQLError(err)
	}
	return provider.MutationOutcome{FeatureID: m.FeatureID, Affected: 1}, nil
}

func (t *featureTx) update(ctx context.Context, mp *writeMapping, m provider.Mutation) (provider.MutationOutcome, error) {
	var sets []string
	var args []interface{}
	for pub, mv := range m.Properties {
		col, ok := mp.writable[pub]
		if !ok {
			return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrSchemaViolation, Reason: fmt.Sprintf("property %q is not writable", pub)}
		}
		v, err := valueToSQL(mv)
		if err != nil {
			return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: err.Error()}
		}
		sets = append(sets, quoteIdent(col)+" = ?")
		args = append(args, v)
	}
	if m.GeometryWKB != nil {
		enc, encStr, err := encodeStorageGeometry(mp, m.GeometryWKB, m.GeometrySRID)
		if err != nil {
			return provider.MutationOutcome{}, err
		}
		sets = append(sets, quoteIdent(mp.geomColumn)+" = ?")
		if encStr != "" {
			args = append(args, encStr)
		} else {
			args = append(args, enc)
		}
	}
	if len(sets) == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "update carries no changes"}
	}
	args = append(args, m.FeatureID)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?", quoteIdent(mp.table), strings.Join(sets, ", "), quoteIdent(mp.idColumn))
	res, err := t.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return provider.MutationOutcome{}, mapSQLError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrNotFound, Reason: fmt.Sprintf("feature %d not found", m.FeatureID)}
	}
	return provider.MutationOutcome{FeatureID: m.FeatureID, Affected: int(n)}, nil
}

func (t *featureTx) delete(ctx context.Context, mp *writeMapping, m provider.Mutation) (provider.MutationOutcome, error) {
	q := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", quoteIdent(mp.table), quoteIdent(mp.idColumn))
	res, err := t.tx.ExecContext(ctx, q, m.FeatureID)
	if err != nil {
		return provider.MutationOutcome{}, mapSQLError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrNotFound, Reason: fmt.Sprintf("feature %d not found", m.FeatureID)}
	}
	return provider.MutationOutcome{FeatureID: m.FeatureID, Affected: int(n)}, nil
}

// mapGeometryPoints applies xform to every vertex of g, preserving structure.
func mapGeometryPoints(g geom.Geometry, xform func(x, y float64) ([2]float64, error)) (geom.Geometry, error) {
	mapPts := func(pts [][2]float64) ([][2]float64, error) {
		out := make([][2]float64, len(pts))
		for i, p := range pts {
			q, err := xform(p[0], p[1])
			if err != nil {
				return nil, err
			}
			out[i] = q
		}
		return out, nil
	}
	switch t := g.(type) {
	case geom.Point:
		q, err := xform(t[0], t[1])
		if err != nil {
			return nil, err
		}
		return geom.Point(q), nil
	case geom.MultiPoint:
		pts, err := mapPts([][2]float64(t))
		if err != nil {
			return nil, err
		}
		return geom.MultiPoint(pts), nil
	case geom.LineString:
		pts, err := mapPts([][2]float64(t))
		if err != nil {
			return nil, err
		}
		return geom.LineString(pts), nil
	case geom.MultiLineString:
		out := make(geom.MultiLineString, len(t))
		for i, ls := range t {
			pts, err := mapPts(ls)
			if err != nil {
				return nil, err
			}
			out[i] = pts
		}
		return out, nil
	case geom.Polygon:
		rings := t.LinearRings()
		out := make(geom.Polygon, len(rings))
		for i, lr := range rings {
			pts, err := mapPts(lr)
			if err != nil {
				return nil, err
			}
			out[i] = pts
		}
		return out, nil
	case geom.MultiPolygon:
		out := make(geom.MultiPolygon, len(t))
		for i, poly := range t {
			rings := geom.Polygon(poly).LinearRings()
			p2 := make(geom.Polygon, len(rings))
			for j, lr := range rings {
				pts, err := mapPts(lr)
				if err != nil {
					return nil, err
				}
				p2[j] = pts
			}
			out[i] = p2
		}
		return out, nil
	}
	return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: fmt.Sprintf("cannot reproject %T", g)}
}
