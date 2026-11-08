package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/report"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// partitioned is a table on a partition scheme over p, a column that is
// neither key nor included in its nonclustered indexes.
const partitioned = `CREATE PARTITION FUNCTION pf_godta (int) AS RANGE RIGHT FOR VALUES (100, 200);
CREATE PARTITION SCHEME ps_godta AS PARTITION pf_godta ALL TO ([PRIMARY]);
CREATE TABLE dbo.godta_p (id int NOT NULL, p int NOT NULL, k int NOT NULL, v int NOT NULL, w int NOT NULL,
  CONSTRAINT pk_godta_p PRIMARY KEY CLUSTERED (id, p)) ON ps_godta(p);
INSERT dbo.godta_p (id, p, k, v, w)
SELECT TOP (3000) n, n % 300, n % 97, n % 89, n FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) n FROM sys.all_columns) s;
CREATE INDEX ix_p_k ON dbo.godta_p(k) WITH (DATA_COMPRESSION = PAGE) ON ps_godta(p);
CREATE INDEX ix_p_v ON dbo.godta_p(v) ON ps_godta(p);`

// TestPartitionedScriptRuns collects an index on a partition scheme (ix_p_k
// with PAGE compression), then
// widens one, replaces another and adds a new one through report, and runs
// indexes.sql inside a transaction rolled back. Every batch must succeed:
// ON [ps_godta] without its column fails (error 2726). Every index the
// script creates is on the scheme.
func TestPartitionedScriptRuns(t *testing.T) {
	f := newFixture(t, "", false)
	exec(t, f.db, partitioned)
	for range 3 {
		exec(t, f.db, "SELECT v FROM dbo.godta_p WHERE k = 5;")
		exec(t, f.db, "SELECT w FROM dbo.godta_p WHERE v = 7;")
	}
	exec(t, f.db, "EXEC sys.sp_query_store_flush_db;")
	w := f.collect(t)

	tab := table(t, w, "godta_p")
	col := map[string]int{}
	for _, c := range tab.Columns {
		col[c.Name] = c.ID
	}
	ix := index(tab, "ix_p_k")
	if ix == nil {
		t.Fatal("ix_p_k not collected")
	}
	if ix.DataSpace != "ps_godta" || ix.PartitionColumn != col["p"] {
		t.Errorf("ix_p_k: data space %q, partition column %d, want ps_godta, %d", ix.DataSpace, ix.PartitionColumn, col["p"])
	}

	m := []model.Measure{{QueryID: 1, Status: "measured", Before: 2, After: 1}}
	rec := func(id string, d model.Decision, existing string, keys []string, include ...string) model.Recommendation {
		c := model.Candidate{ID: id, Table: tab.ObjectID, Decision: d, Existing: existing, Origin: "algorithm"}
		for _, k := range keys {
			c.Keys = append(c.Keys, model.KeyCol{ColumnID: col[k]})
		}
		for _, n := range include {
			c.Include = append(c.Include, col[n])
		}
		return model.Recommendation{Candidate: c, Gain: 10, Decision: report.Recommended, Queries: m}
	}
	recs := []model.Recommendation{
		rec("aaaaaaaaaaaa", model.DecisionWiden, "ix_p_k", []string{"k"}, "v"),
		rec("bbbbbbbbbbbb", model.DecisionReplace, "ix_p_v", []string{"v", "k"}, "w"),
		rec("cccccccccccc", model.DecisionNew, "", []string{"w"}),
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.DBDir(w.Server.ServerName, w.Database.Name)
	if err := report.Write(ws, dir, w, recs, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, report.ScriptFile))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	if !strings.Contains(script, "ON [ps_godta]([p])") {
		t.Errorf("indexes.sql does not name the scheme with its column:\n%s", script)
	}
	// The lab is Developer Edition, Enterprise features: the widening is an
	// online DROP_EXISTING that keeps the compression and the scheme, and it
	// must run below.
	if !w.Server.IsEnterpriseFeatures {
		t.Fatalf("the lab instance %q has no Enterprise features: the online statement is not tested", w.Server.Edition)
	}
	if want := "CREATE NONCLUSTERED INDEX [ix_p_k] ON [dbo].[godta_p] ([k] ASC) INCLUDE ([v]) WITH (DROP_EXISTING = ON, ONLINE = ON, DATA_COMPRESSION = PAGE) ON [ps_godta]([p])"; !strings.Contains(script, want) {
		t.Errorf("indexes.sql lacks\n%s\ngot:\n%s", want, script)
	}

	ctx := ctxT(t)
	c, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		t.Fatal(err)
	}
	defer c.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK")
	var cur []string
	n := 0
	for _, line := range strings.Split(script, "\n") {
		if strings.TrimSpace(line) != "GO" {
			cur = append(cur, line)
			continue
		}
		batch := strings.Join(cur, "\n")
		cur = nil
		n++
		if _, err := c.ExecContext(ctx, batch); err != nil {
			t.Fatalf("batch %d failed: %v\n%s", n, err, batch)
		}
	}
	// The widened ix_p_k, the replacing and the new index, and nothing
	// left of ix_p_v.
	rows, err := c.QueryContext(ctx, `SELECT i.name, ISNULL(ps.name, N'') FROM sys.indexes i
LEFT JOIN sys.partition_schemes ps ON ps.data_space_id = i.data_space_id
WHERE i.object_id = OBJECT_ID(N'dbo.godta_p') AND i.index_id > 1 ORDER BY i.name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name, scheme string
		if err := rows.Scan(&name, &scheme); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		if scheme != "ps_godta" {
			t.Errorf("index %s is on %q, not on the scheme ps_godta", name, scheme)
		}
	}
	rows.Close()
	if len(names) != 3 || names[2] != "ix_p_k" {
		t.Errorf("indexes after the script %v, want the replacing, the new and ix_p_k", names)
	}
}
