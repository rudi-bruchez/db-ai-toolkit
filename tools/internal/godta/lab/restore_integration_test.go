package lab_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/evaluate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/replay"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// scopedConfigsQuery is collect's read of sys.database_scoped_configurations.
const scopedConfigsQuery = `SELECT name, ISNULL(CAST(value AS nvarchar(4000)), N''),
  ISNULL(CAST(SQL_VARIANT_PROPERTY(value, 'BaseType') AS nvarchar(128)), N'')
FROM sys.database_scoped_configurations
WHERE name IN (N'MAXDOP', N'LEGACY_CARDINALITY_ESTIMATION', N'PARAMETER_SNIFFING', N'QUERY_OPTIMIZER_HOTFIXES')
ORDER BY name;`

func readScopedConfigs(t *testing.T, ctx context.Context, db *sql.DB) []tsql.ScopedConfig {
	t.Helper()
	rows, err := db.QueryContext(ctx, scopedConfigsQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []tsql.ScopedConfig
	for rows.Next() {
		var c tsql.ScopedConfig
		if err := rows.Scan(&c.Name, &c.Value, &c.BaseType); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func scalarInt(t *testing.T, ctx context.Context, q conn.Querier, query string, args ...any) int {
	t.Helper()
	var n sql.NullInt64
	if err := q.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return int(n.Int64)
}

// labEnv is the lab of the integration tests: the profile, a pool on
// master, its identity, the host directory and the container's view of it.
type labEnv struct {
	server       *sql.DB
	inst         conn.Instance
	open         func(db string) (*sql.DB, error)
	hostDir      string
	containerDir string
}

func newLabEnv(t *testing.T) *labEnv {
	t.Helper()
	hostDir, containerDir := os.Getenv("GODTA_TEST_LAB_HOSTDIR"), os.Getenv("GODTA_TEST_LAB_CONTAINERDIR")
	if hostDir == "" || containerDir == "" {
		t.Skip("GODTA_TEST_LAB_HOSTDIR and GODTA_TEST_LAB_CONTAINERDIR not set")
	}
	lp, lr := ittest.Profile(t, "GODTA_TEST_LAB_PROFILE")
	e := &labEnv{server: ittest.OpenDB(t, lp, lr, ""), hostDir: hostDir, containerDir: containerDir}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var err error
	if e.inst, err = conn.Identify(ctx, e.server); err != nil {
		t.Fatal(err)
	}
	e.open = func(db string) (*sql.DB, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return conn.Open(ctx, lp, lr, db)
	}
	return e
}

// cloneToLab runs Clone of the fixture into the lab's host directory, the
// host file removed in t.Cleanup.
func cloneToLab(t *testing.T, f *cloneFixture, e *labEnv) (runplan.Plan, lab.File) {
	t.Helper()
	p := f.plan(t, 0, 0)
	t.Cleanup(func() { os.Remove(filepath.Join(e.hostDir, runplan.CloneDatabaseName(p.RunID)+".bak")) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	file, err := lab.Clone(ctx, f.db, f.ws, p, e.hostDir)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	return p, file
}

func runNames(t *testing.T, ctx context.Context, q conn.Querier, query string) []string {
	t.Helper()
	rows, err := q.QueryContext(ctx, query, tsql.RunPattern())
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

const statsColumnsQuery = `SELECT s.name FROM sys.stats AS s WHERE s.name LIKE @p1
  AND (SELECT COUNT(*) FROM sys.stats_columns AS c WHERE c.object_id = s.object_id AND c.stats_id = s.stats_id) %s 1
ORDER BY s.name`

// TestRestorePreparesClone: the fixture's clone, with two candidates of two
// keys and one of one key, restored on the lab and prepared: a clone,
// READ_WRITE, automatic statistics off, the source's database MAXDOP, no
// multicolumn godta_* statistics left and the single-column ones there, one
// stream per candidate of several keys, lab.json with Prepared. Then the
// costs of three queries are measured on the source and on the lab, with
// the hypothetical index (a, b), for the report.
func TestRestorePreparesClone(t *testing.T) {
	e := newLabEnv(t)
	f := newCloneFixture(t)
	ab := f.cands[0]
	col := func(name string) int {
		for _, c := range f.w.Tables[ab.Table].Columns {
			if c.Name == name {
				return c.ID
			}
		}
		t.Fatalf("no column %s", name)
		return 0
	}
	ca := model.Candidate{ID: "c2", Table: ab.Table, Keys: []model.KeyCol{{ColumnID: col("c")}, {ColumnID: col("a")}}, Decision: model.DecisionNew}
	b := model.Candidate{ID: "c3", Table: ab.Table, Keys: []model.KeyCol{{ColumnID: col("b")}}, Decision: model.DecisionNew}
	f.cands = append(f.cands, ca, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	f.w.ScopedConfigs = readScopedConfigs(t, ctx, f.db)
	srcMaxdop := scalarInt(t, ctx, f.db, "SELECT CAST(value AS int) FROM sys.database_scoped_configurations WHERE name = N'MAXDOP'")
	if srcMaxdop != 2 {
		t.Fatalf("the data set sets the source's database MAXDOP to 2, read %d", srcMaxdop)
	}
	if e.inst.MaxDOP == 2 {
		t.Fatalf("the lab's instance MAXDOP is 2: the test cannot tell a carried MAXDOP from the lab's own")
	}

	p, file := cloneToLab(t, f, e)
	db := runplan.CloneDatabaseName(p.RunID)
	dropOnCleanup(t, e.server, db)
	dir := f.ws.DBDir(p.Server, p.Database)
	prep, err := lab.Restore(ctx, f.ws, dir, e.server, e.open, f.inst, e.inst, f.w, file, e.containerDir, "", db)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	t.Logf("restored %s, %d stream(s), warnings %q", prep.Database, len(prep.Streams), prep.Warnings)

	if prep.Database != db {
		t.Errorf("Prepared.Database %q, want %q", prep.Database, db)
	}
	if n := scalarInt(t, ctx, e.server, "SELECT CAST(DATABASEPROPERTYEX(@p1, 'IsClone') AS int)", db); n != 1 {
		t.Errorf("IsClone of %s on the lab: %d, want 1", db, n)
	}
	var readOnly, autoCreate, autoUpdate bool
	if err := e.server.QueryRowContext(ctx, "SELECT is_read_only, is_auto_create_stats_on, is_auto_update_stats_on FROM sys.databases WHERE name = @p1", db).
		Scan(&readOnly, &autoCreate, &autoUpdate); err != nil {
		t.Fatal(err)
	}
	if readOnly {
		t.Errorf("%s is read-only on the lab", db)
	}
	if autoCreate {
		t.Errorf("%s: AUTO_CREATE_STATISTICS is ON", db)
	}
	if autoUpdate {
		t.Errorf("%s: AUTO_UPDATE_STATISTICS is ON", db)
	}

	labDB, err := e.open(db)
	if err != nil {
		t.Fatal(err)
	}
	// Closed in t.Cleanup, after the drops of the measurement's indexes
	// registered later.
	t.Cleanup(func() { labDB.Close() })
	if n := scalarInt(t, ctx, labDB, "SELECT CAST(value AS int) FROM sys.database_scoped_configurations WHERE name = N'MAXDOP'"); n != srcMaxdop {
		t.Errorf("database MAXDOP on the lab: %d, the source's %d", n, srcMaxdop)
	}
	if multi := runNames(t, ctx, labDB, fmt.Sprintf(statsColumnsQuery, ">")); len(multi) != 0 {
		t.Errorf("multicolumn statistics left on the lab: %v", multi)
	}
	// (a, b), (c, a) and (b) give the single-column (a), (b) and (c).
	if single := runNames(t, ctx, labDB, fmt.Sprintf(statsColumnsQuery, "=")); len(single) != 3 {
		t.Errorf("single-column godta_* statistics on the lab: %v, want 3", single)
	}
	var keys []string
	for k, v := range prep.Streams {
		keys = append(keys, k)
		if len(v) == 0 {
			t.Errorf("stream %s is empty", k)
		}
	}
	slices.Sort(keys)
	want := []string{lab.StreamKey(ab.Table, ab.Keys), lab.StreamKey(ca.Table, ca.Keys)}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Errorf("streams %v, want one per candidate of several keys: %v", keys, want)
	}
	st, err := lab.LoadState(f.ws, dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.File != file || st.Prepared == nil || st.Prepared.Database != db || len(st.Prepared.Streams) != len(prep.Streams) {
		t.Errorf("lab.json has %+v, want File %+v and the Prepared of %s", st, file, db)
	}
	if left := runNames(t, ctx, f.db, "SELECT name FROM sys.stats WHERE name LIKE @p1"); len(left) != 0 {
		t.Errorf("statistics left on the source: %v", left)
	}
	if left := runNames(t, ctx, f.server, "SELECT name FROM sys.databases WHERE name LIKE @p1"); len(left) != 0 {
		t.Errorf("databases left on the source: %v", left)
	}

	measureSourceAgainstLab(t, ctx, f, e, labDB, prep, ab)
}

// measureSourceAgainstLab logs, for three queries of the spike, the
// SHOWPLAN_XML cost and estimate on the source and on the lab, and the
// AUTOPILOT cost with the hypothetical index of c on the source, on the lab
// with the stream posted by StatsHook, and on the lab without it. It
// asserts nothing: the comparison of the two instances is task 19's.
func measureSourceAgainstLab(t *testing.T, ctx context.Context, f *cloneFixture, e *labEnv, labDB *sql.DB, prep lab.Prepared, c model.Candidate) {
	t.Helper()
	queries := []string{
		"SELECT id FROM dbo.godta_t WHERE a = 0 AND b = 1",
		"SELECT id FROM dbo.godta_t WHERE a = 933 AND b = 9332",
		"SELECT id FROM dbo.godta_t WHERE a = 0 AND b = 9332",
	}
	tbl := f.w.Tables[c.Table]
	run := workspace.NewRunID()
	type side struct {
		name   string
		db     *sql.DB
		inst   conn.Instance
		hook   bool
		suffix string
	}
	for _, sd := range []side{{"source", f.db, f.inst, false, "s"}, {"lab+stream", labDB, e.inst, true, "l"}, {"lab-nostream", labDB, e.inst, false, "n"}} {
		s, err := evaluate.NewSession(ctx, sd.db, sd.inst, time.Minute, 2000)
		if err != nil {
			t.Fatalf("%s: NewSession: %v", sd.name, err)
		}
		name := "godta_" + run + "_m" + sd.suffix
		def, ok := runplan.Definition(tbl, c, name)
		if !ok {
			t.Fatal("definition")
		}
		if err := s.Exec(ctx, tsql.CreateHypothetical(def)); err != nil {
			t.Fatalf("%s: %v", sd.name, err)
		}
		t.Cleanup(func() {
			if _, err := sd.db.ExecContext(context.Background(), "IF INDEXPROPERTY(OBJECT_ID(N'dbo.godta_t'), "+tsql.QuoteString(name)+", 'IndexID') IS NOT NULL "+tsql.DropIndex(def.Table, name)); err != nil {
				t.Errorf("%s: dropping %s: %v", sd.name, name, err)
			}
		})
		if sd.hook {
			if _, err := lab.StatsHook(prep, f.w).AfterCreateHypothetical(ctx, s, c, name); err != nil {
				t.Errorf("%s: hook: %v", sd.name, err)
			}
		}
		objectID := scalarInt(t, ctx, sd.db, "SELECT OBJECT_ID(N'dbo.godta_t')")
		t.Logf("%s: object_id of dbo.godta_t %d (source workload %d)", sd.name, objectID, tbl.ObjectID)
		indexID := scalarInt(t, ctx, sd.db, "SELECT index_id FROM sys.indexes WHERE object_id = OBJECT_ID(N'dbo.godta_t') AND name = @p1", name)
		rows := int64(scalarInt(t, ctx, sd.db, "SELECT SUM(row_count) FROM sys.dm_db_partition_stats WHERE object_id = OBJECT_ID(N'dbo.godta_t') AND index_id <= 1"))
		pages := evaluate.HypotheticalPages(rows, 8, 4)
		for _, q := range queries {
			rp := replay.Replay{Kind: replay.KindAdhoc, Call: q}
			sp, err := s.Compile(ctx, rp, false)
			if err != nil {
				t.Errorf("%s: SHOWPLAN_XML %s: %v", sd.name, q, err)
				continue
			}
			if err := s.Reset(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.DeclareHypothetical(ctx, objectID, indexID, pages); err != nil {
				t.Fatal(err)
			}
			ap, err := s.Compile(ctx, rp, true)
			if err != nil {
				t.Errorf("%s: AUTOPILOT %s: %v", sd.name, q, err)
				continue
			}
			t.Logf("%-12s %-55s showplan cost %.6f rows %.1f | hypothetical cost %.6f rows %.1f",
				sd.name, q, sp.Cost, firstRows(sp.Accesses), ap.Cost, firstRows(ap.Accesses))
		}
		s.Close()
	}
}

// firstRows is the estimate of the plan's first access, -1 without one.
func firstRows(as []showplan.Access) float64 {
	if len(as) == 0 {
		return -1
	}
	return as[0].EstimateRows
}

// TestRestoreRefusesSameInstance: Restore with the source as the lab is
// refused before anything is sent to it, and no database is created.
func TestRestoreRefusesSameInstance(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	inst, err := conn.Identify(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	db := "godta_" + workspace.NewRunID() + "_c"
	dropOnCleanup(t, server, db)
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	open := func(string) (*sql.DB, error) { opened = true; return nil, fmt.Errorf("open must not be called") }
	file := lab.File{HostPath: filepath.Join(t.TempDir(), db+".bak")}
	_, err = lab.Restore(ctx, ws, t.TempDir(), server, open, inst, inst, &model.Workload{}, file, "/var/opt/mssql/godta", "", db)
	if err == nil {
		t.Fatal("Restore accepted the source as the lab")
	}
	t.Logf("refused: %v", err)
	if !strings.Contains(err.Error(), "lab guard") {
		t.Errorf("refused by %v, want the lab guard", err)
	}
	if opened {
		t.Error("Restore opened the database")
	}
	if n := scalarInt(t, ctx, server, "SELECT COUNT(*) FROM sys.databases WHERE name = @p1", db); n != 0 {
		t.Errorf("database %s created on the source", db)
	}
}
