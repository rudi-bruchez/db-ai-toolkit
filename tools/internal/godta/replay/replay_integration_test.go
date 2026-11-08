package replay_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/replay"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// TestReplayCompilesOnLab sends the Setup, Call and Teardown of three Build
// outputs under SET SHOWPLAN_XML ON, on one pinned connection, each statement
// in its own batch. The unit tests prove the text; this proves the engine
// compiles it into a plan. The proc case runs under SET DATEFORMAT dmy with a
// day above 12, which the plan's own datetime form would not survive.
func TestReplayCompilesOnLab(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	dbName := ittest.NewDatabase(t, server, "")
	db := ittest.OpenDB(t, p, r, dbName)
	ittest.LoadDataset(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	w := &model.Workload{
		Database: model.DBInfo{Name: dbName, Collation: "SQL_Latin1_General_CP1_CI_AS"},
		Tables: map[int]*model.Table{
			1: {ObjectID: 1, Schema: "dbo", Name: "godta_t"},
			2: {ObjectID: 2, Schema: "dbo", Name: "godta_h"},
		},
	}
	tAccess := []showplan.Access{access(dbName, "dbo", "godta_t")}
	tempProc := "#godta_" + workspace.NewRunID() + "_r"

	cases := []struct {
		name string
		q    model.Query
		st   *showplan.Statement
		kind replay.Kind
	}{
		{
			name: "proc_datetime_dmy",
			q: model.Query{
				Text:       "(@d datetime)SELECT COUNT(*) FROM dbo.godta_t WHERE a = 933 AND @d > '20000101'",
				SetOptions: baseOpts, DateFirst: 1, DateFormat: 2,
			},
			st: &showplan.Statement{
				Params:   []showplan.Param{{Name: "@d", DataType: "datetime", CompiledValue: "'2026-11-25 10:11:12.000'"}},
				Accesses: tAccess,
			},
			kind: replay.KindProc,
		},
		{
			name: "batch_locals",
			q: model.Query{
				Text:       "(@a int)SELECT c FROM dbo.godta_t WHERE a = @a",
				SetOptions: baseOpts, DateFirst: 7, DateFormat: 1,
			},
			st:   &showplan.Statement{Accesses: tAccess},
			kind: replay.KindBatchLocals,
		},
		{
			name: "adhoc",
			q: model.Query{
				Text:       "SELECT y FROM dbo.godta_h WHERE x = 42",
				SetOptions: baseOpts, DateFirst: 7, DateFormat: 1,
			},
			st:   &showplan.Statement{Accesses: []showplan.Access{access(dbName, "dbo", "godta_h")}},
			kind: replay.KindAdhoc,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rp := replay.Build(tc.q, tc.st, w, tempProc)
			if rp.Kind != tc.kind {
				t.Fatalf("Kind %q (%s), want %q", rp.Kind, rp.Reason, tc.kind)
			}
			for _, s := range append(append([]string{}, rp.SetOptions...), rp.Setup...) {
				if _, err := c.ExecContext(ctx, s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
			defer func() {
				for _, s := range rp.Teardown {
					if _, err := c.ExecContext(ctx, s); err != nil {
						t.Errorf("%s: %v", s, err)
					}
				}
			}()
			docs, err := showplanDocs(ctx, c, rp.Call)
			if err != nil {
				t.Fatalf("%s: %v", rp.Call, err)
			}
			if len(docs) != 1 {
				t.Fatalf("%s: %d plan documents, want 1", rp.Call, len(docs))
			}
			plan, err := showplan.Parse([]byte(docs[0]))
			if err != nil {
				t.Fatalf("%s: %v", rp.Call, err)
			}
			if n := len(plan.Statements); n == 0 || plan.Statements[n-1].Root == nil {
				t.Fatalf("%s: the last statement of the plan has no QueryPlan", rp.Call)
			}
		})
	}
}

// showplanDocs compiles stmt under SET SHOWPLAN_XML ON, each SET in its own
// batch, and returns every document it yields.
func showplanDocs(ctx context.Context, c *sql.Conn, stmt string) (docs []string, err error) {
	if _, err := c.ExecContext(ctx, "SET SHOWPLAN_XML ON"); err != nil {
		return nil, err
	}
	defer func() {
		if _, offErr := c.ExecContext(ctx, "SET SHOWPLAN_XML OFF"); offErr != nil && err == nil {
			err = fmt.Errorf("SET SHOWPLAN_XML OFF: %w", offErr)
		}
	}()
	rows, err := c.QueryContext(ctx, stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for {
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			docs = append(docs, s)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return docs, rows.Err()
}
