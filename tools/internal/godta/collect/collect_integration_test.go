package collect

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// fixture is a throwaway database with the data set, its pool and the
// server pool.
type fixture struct {
	p      sqlq.Profile
	r      sqlq.SecretResolver
	server *sql.DB
	name   string
	db     *sql.DB
}

func newFixture(t *testing.T, collation string, dataset bool) fixture {
	t.Helper()
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	name := ittest.NewDatabase(t, server, collation)
	db := ittest.OpenDB(t, p, r, name)
	if dataset {
		ittest.LoadDataset(t, db)
	}
	return fixture{p, r, server, name, db}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func exec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(ctxT(t), stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

// collectOne lists the databases with an Include on the fixture's name and
// collects it.
func (f fixture) collect(t *testing.T) *model.Workload {
	t.Helper()
	ctx := ctxT(t)
	inst, err := conn.Identify(ctx, f.server)
	if err != nil {
		t.Fatal(err)
	}
	keep, skipped, err := Databases(ctx, f.server, Options{Include: []string{f.name}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 1 || keep[0].Name != f.name {
		t.Fatalf("kept %v, skipped %v, want %s kept", keep, skipped, f.name)
	}
	w, err := Database(ctx, f.db, inst, keep[0], Options{})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func table(t *testing.T, w *model.Workload, name string) *model.Table {
	t.Helper()
	for _, tab := range w.Tables {
		if tab.Schema == "dbo" && tab.Name == name {
			return tab
		}
	}
	t.Fatalf("table dbo.%s not collected", name)
	return nil
}

func index(tab *model.Table, name string) *model.Index {
	for i := range tab.Indexes {
		if tab.Indexes[i].Name == name {
			return &tab.Indexes[i]
		}
	}
	return nil
}

func TestCollectReadsQueryStore(t *testing.T) {
	f := newFixture(t, "", true)
	ittest.RunWorkload(t, f.db, 3)
	w := f.collect(t)
	if w.Source != model.SourceQueryStore {
		t.Errorf("Source %q, want %q", w.Source, model.SourceQueryStore)
	}
	if w.CaptureMode != "ALL" {
		t.Errorf("CaptureMode %q, want ALL", w.CaptureMode)
	}
	const param = "(@a int,@b int)SELECT c FROM dbo.godta_t WHERE a = @a AND b = @b"
	var sawParam, sawH bool
	for _, q := range w.Queries {
		if q.Text == param {
			sawParam = true
			if q.Executions < 3 {
				t.Errorf("parameterized query: %d executions, want at least 3", q.Executions)
			}
			if q.PlanXML == "" {
				t.Error("parameterized query: empty PlanXML")
			}
		}
		if strings.Contains(q.Text, "godta_h") && strings.Contains(q.Text, "42") {
			sawH = true
		}
	}
	if !sawParam {
		t.Errorf("no query with text %q", param)
	}
	if !sawH {
		t.Error("no query x = 42 on godta_h")
	}
	var sawUpdate bool
	for _, q := range w.Writes {
		if strings.HasPrefix(q.Text, "UPDATE dbo.godta_t") {
			sawUpdate = true
		}
		if !q.IsWrite {
			t.Errorf("write %q has IsWrite false", q.Text)
		}
	}
	if !sawUpdate {
		t.Error("Writes has no UPDATE of godta_t")
	}
	for _, q := range w.Queries {
		if q.IsWrite {
			t.Errorf("read list has the write %q", q.Text)
		}
	}
}

func TestCollectIndexes(t *testing.T) {
	f := newFixture(t, "", true)
	ittest.RunWorkload(t, f.db, 1)
	w := f.collect(t)
	tt := table(t, w, "godta_t")
	if pk := index(tt, "pk_godta_t"); pk == nil || pk.Kind != model.KindClustered || !pk.PrimaryKey {
		t.Errorf("pk_godta_t: %+v, want a clustered primary key", pk)
	}
	if fi := index(tt, "godta_filt"); fi == nil || fi.Filter == "" {
		t.Errorf("godta_filt: %+v, want a filter", fi)
	}
	h := table(t, w, "godta_h")
	nix := index(h, "nix_h_x")
	if nix == nil {
		t.Fatal("nix_h_x not collected")
	}
	var pages int64
	if err := f.db.QueryRowContext(ctxT(t), `SELECT SUM(in_row_data_page_count) FROM sys.dm_db_partition_stats
WHERE object_id = OBJECT_ID(N'dbo.godta_h') AND index_id = INDEXPROPERTY(OBJECT_ID(N'dbo.godta_h'), N'nix_h_x', 'IndexID');`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if nix.DataPages != pages || pages == 0 {
		t.Errorf("nix_h_x DataPages %d, in_row_data_page_count %d", nix.DataPages, pages)
	}
}

func TestCollectColumnstoreFlag(t *testing.T) {
	f := newFixture(t, "", true)
	ittest.RunWorkload(t, f.db, 1)
	w := f.collect(t)
	if !table(t, w, "godta_cs").HasColumnstore {
		t.Error("godta_cs.HasColumnstore false")
	}
	if table(t, w, "godta_h").HasColumnstore {
		t.Error("godta_h.HasColumnstore true")
	}
}

func TestCollectHypotheticalsApart(t *testing.T) {
	f := newFixture(t, "", true)
	exec(t, f.db, "CREATE INDEX godta_test_hyp ON dbo.godta_h(y) WITH STATISTICS_ONLY = -1;")
	ittest.RunWorkload(t, f.db, 1)
	w := f.collect(t)
	if index(table(t, w, "godta_h"), "godta_test_hyp") != nil {
		t.Error("the hypothetical index is in Table.Indexes")
	}
	var found bool
	for _, ix := range w.Hypotheticals {
		if ix.Name == "godta_test_hyp" && ix.Hypothetical {
			found = true
		}
	}
	if !found {
		t.Errorf("hypothetical index not in Hypotheticals: %+v", w.Hypotheticals)
	}
}

func TestCollectQueryStoreOffFallsBack(t *testing.T) {
	f := newFixture(t, "", true)
	exec(t, f.server, "ALTER DATABASE "+tsql.QuoteName(f.name)+" SET QUERY_STORE = OFF;")
	// The workload runs after the Query Store is off, on the same database:
	// its plans are in the cache when collect reads it (sys.sp_query_store_flush_db
	// is not called, the Query Store being off).
	for _, stmt := range ittest.Workload {
		rows, err := f.db.QueryContext(ctxT(t), stmt)
		if err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		for rows.Next() {
		}
		rows.Close()
	}
	w := f.collect(t)
	if w.Source != model.SourcePlanCache {
		t.Errorf("Source %q, want %q", w.Source, model.SourcePlanCache)
	}
	if !slices.Contains(w.Warnings, WarnPlanCache) {
		t.Errorf("warnings %q, want %q", w.Warnings, WarnPlanCache)
	}
	var sawH bool
	for _, q := range w.Queries {
		if strings.Contains(q.Text, "godta_h") {
			sawH = true
			if q.PlanXML == "" {
				t.Error("cached query on godta_h without a plan")
			}
		}
	}
	if !sawH {
		t.Errorf("no cached query on godta_h among %d", len(w.Queries))
	}
	// The cache now holds collect's own queries, and those of the drift
	// check and the fingerprints of a dry-run: a second collect reads back
	// none of them.
	if _, err := CatalogDrift(ctxT(t), f.db, w); err != nil {
		t.Fatal(err)
	}
	if _, err := runplan.ReadFingerprints(ctxT(t), f.db, slices.Sorted(maps.Keys(w.Tables))); err != nil {
		t.Fatal(err)
	}
	again := f.collect(t)
	for _, q := range append(again.Queries, again.Writes...) {
		if tsql.IsInternal(q.Text) || !strings.Contains(q.Text, "godta_") {
			t.Errorf("second collect: query %d is not of the workload: %.300q", q.QueryID, q.Text)
		}
	}
}

func TestCollectSkipsReadOnly(t *testing.T) {
	f := newFixture(t, "", false)
	ro := ittest.NewDatabase(t, f.server, "")
	exec(t, f.server, "ALTER DATABASE "+tsql.QuoteName(ro)+" SET READ_ONLY;")
	keep, skipped, err := Databases(ctxT(t), f.server, Options{Include: []string{f.name, ro}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 1 || keep[0].Name != f.name {
		t.Errorf("kept %v, want %s", keep, f.name)
	}
	if !slices.Contains(skipped, Skip{Name: ro, Reason: ReasonReadOnly}) {
		t.Errorf("skipped %v, want %s read_only", skipped, ro)
	}
}

func TestCollectSkipsClone(t *testing.T) {
	f := newFixture(t, "", false)
	clone := "godta_" + workspace.NewRunID() + "_c"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := f.server.ExecContext(ctx, "IF DB_ID("+tsql.QuoteString(clone)+") IS NOT NULL "+tsql.DropDatabase(clone)+";"); err != nil {
			t.Errorf("dropping %s: %v", clone, err)
		}
	})
	exec(t, f.server, tsql.CloneDatabase(f.name, clone))
	keep, skipped, err := Databases(ctxT(t), f.server, Options{Include: []string{f.name, clone}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 1 || keep[0].Name != f.name {
		t.Errorf("kept %v, want %s", keep, f.name)
	}
	if !slices.Contains(skipped, Skip{Name: clone, Reason: ReasonClone}) {
		t.Errorf("skipped %v, want %s clone", skipped, clone)
	}
}

func TestCollectCaseSensitive(t *testing.T) {
	f := newFixture(t, "Latin1_General_CS_AS", false)
	exec(t, f.db, "CREATE TABLE dbo.godta_cs_t (a int NOT NULL, A int NOT NULL);")
	exec(t, f.db, "INSERT dbo.godta_cs_t (a, A) VALUES (1, 2), (3, 4);")
	for range 2 {
		exec(t, f.db, "SELECT a, A FROM dbo.godta_cs_t WHERE a = 1 AND A = 2;")
	}
	exec(t, f.db, "EXEC sys.sp_query_store_flush_db;")
	w := f.collect(t)
	tab := table(t, w, "godta_cs_t")
	var names []string
	ids := map[int]bool{}
	for _, c := range tab.Columns {
		names = append(names, c.Name)
		ids[c.ID] = true
	}
	if !slices.Equal(names, []string{"a", "A"}) || len(ids) != 2 {
		t.Errorf("columns %+v, want a and A, distinct", tab.Columns)
	}
}

func TestCollectScopedConfigTyped(t *testing.T) {
	f := newFixture(t, "", true)
	ittest.RunWorkload(t, f.db, 1)
	w := f.collect(t)
	types := map[string]string{}
	for _, c := range w.ScopedConfigs {
		types[c.Name] = c.BaseType
	}
	if types["MAXDOP"] != "int" {
		t.Errorf("MAXDOP base type %q, want int", types["MAXDOP"])
	}
	if types["LEGACY_CARDINALITY_ESTIMATION"] != "bit" {
		t.Errorf("LEGACY_CARDINALITY_ESTIMATION base type %q, want bit", types["LEGACY_CARDINALITY_ESTIMATION"])
	}
}

// TestCatalogDrift: the live indexes of a freshly collected database are
// those of collect.json; an index created, changed or dropped since is
// named.
func TestCatalogDrift(t *testing.T) {
	f := newFixture(t, "", true)
	ittest.RunWorkload(t, f.db, 1)
	w := f.collect(t)
	ctx := ctxT(t)
	drift, err := CatalogDrift(ctx, f.db, w)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 0 {
		t.Fatalf("control, right after collect: drift %q, want none", drift)
	}
	exec(t, f.db, "CREATE INDEX godta_drift_new ON dbo.godta_t (c) INCLUDE (a);")
	exec(t, f.db, "DROP INDEX nix_h_x ON dbo.godta_h; CREATE INDEX nix_h_x ON dbo.godta_h (x) INCLUDE (y);")
	drift, err = CatalogDrift(ctx, f.db, w)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(drift, "\n")
	for _, want := range []string{"[dbo].[godta_t]: index [godta_drift_new] created", "[dbo].[godta_h]: index [nix_h_x] changed"} {
		if !strings.Contains(got, want) {
			t.Errorf("drift lacks %q:\n%s", want, got)
		}
	}
	exec(t, f.db, "DROP INDEX godta_drift_new ON dbo.godta_t;")
	exec(t, f.db, "DROP INDEX nix_h_x ON dbo.godta_h;")
	drift, err = CatalogDrift(ctx, f.db, w)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(drift, "\n"); got != "[dbo].[godta_h]: index [nix_h_x] dropped" {
		t.Errorf("after the drops, drift %q, want nix_h_x dropped alone", got)
	}
}

// TestCollectSkipsSingleUser: Databases reads user_access_desc: a database
// set to SINGLE_USER, then RESTRICTED_USER, is skipped with its reason.
func TestCollectSkipsSingleUser(t *testing.T) {
	f := newFixture(t, "", false)
	db := ittest.NewDatabase(t, f.server, "")
	for mode, reason := range map[string]string{"SINGLE_USER": ReasonSingleUser, "RESTRICTED_USER": ReasonRestrictedUser} {
		exec(t, f.server, "ALTER DATABASE "+tsql.QuoteName(db)+" SET "+mode+" WITH ROLLBACK IMMEDIATE;")
		keep, skipped, err := Databases(ctxT(t), f.server, Options{Include: []string{f.name, db}})
		if err != nil {
			t.Fatal(err)
		}
		if len(keep) != 1 || keep[0].Name != f.name {
			t.Errorf("%s: kept %v, want %s alone", mode, keep, f.name)
		}
		if !slices.Contains(skipped, Skip{Name: db, Reason: reason}) {
			t.Errorf("%s: skipped %v, want %s %s", mode, skipped, db, reason)
		}
	}
}

// TestCollectLowerCaseHintCaseSensitive: under a case-sensitive database
// collation sys.sql_modules.definition has the database's collation (measured
// on 2022), so the scan for INDEX hints must compare without case or a module
// written with index(...) in lower case is missed.
func TestCollectLowerCaseHintCaseSensitive(t *testing.T) {
	f := newFixture(t, "Latin1_General_CS_AS", false)
	exec(t, f.db, "CREATE TABLE dbo.godta_lc_t (a int NOT NULL, b int NOT NULL, INDEX godta_lc_b (b));")
	exec(t, f.db, "CREATE PROCEDURE dbo.godta_lc_p AS SELECT a FROM dbo.godta_lc_t with (index(godta_lc_b)) WHERE b = 1;")
	for range 2 {
		exec(t, f.db, "SELECT a FROM dbo.godta_lc_t WHERE b = 2;")
	}
	exec(t, f.db, "EXEC sys.sp_query_store_flush_db;")
	w := f.collect(t)
	ix := index(table(t, w, "godta_lc_t"), "godta_lc_b")
	if ix == nil {
		t.Fatal("index godta_lc_b not collected")
	}
	if !slices.Contains(ix.NamedIn, "sql_module:dbo.godta_lc_p") {
		t.Errorf("NamedIn %v, want sql_module:dbo.godta_lc_p", ix.NamedIn)
	}
}
