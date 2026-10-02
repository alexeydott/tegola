//go:build cgo

package gpkg

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexeydott/tegola/provider"
	"github.com/mattn/go-sqlite3"
)

func filterTestProvider(t *testing.T, ddl string, overrides map[string]interface{}) (*Provider, *sql.DB) {
	t.Helper()
	p, db := queryTestProvider(t, ddl, overrides)
	// The legacy cleanup regression counts the registry, not open handles.
	// Remove only this fixture's already-owned provider; never reset other tests.
	t.Cleanup(func() {
		providersMu.Lock()
		defer providersMu.Unlock()
		for i, registered := range providers {
			if registered == p {
				providers = append(providers[:i], providers[i+1:]...)
				break
			}
		}
	})
	return p, db
}

func filterTestExpression(t *testing.T, node provider.FilterNode) *provider.FilterExpression {
	t.Helper()
	expression, err := provider.NewFilterExpression(node)
	if err != nil {
		t.Fatal(err)
	}
	return &expression
}

func filterTestCompare(t *testing.T, name, text string, kind provider.FilterScalarType, operator provider.FilterCompareOperator) provider.FilterNode {
	t.Helper()
	literal, err := provider.NewFilterLiteral(kind, text)
	if err != nil {
		t.Fatal(err)
	}
	return provider.FilterNode{Kind: provider.FilterCompare, Property: name, Operator: operator, Literal: literal}
}

func TestFeatureFilterRealSQLiteExactAndNull(t *testing.T) {
	p, _ := filterTestProvider(t, `CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,n INTEGER,s TEXT,b BOOLEAN);
INSERT INTO items VALUES(10,'POINT (0 0)',NULL,NULL,NULL),(20,'POINT (0 0)',-2,'a',0),(30,'POINT (0 0)',-1,'a ',1),
(40,'POINT (0 0)',9223372036854775807,'🙂',1);`, nil)
	meta, err := p.layers["items"].FeatureQueryables()
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.FeatureQueryable{{Name: "b", Type: provider.QueryableBoolean, Nullable: true}, {Name: "n", Type: provider.QueryableInteger, Nullable: true}, {Name: "s", Type: provider.QueryableString, Nullable: true}}
	if !reflect.DeepEqual(meta.Fields(), want) {
		t.Fatalf("metadata %#v", meta.Fields())
	}
	for _, test := range []struct {
		name string
		node provider.FilterNode
		ids  []uint64
	}{
		{"fraction", filterTestCompare(t, "n", "-1.2", provider.FilterNumber, provider.FilterLess), []uint64{20}},
		{"range", filterTestCompare(t, "n", "9223372036854775807", provider.FilterNumber, provider.FilterEqual), []uint64{40}},
		{"spaces", filterTestCompare(t, "s", "a", provider.FilterString, provider.FilterGreater), []uint64{30, 40}},
		{"boolean", filterTestCompare(t, "b", "true", provider.FilterBoolean, provider.FilterEqual), []uint64{30, 40}},
		{"null", provider.FilterNode{Kind: provider.FilterIsNull, Property: "n"}, []uint64{10}},
		{"unknown_not", provider.FilterNode{Kind: provider.FilterNot, Children: []provider.FilterNode{filterTestCompare(t, "n", "1.1", provider.FilterNumber, provider.FilterEqual)}}, []uint64{20, 30, 40}},
		{"false", provider.FilterNode{Kind: provider.FilterBooleanConstant}, []uint64{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := provider.FeatureQuery{Limit: 10, Filter: filterTestExpression(t, test.node)}
			ids, result := queryIDs(t, p, query)
			if !reflect.DeepEqual(ids, test.ids) || result.NumberMatched == nil || *result.NumberMatched != uint64(len(test.ids)) {
				t.Fatalf("IDs %v want %v result%+v", ids, test.ids, result)
			}
		})
	}
	var tags map[string]any
	_, err = p.QueryFeatures(context.Background(), "items", provider.FeatureQuery{IDs: []uint64{30}, Limit: 1, Filter: filterTestExpression(t, provider.FilterNode{Kind: provider.FilterBooleanConstant, Boolean: true})}, func(feature *provider.Feature) error { tags = feature.Tags; return nil })
	if err != nil || tags["b"] != true {
		t.Fatalf("boolean raw projection %#v %v", tags, err)
	}
	ids, result := queryIDs(t, p, provider.FeatureQuery{Offset: 1, Limit: 1, Filter: filterTestExpression(t, filterTestCompare(t, "n", "-3", provider.FilterNumber, provider.FilterGreater))})
	if !reflect.DeepEqual(ids, []uint64{30}) || !result.HasMore {
		t.Fatalf("filter before page: %v %+v", ids, result)
	}
}

func TestFeatureFilterInvalidDomainCannotBeMasked(t *testing.T) {
	for _, test := range []struct{ name, column, value string }{
		{"integer", "n", "'bad'"}, {"bool_two", "b", "2"}, {"bool_text", "b", "'bad'"},
		{"invalid_utf8", "s", "CAST(x'ff' AS TEXT)"}, {"blob", "s", "x'00'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, db := filterTestProvider(t, `CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,n INTEGER,s TEXT,b BOOLEAN);
INSERT INTO items VALUES(1,'POINT (0 0)',1,'ok',1);`, nil)
			if _, err := db.Exec("UPDATE items SET " + quoteIdent(test.column) + "=" + test.value); err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err := p.QueryFeatures(context.Background(), "items", provider.FeatureQuery{Limit: 1, Filter: filterTestExpression(t, provider.FilterNode{Kind: provider.FilterBooleanConstant})}, func(*provider.Feature) error { calls++; return nil })
			var data provider.FeatureDataError
			if !errors.As(err, &data) || calls != 0 {
				t.Fatalf("bad source masked: %v calls%d", err, calls)
			}
		})
	}
}

func TestFeatureFilterSourceCapAndLazyCallback(t *testing.T) {
	p, db := filterTestProvider(t, `CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,s TEXT); INSERT INTO items VALUES(1,NULL,'a');`, nil)
	query := provider.FeatureQuery{Limit: 1, Filter: filterTestExpression(t, provider.FilterNode{Kind: provider.FilterBooleanConstant, Boolean: true})}
	if _, err := db.Exec("UPDATE items SET s=?", strings.Repeat("x", maxFilterSourceTextBytes)); err != nil {
		t.Fatal(err)
	}
	if ids, _ := queryIDs(t, p, query); !reflect.DeepEqual(ids, []uint64{1}) {
		t.Fatal(ids)
	}
	if _, err := db.Exec("UPDATE items SET s=?", strings.Repeat("x", maxFilterSourceTextBytes+1)); err != nil {
		t.Fatal(err)
	}
	_, err := p.QueryFeatures(context.Background(), "items", query, func(*provider.Feature) error { return nil })
	var data provider.FeatureDataError
	if !errors.As(err, &data) {
		t.Fatalf("oversize not data error %v", err)
	}
	connection, err := p.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	}()
	calls := 0
	if err := connection.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc(featureUTF8Function, func(any) bool { calls++; return true }, false)
	}); err != nil {
		t.Fatal(err)
	}
	// Isolate the SQL boundary to prove wrong-type and oversize values cannot
	// allocate Go arguments or enter even an instrumented callback.
	for _, value := range []any{strings.Repeat("x", maxFilterSourceTextBytes+1), int64(3), []byte{1}, nil} {
		var valid int
		err := connection.QueryRowContext(context.Background(), "SELECT CASE WHEN typeof(?)='text' AND length(CAST(? AS BLOB))<=1048576 THEN "+featureUTF8Function+"(?) ELSE 0 END", value, value, value).Scan(&valid)
		if err != nil || valid != 0 || calls != 0 {
			t.Fatalf("lazy CASE: valid%d calls%d err%v", valid, calls, err)
		}
	}
}

func TestFeatureFilterDriverPoolReadOnlyAndCancellation(t *testing.T) {
	p, _ := filterTestProvider(t, `CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,s TEXT); INSERT INTO items VALUES(1,NULL,'a');`, nil)
	p.db.SetMaxOpenConns(4)
	connections := []*sql.Conn{}
	for i := 0; i < 4; i++ {
		connection, err := p.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		var valid bool
		if err := connection.QueryRowContext(context.Background(), "SELECT "+featureUTF8Function+"(?)", "🙂\x00").Scan(&valid); err != nil || !valid {
			t.Fatalf("pool UDF %v %v", valid, err)
		}
		if _, err := connection.ExecContext(context.Background(), "UPDATE items SET s='changed'"); err == nil {
			t.Fatal("provider lost read-only DSN")
		}
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	query := provider.FeatureQuery{Limit: 1, Filter: filterTestExpression(t, provider.FilterNode{Kind: provider.FilterBooleanConstant, Boolean: true})}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := p.QueryFeatures(context.Background(), "items", query, func(*provider.Feature) error { return nil })
			if err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.QueryFeatures(ctx, "items", query, func(*provider.Feature) error { t.Error("callback after cancel"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = p.QueryFeatures(ctx, "items", query, func(*provider.Feature) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline %v", err)
	}
}

func TestFeatureFilterSchemaDriftAndOptionalMetadata(t *testing.T) {
	p, db := filterTestProvider(t, `CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,n INTEGER); INSERT INTO items VALUES(1,NULL,1);`, nil)
	if _, err := db.Exec("ALTER TABLE items ADD COLUMN other TEXT"); err != nil {
		t.Fatal(err)
	}
	_, err := p.QueryFeatures(context.Background(), "items", provider.FeatureQuery{Limit: 1, Filter: filterTestExpression(t, provider.FilterNode{Kind: provider.FilterBooleanConstant, Boolean: true})}, func(*provider.Feature) error { t.Error("changed schema callback"); return nil })
	var data provider.FeatureDataError
	if !errors.As(err, &data) {
		t.Fatalf("schema change: %v", err)
	}
	// Unsupported scalar declarations do not invalidate Core or advertise types.
	p, _ = filterTestProvider(t, `CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,f REAL,d NUMERIC,at TIMESTAMP); INSERT INTO items VALUES(1,NULL,1,2,NULL);`, nil)
	meta, err := p.layers["items"].FeatureQueryables()
	if err != nil || len(meta.Fields()) != 0 {
		t.Fatalf("unproved metadata: %#v %v", meta.Fields(), err)
	}
	if ids, _ := queryIDs(t, p, provider.FeatureQuery{Limit: 1}); !reflect.DeepEqual(ids, []uint64{1}) {
		t.Fatal(ids)
	}
}

func TestFeatureFilterUTF16AndBudgetPreserveCore(t *testing.T) {
	p, _ := filterTestProvider(t, `PRAGMA encoding='UTF-16'; CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,s TEXT); INSERT INTO items VALUES(1,NULL,'ok');`, nil)
	if _, err := p.layers["items"].FeatureQueryables(); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("UTF-16 queryable profile admitted: %v", err)
	}
	if ids, _ := queryIDs(t, p, provider.FeatureQuery{Limit: 1}); !reflect.DeepEqual(ids, []uint64{1}) {
		t.Fatal("optional profile disabled Core", ids)
	}
	ddl := "CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT"
	for i := 0; i < 130; i++ {
		name := strings.Repeat("x", 512) + string(rune(0x1000+i))
		ddl += "," + quoteIdent(name) + " INTEGER"
	}
	ddl += ")"
	p, _ = filterTestProvider(t, ddl, nil)
	if _, err := p.layers["items"].FeatureQueryables(); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("oversized optional catalog admitted: %v", err)
	}
	if _, err := p.QueryFeatures(context.Background(), "items", provider.FeatureQuery{Limit: 1}, func(*provider.Feature) error { return nil }); err != nil {
		t.Fatalf("catalog budget disabled Core: %v", err)
	}
}

func TestFeatureFilterIntegrityScanCancellation(t *testing.T) {
	p, _ := filterTestProvider(t, `CREATE TABLE items(id INTEGER PRIMARY KEY,geom TEXT,s TEXT);
WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<50000)
INSERT INTO items SELECT x,NULL,'ok' FROM n;`, nil)
	// Own one connection and instrument its fixed scalar only for this test.
	// Cancellation happens while SQL is checking the source domain, not merely
	// before entering QueryFeatures, and the callback remains bounded/pure in production.
	p.db.SetMaxOpenConns(1)
	connection, err := p.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := false
	if err := connection.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc(featureUTF8Function, func(value any) bool {
			if !entered {
				entered = true
				cancel()
			}
			return validFilterUTF8(value)
		}, false)
	}); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = p.QueryFeatures(ctx, "items", provider.FeatureQuery{Limit: 1, Filter: filterTestExpression(t, provider.FilterNode{Kind: provider.FilterBooleanConstant})}, func(*provider.Feature) error {
		t.Error("callback after integrity scan cancellation")
		return nil
	})
	if !entered || !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation entered%v err%v", entered, err)
	}
}
