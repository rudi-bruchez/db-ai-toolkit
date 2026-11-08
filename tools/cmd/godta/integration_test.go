package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/report"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// TestEndToEndServer runs the part 1 phases through the binary's run, on a
// throwaway database loaded with the data set and the workload (plan,
// annexes A and A bis): collect, candidates, evaluate --dry-run, evaluate
// --plan, report, cleanup --dry-run.
func TestEndToEndServer(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	name := ittest.NewDatabase(t, server, "")
	db := ittest.OpenDB(t, p, r, name)
	ittest.LoadDataset(t, db)
	// The Query Store captured the load of the data set too: its INSERT of
	// 200 000 rows into godta_h is a write of the window, and the score's
	// maintenance fallback (rows written x executions) then rejects every
	// candidate of the table (measured). The workload under test is annex A
	// bis alone, as it would be on a server whose tables were loaded long
	// before the window.
	if _, err := server.Exec("ALTER DATABASE [" + name + "] SET QUERY_STORE CLEAR;"); err != nil {
		t.Fatal(err)
	}
	ittest.RunWorkload(t, db, 3)

	project := filepath.Join(t.TempDir(), "project")
	common := []string{"--project", project, "--profiles", os.Getenv("GODTA_TEST_PROFILES"),
		"--profile", os.Getenv("GODTA_TEST_PROFILE"), "--env", filepath.Join(t.TempDir(), "absent.env"),
		"--include", name}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	step := func(args ...string) output {
		t.Helper()
		var buf bytes.Buffer
		code := run(ctx, append(args, common...), realDepsTo(&buf))
		var out output
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatalf("%s: stdout is not JSON: %v\n%s", args[0], err, buf.String())
		}
		if code != exitOK || !out.OK {
			t.Fatalf("%s: code %d\n%s", strings.Join(args, " "), code, buf.String())
		}
		return out
	}

	out := step("collect")
	if len(out.Databases) != 1 || out.Databases[0].Name != name || out.Databases[0].Status != "collected" {
		t.Fatalf("collect --include %s: databases %+v, want that database alone, collected", name, out.Databases)
	}
	serverName := out.Databases[0].Server
	step("candidates")
	out = step("evaluate", "--target", "server", "--dry-run")
	if out.Plan == "" {
		t.Fatalf("evaluate --dry-run wrote no plan: %+v", out)
	}
	var plan runplan.Plan
	data, err := os.ReadFile(out.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	step("evaluate", "--target", "server", "--allow-write", "--plan", out.Plan)
	ws := workspace.WS{Root: project}
	// The collection, the plan and the evaluation name the same collect run:
	// that is what report checks before scoring.
	var w model.Workload
	var ev evaluateFile
	if err := ws.ReadJSON(ws.DBDir(serverName, name), workspace.CollectFile, &w); err != nil {
		t.Fatal(err)
	}
	if err := ws.ReadJSON(ws.DBDir(serverName, name), workspace.EvaluateServerFile, &ev); err != nil {
		t.Fatal(err)
	}
	if w.CollectID == "" || plan.CollectID != w.CollectID || ev.CollectID != w.CollectID {
		t.Errorf("collect_id: collect.json %q, plan %q, evaluate-server.json %q, want one and the same", w.CollectID, plan.CollectID, ev.CollectID)
	}
	step("report")

	var res report.Result
	if err := ws.ReadJSON(ws.DBDir(serverName, name), report.ResultFile, &res); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range res.Recommendations {
		if rec.Action == "widen" && rec.Existing != nil && rec.Existing.Name == "nix_h_x" &&
			rec.Definition == "[dbo].[godta_h] ([x] ASC) INCLUDE ([y])" && rec.Section == report.Recommended {
			found = true
		}
	}
	if !found {
		var b strings.Builder
		for _, rec := range res.Recommendations {
			g := "nil"
			if rec.Gain != nil {
				g = fmt.Sprintf("%.2f", *rec.Gain)
			}
			b.WriteString("\n  " + rec.Section + " " + rec.Action + " " + rec.Definition + " gain " + g + " notes " + strings.Join(rec.Notes, "; "))
			for _, q := range rec.Queries {
				qj, _ := json.Marshal(q)
				b.WriteString("\n    " + string(qj))
			}
		}
		t.Errorf("no recommended widen of nix_h_x into [dbo].[godta_h] ([x] ASC) INCLUDE ([y]); recommendations:%s", b.String())
	}

	out = step("cleanup", "--dry-run")
	var cplan runplan.Plan
	data, err = os.ReadFile(out.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cplan); err != nil {
		t.Fatal(err)
	}
	if len(cplan.Steps) != 0 || len(out.Databases) != 0 {
		t.Errorf("cleanup --dry-run found leftovers: steps %+v, databases %+v", cplan.Steps, out.Databases)
	}

	// Every phase above sent goDTA's own queries to the database (the
	// catalog reads of collect, the drift check and the fingerprints of the
	// dry-run, the reads of evaluate and cleanup), and its Query Store, in
	// capture mode ALL, captured them. A second collect reads back the
	// workload alone: every query names a table of the data set.
	if _, err := db.ExecContext(ctx, "EXEC sys.sp_query_store_flush_db;"); err != nil {
		t.Fatal(err)
	}
	step("collect")
	var again model.Workload
	if err := ws.ReadJSON(ws.DBDir(serverName, name), workspace.CollectFile, &again); err != nil {
		t.Fatal(err)
	}
	if len(again.Queries) == 0 {
		t.Fatal("second collect: no query")
	}
	for _, q := range append(again.Queries, again.Writes...) {
		if tsql.IsInternal(q.Text) || !datasetTable.MatchString(q.Text) {
			t.Errorf("second collect: query %d is not of the workload: %.300q", q.QueryID, q.Text)
		}
	}

	var left int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM sys.indexes WHERE name LIKE @p1)
  + (SELECT COUNT(*) FROM sys.stats WHERE name LIKE @p1)
  + (SELECT COUNT(*) FROM sys.databases WHERE name LIKE @p2)`,
		tsql.RunPattern(), tsql.LikePrefix("godta_"+plan.RunID+"_")).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d objects named LIKE %s left behind", left, tsql.RunPattern())
	}
}

// datasetTable is a reference to a table or view of the data set.
var datasetTable = regexp.MustCompile(`(?i)\[?dbo\]?\.\[?godta_(t|h|cs|v)\b`)

// realDepsTo is realDeps writing to w.
func realDepsTo(w *bytes.Buffer) deps {
	d := realDeps()
	d.stdout = w
	return d
}
