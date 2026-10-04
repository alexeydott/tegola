package postgis

import (
	"context"
	"fmt"
	"strings"

	"github.com/alexeydott/geom"
	"github.com/alexeydott/geom/encoding/wkb"
	"github.com/alexeydott/geom/encoding/wkt"
	"github.com/alexeydott/proj"
	"github.com/alexeydott/tegola/mos"
	"github.com/alexeydott/tegola/provider"
)

// ph returns the $n placeholder for arg index i (1-based).
func ph(i int) string { return fmt.Sprintf("$%d", i) }

func encodeStorageGeometry(mp *writeMapping, wkbBytes []byte, inputSRID uint64) (enc []byte, encStr string, useSTGeom bool, err error) {
	g, err := wkb.DecodeBytes(wkbBytes)
	if err != nil {
		return nil, "", false, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("invalid WKB: %v", err)}
	}
	if err := checkGeometryType(g, mp.geomType); err != nil {
		return nil, "", false, err
	}
	srid := inputSRID
	if srid == 0 {
		srid = mp.geomSRID
	}
	if srid != mp.geomSRID {
		g, err = transformGeometry(g, srid, mp.geomSRID)
		if err != nil {
			return nil, "", false, err
		}
	}
	rawWKB, err := wkb.EncodeBytes(g)
	if err != nil {
		return nil, "", false, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("WKB encode: %v", err)}
	}
	switch mp.geomFormat {
	case "mos":
		blob, err := mos.Encode(g, mp.mosOpts)
		if err != nil {
			return nil, "", false, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("MOS encode: %v", err)}
		}
		return blob, "", false, nil
	case "wkb":
		return rawWKB, "", false, nil
	case "wkt":
		var sb strings.Builder
		if err := wkt.Encode(&sb, g); err != nil {
			return nil, "", false, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: fmt.Sprintf("WKT encode: %v", err)}
		}
		return nil, sb.String(), false, nil
	default: // "postgis" native
		return rawWKB, "", true, nil
	}
}

func checkGeometryType(g geom.Geometry, want string) error {
	if want == "" {
		return nil
	}
	if normalizeGeomType(g) != want {
		return &provider.MutationError{Kind: provider.MutationErrSchemaViolation, Reason: fmt.Sprintf("geometry type mismatch: layer wants %q", want)}
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
	return nil, &provider.MutationError{Kind: provider.MutationErrUnsupportedCapability, Reason: fmt.Sprintf("CRS transform %d -> %d not supported", from, to)}
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
		return mv.Boolean, nil
	default:
		return nil, fmt.Errorf("unsupported value kind %v", mv.Kind)
	}
}

func (t *featureTx) geomArg(mp *writeMapping, m provider.Mutation, args []interface{}) ([]string, []interface{}, error) {
	enc, encStr, useST, err := encodeStorageGeometry(mp, m.GeometryWKB, m.GeometrySRID)
	if err != nil {
		return nil, nil, err
	}
	if encStr != "" {
		return []string{ph(len(args) + 1)}, append(args, encStr), nil
	}
	if useST {
		// ST_SetSRID(ST_GeomFromWKB($n), srid)
		return []string{fmt.Sprintf("ST_SetSRID(ST_GeomFromWKB(%s), %d)", ph(len(args)+1), mp.geomSRID)}, append(args, enc), nil
	}
	return []string{ph(len(args) + 1)}, append(args, enc), nil
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
		holders = append(holders, ph(len(args)+1))
		args = append(args, v)
	}
	if m.GeometryWKB != nil {
		h, a, err := t.geomArg(mp, m, args)
		if err != nil {
			return provider.MutationOutcome{}, err
		}
		cols = append(cols, quoteIdent(mp.geomColumn))
		holders = append(holders, h[0])
		args = a
	}
	if len(cols) == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "insert carries no properties or geometry"}
	}
	tbl := quoteIdent(mp.schema) + "." + quoteIdent(mp.table)
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s", tbl, strings.Join(cols, ", "), strings.Join(holders, ", "), quoteIdent(mp.idColumn))
	var id uint64
	if err := t.tx.QueryRow(ctx, q, args...).Scan(&id); err != nil {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("insert: %v", err)}
	}
	return provider.MutationOutcome{FeatureID: id, Affected: 1}, nil
}

func (t *featureTx) replace(ctx context.Context, mp *writeMapping, m provider.Mutation) (provider.MutationOutcome, error) {
	if _, err := t.delete(ctx, mp, m); err != nil {
		return provider.MutationOutcome{}, err
	}
	var cols, holders []string
	var args []interface{}
	cols = append(cols, quoteIdent(mp.idColumn))
	holders = append(holders, ph(1))
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
		holders = append(holders, ph(len(args)+1))
		args = append(args, v)
	}
	if m.GeometryWKB != nil {
		h, a, err := t.geomArg(mp, m, args)
		if err != nil {
			return provider.MutationOutcome{}, err
		}
		cols = append(cols, quoteIdent(mp.geomColumn))
		holders = append(holders, h[0])
		args = a
	}
	tbl := quoteIdent(mp.schema) + "." + quoteIdent(mp.table)
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tbl, strings.Join(cols, ", "), strings.Join(holders, ", "))
	if _, err := t.tx.Exec(ctx, q, args...); err != nil {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("replace: %v", err)}
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
		sets = append(sets, quoteIdent(col)+" = "+ph(len(args)+1))
		args = append(args, v)
	}
	if m.GeometryWKB != nil {
		h, a, err := t.geomArg(mp, m, args)
		if err != nil {
			return provider.MutationOutcome{}, err
		}
		sets = append(sets, quoteIdent(mp.geomColumn)+" = "+h[0])
		args = a
	}
	if len(sets) == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "update carries no changes"}
	}
	tbl := quoteIdent(mp.schema) + "." + quoteIdent(mp.table)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s", tbl, strings.Join(sets, ", "), quoteIdent(mp.idColumn), ph(len(args)+1))
	args = append(args, m.FeatureID)
	tag, err := t.tx.Exec(ctx, q, args...)
	if err != nil {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("update: %v", err)}
	}
	if tag.RowsAffected() == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrNotFound, Reason: fmt.Sprintf("feature %d not found", m.FeatureID)}
	}
	return provider.MutationOutcome{FeatureID: m.FeatureID, Affected: int(tag.RowsAffected())}, nil
}

func (t *featureTx) delete(ctx context.Context, mp *writeMapping, m provider.Mutation) (provider.MutationOutcome, error) {
	tbl := quoteIdent(mp.schema) + "." + quoteIdent(mp.table)
	q := fmt.Sprintf("DELETE FROM %s WHERE %s = $1", tbl, quoteIdent(mp.idColumn))
	tag, err := t.tx.Exec(ctx, q, m.FeatureID)
	if err != nil {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrCommitUnknown, Reason: fmt.Sprintf("delete: %v", err)}
	}
	if tag.RowsAffected() == 0 {
		return provider.MutationOutcome{}, &provider.MutationError{Kind: provider.MutationErrNotFound, Reason: fmt.Sprintf("feature %d not found", m.FeatureID)}
	}
	return provider.MutationOutcome{FeatureID: m.FeatureID, Affected: int(tag.RowsAffected())}, nil
}
