package postgis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/alexeydott/tegola/provider"
	pa "github.com/alexeydott/tegola/provider/audit"
	"github.com/jackc/pgx/v5/pgxpool"
)

func followupPostGIS(t *testing.T) (*Writer, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEGOLA_REVIEW_POSTGIS_DSN")
	if dsn == "" {
		t.Skip("TEGOLA_REVIEW_POSTGIS_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("tegola_followup_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoteIdent(schema)); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = quoteIdent(schema) + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoteIdent(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	for _, statement := range splitStmts(pa.PostgresDDL) {
		if _, err = pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"INSERT INTO tegola_schema_version VALUES(2,NOW())",
		"CREATE TABLE items(id BIGSERIAL PRIMARY KEY,geom BYTEA,name TEXT)",
		"CREATE TABLE identity_items(id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,geom BYTEA,name TEXT)",
		"CREATE TABLE manual_items(id BIGINT PRIMARY KEY,geom BYTEA,name TEXT)",
	} {
		if _, err = pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	p := &Provider{config: *pool.Config(), pool: &connectionPoolCollector{Pool: pool}, layers: map[string]Layer{}}
	for _, table := range []string{"items", "identity_items", "manual_items"} {
		p.layers[table] = Layer{name: table, tablename: schema + "." + table, idField: "id", geomField: "geom", geometryFormat: "wkb", srid: 4326}
	}
	return p.writer(), pool
}

func TestFollowupPostGISCreateAdmission(t *testing.T) {
	w, pool := followupPostGIS(t)
	for _, table := range []string{"items", "identity_items", "manual_items"} {
		wd, err := w.DescribeWritable(context.Background(), table)
		if err != nil {
			t.Fatal(err)
		}
		if (wd.CreateUnsupportedReason != "") != (table == "manual_items") {
			t.Errorf("%s create admission=%q", table, wd.CreateUnsupportedReason)
		}
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "INSERT INTO manual_items(id,name) VALUES(9,'existing')"); err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginFeatureTx(ctx, provider.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Apply(ctx, provider.Mutation{Op: provider.MutationInsert, Collection: "manual_items", Properties: map[string]provider.MutationValue{"name": {Kind: provider.MutationValueString, String: "no id"}}})
	if me, ok := provider.AsMutationError(err); !ok || me.Kind != provider.MutationErrUnsupportedCapability {
		t.Fatalf("manual PK insert admission: %v", err)
	}
	_, err = tx.Apply(ctx, provider.Mutation{Op: provider.MutationUpdate, Collection: "manual_items", FeatureID: 9, IfRevision: "0.0", Properties: map[string]provider.MutationValue{"name": {Kind: provider.MutationValueString, String: "updated"}}})
	if err != nil {
		t.Fatalf("manual PK update should remain supported: %v", err)
	}
	if _, err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFollowupPostGISReceiptCorrelation(t *testing.T) {
	w, pool := followupPostGIS(t)
	ctx := context.Background()
	if _, err := w.DescribeWritable(ctx, "items"); err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		tx, err := w.BeginFeatureTx(ctx, provider.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		native := tx.(*featureTx)
		if _, err = tx.Apply(ctx, provider.Mutation{Op: provider.MutationInsert, Collection: "items", Properties: map[string]provider.MutationValue{"name": {Kind: provider.MutationValueString, String: "audit id"}}}); err != nil {
			t.Fatal(err)
		}
		// A closed native transaction injects a Commit error; this does not
		// simulate an acknowledgement lost after the server committed.
		if fail {
			if err = native.tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
		}
		receipt, err := tx.Commit(ctx)
		if receipt.TransactionID == "" || receipt.TransactionID != native.txID {
			t.Errorf("commit fail=%v lost correlation: %+v", fail, receipt)
		}
		var auditRows int
		if readErr := pool.QueryRow(ctx, "SELECT COUNT(*) FROM tegola_audit WHERE transaction_id=$1", receipt.TransactionID).Scan(&auditRows); readErr != nil {
			t.Fatal(readErr)
		}
		expected := 1
		if fail {
			expected = 0
		}
		if auditRows != expected {
			t.Errorf("audit correlation rows=%d want=%d", auditRows, expected)
		}
		if (err != nil) != fail {
			t.Fatalf("commit fail=%v err=%v", fail, err)
		}
	}
}

func TestFollowupPostGISRevisionLockOrder(t *testing.T) {
	cases := []struct {
		name string
		op   provider.MutationOp
		want string
	}{
		{name: "replace", op: provider.MutationReplace, want: "0.1"},
		{name: "update", op: provider.MutationUpdate, want: "0.1"},
		{name: "delete", op: provider.MutationDelete, want: "0.1"},
		{name: "first_revision", op: provider.MutationUpdate, want: "0.0"},
		{name: "unguarded", op: provider.MutationReplace, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, pool := followupPostGIS(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := w.DescribeWritable(ctx, "items"); err != nil {
				t.Fatal(err)
			}
			mp := w.mappings["items"]
			if _, err := pool.Exec(ctx, "INSERT INTO items(id,name) VALUES(1,'original')"); err != nil {
				t.Fatal(err)
			}
			if tc.want != "0.0" {
				if _, err := pool.Exec(ctx, "INSERT INTO tegola_revisions VALUES($1,1,1,0)", mp.revisionCollection()); err != nil {
					t.Fatal(err)
				}
			}

			tx1, err := w.BeginFeatureTx(ctx, provider.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx1.Rollback(ctx) }()
			tx2, err := w.BeginFeatureTx(ctx, provider.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx2.Rollback(ctx) }()
			first, second := tx1.(*featureTx), tx2.(*featureTx)
			if err = checkRevisionCAS(ctx, first.tx, mp.revisionCollection(), 1, tc.want); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err = second.tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, applyErr := tx2.Apply(ctx, provider.Mutation{Op: tc.op, Collection: "items", FeatureID: 1, IfRevision: tc.want, Properties: map[string]provider.MutationValue{"name": {Kind: provider.MutationValueString, String: "loser"}}})
				done <- applyErr
			}()
			waiting := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if err := pool.QueryRow(ctx, "SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("second mutation did not reach native lock wait")
			}
			_, err = tx1.Apply(ctx, provider.Mutation{Op: provider.MutationUpdate, Collection: "items", FeatureID: 1, IfRevision: tc.want, Properties: map[string]provider.MutationValue{"name": {Kind: provider.MutationValueString, String: "winner"}}})
			if err != nil {
				_ = tx1.Rollback(ctx)
				t.Errorf("revision owner cannot update feature: %v", err)
			} else {
				if _, err = tx1.Commit(ctx); err != nil {
					t.Error(err)
				}
			}
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("overlapping mutation did not finish")
			}
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unguarded overlap failed: %v", err)
				}
				return
			}
			if me, ok := provider.AsMutationError(err); !ok || me.Kind != provider.MutationErrPreconditionFailed {
				t.Errorf("overlap must resolve as stale revision, got %v", err)
			}
		})
	}
}
