package cleanup_test

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/cleanup"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// env is one test's server connection, instance, workspace and opener.
type env struct {
	t      *testing.T
	ctx    context.Context
	server *sql.DB
	inst   conn.Instance
	ws     workspace.WS
	open   func(db string) (*sql.DB, error)
	opened func(db string) *sql.DB
}

func newEnv(t *testing.T) *env {
	t.Helper()
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	server := ittest.OpenDB(t, p, r, "")
	inst, err := conn.Identify(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &env{
		t: t, ctx: ctx, server: server, inst: inst, ws: ws,
		open:   func(db string) (*sql.DB, error) { return conn.Open(ctx, p, r, db) },
		opened: func(db string) *sql.DB { return ittest.OpenDB(t, p, r, db) },
	}
}

// smallDB creates a throwaway database with dbo.godta_h (x, y), a few
// thousand rows, and returns its name and a pool on it.
func (e *env) smallDB() (string, *sql.DB) {
	e.t.Helper()
	name := ittest.NewDatabase(e.t, e.server, "")
	db := e.opened(name)
	e.exec(db, "CREATE TABLE dbo.godta_h (x int NOT NULL, y varchar(40) NOT NULL)")
	e.exec(db, "INSERT dbo.godta_h SELECT TOP (5000) ABS(CHECKSUM(NEWID())) % 1000, CONVERT(varchar(36), NEWID()) FROM sys.all_columns x CROSS JOIN sys.all_columns y")
	return name, db
}

func (e *env) exec(q conn.Querier, stmt string) {
	e.t.Helper()
	if _, err := q.ExecContext(e.ctx, stmt); err != nil {
		e.t.Fatalf("%s: %v", stmt, err)
	}
}

var godtaH = tsql.Obj{Schema: "dbo", Name: "godta_h"}

func (e *env) hypothetical(q conn.Querier, name string) {
	e.t.Helper()
	e.exec(q, tsql.CreateHypothetical(tsql.IndexDef{Table: godtaH, Name: name, Keys: []tsql.KeyCol{{Name: "x"}}, Include: []string{"y"}}))
}

func (e *env) entry(db, kind, name, state string) workspace.Entry {
	return workspace.Entry{
		RunID: runIDOf(name), Server: e.inst.ServerName, Database: db, Kind: kind,
		Schema: "dbo", Table: "godta_h", Name: name, At: time.Now().UTC(), State: state,
	}
}

func runIDOf(name string) string {
	if tsql.IsRunName(name) {
		return name[6:14]
	}
	return ""
}

func (e *env) record(entries ...workspace.Entry) {
	e.t.Helper()
	for _, en := range entries {
		if err := e.ws.Record(en); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) find() cleanup.Found {
	e.t.Helper()
	f, err := cleanup.Find(e.ctx, e.server, e.open, e.ws)
	if err != nil {
		e.t.Fatalf("Find: %v", err)
	}
	return f
}

// run builds, seals and runs the cleanup plan of entries, which the caller
// restricts to its own database: the pattern scan sees the whole server.
func (e *env) run(entries []workspace.Entry) []workspace.Entry {
	e.t.Helper()
	p := runplan.ForCleanup(entries)
	p.MachineName = e.inst.MachineName
	p = p.Seal()
	removed, err := cleanup.Run(e.ctx, e.server, e.open, e.ws, p)
	if err != nil {
		e.t.Fatalf("Run: %v", err)
	}
	return removed
}

func inDB(entries []workspace.Entry, db string) []workspace.Entry {
	var out []workspace.Entry
	for _, en := range entries {
		if en.Database == db {
			out = append(out, en)
		}
	}
	return out
}

func has(entries []workspace.Entry, kind, name string) bool {
	for _, en := range entries {
		if en.Kind == kind && en.Name == name {
			return true
		}
	}
	return false
}

func count(t *testing.T, ctx context.Context, q conn.Querier, query string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func indexCount(t *testing.T, ctx context.Context, db *sql.DB, name string) int {
	t.Helper()
	return count(t, ctx, db, "SELECT COUNT(*) FROM sys.indexes WHERE object_id = OBJECT_ID(N'dbo.godta_h') AND name = @p1", name)
}

func (e *env) pendingNamed(name string) bool {
	e.t.Helper()
	pending, err := e.ws.Pending()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, en := range pending {
		if en.Name == name {
			return true
		}
	}
	return false
}

// TestCleanupFromJournal: a hypothetical index journaled "created" is found
// in Journal, not again by the pattern, and dropped; its entry turns
// "removed".
func TestCleanupFromJournal(t *testing.T) {
	e := newEnv(t)
	dbName, db := e.smallDB()
	name := runplan.IndexName(workspace.NewRunID(), 1)
	e.record(e.entry(dbName, workspace.KindHypotheticalIndex, name, workspace.StatePlanned))
	e.hypothetical(db, name)
	e.record(e.entry(dbName, workspace.KindHypotheticalIndex, name, workspace.StateCreated))

	f := e.find()
	if !has(f.Journal, workspace.KindHypotheticalIndex, name) {
		t.Fatalf("Journal %+v lacks %s", f.Journal, name)
	}
	if has(inDB(f.ByPattern, dbName), workspace.KindHypotheticalIndex, name) {
		t.Errorf("%s found again by the pattern: %+v", name, f.ByPattern)
	}
	removed := e.run(inDB(f.Journal, dbName))
	if len(removed) != 1 || removed[0].Name != name || removed[0].State != workspace.StateRemoved {
		t.Errorf("removed %+v, want %s removed", removed, name)
	}
	if n := indexCount(t, e.ctx, db, name); n != 0 {
		t.Errorf("%s still there after Run", name)
	}
	if e.pendingNamed(name) {
		t.Errorf("%s still pending in the journal", name)
	}
}

// TestCleanupByPattern: a hypothetical index and a statistics object of a run
// name, without journal, are found by the pattern, and Run removes them. The
// index's own statistics are not listed as statistics.
func TestCleanupByPattern(t *testing.T) {
	e := newEnv(t)
	dbName, db := e.smallDB()
	e.hypothetical(db, "godta_ab12cd34_1")
	e.exec(db, tsql.CreateStatistics(godtaH, "godta_ab12cd34_2", []string{"y"}))

	f := e.find()
	mine := inDB(f.ByPattern, dbName)
	if len(mine) != 2 || !has(mine, workspace.KindHypotheticalIndex, "godta_ab12cd34_1") || !has(mine, workspace.KindStatistics, "godta_ab12cd34_2") {
		t.Fatalf("ByPattern in %s: %+v, want the index _1 and the statistics _2 only", dbName, mine)
	}
	for _, en := range mine {
		if en.Server != e.inst.ServerName || en.Schema != "dbo" || en.Table != "godta_h" || en.RunID != "ab12cd34" {
			t.Errorf("entry %+v: wrong server, table or run ID", en)
		}
	}
	e.run(mine)
	if n := indexCount(t, e.ctx, db, "godta_ab12cd34_1"); n != 0 {
		t.Errorf("index still there after Run")
	}
	if n := count(t, e.ctx, db, "SELECT COUNT(*) FROM sys.stats WHERE name LIKE N'godta[_]ab12cd34[_]%'"); n != 0 {
		t.Errorf("%d statistics still there after Run", n)
	}
}

// TestCleanupLeavesForeign: a hypothetical index of the DTA is in Unknown,
// in no list Run takes, and is still there after Run has dropped goDTA's
// index on the same table.
func TestCleanupLeavesForeign(t *testing.T) {
	e := newEnv(t)
	dbName, db := e.smallDB()
	e.hypothetical(db, "_dta_index_x")
	// A goDTA index on the same table, so that Run does drop something there.
	ours := runplan.IndexName(workspace.NewRunID(), 1)
	e.hypothetical(db, ours)

	f := e.find()
	if has(f.Entries(), workspace.KindHypotheticalIndex, "_dta_index_x") {
		t.Fatalf("_dta_index_x is in a list Run takes: %+v", f.Entries())
	}
	listed := false
	for _, u := range f.Unknown {
		if strings.Contains(u, dbName) && strings.Contains(u, "_dta_index_x") {
			listed = true
		}
	}
	if !listed {
		t.Errorf("Unknown %q lacks _dta_index_x of %s", f.Unknown, dbName)
	}
	e.run(inDB(f.Entries(), dbName))
	if n := indexCount(t, e.ctx, db, ours); n != 0 {
		t.Errorf("%s still there after Run", ours)
	}
	if n := indexCount(t, e.ctx, db, "_dta_index_x"); n != 1 {
		t.Errorf("_dta_index_x: %d left, want 1", n)
	}
}

// TestCleanupIgnoresDataset: on the data set, none of its objects
// (godta_t, godta_filt, godta_v_cx) nor the godta_it_ database itself is
// found.
func TestCleanupIgnoresDataset(t *testing.T) {
	e := newEnv(t)
	dbName := ittest.NewDatabase(t, e.server, "")
	ittest.LoadDataset(t, e.opened(dbName))

	f := e.find()
	for _, en := range f.Entries() {
		if en.Database == dbName || en.Name == dbName {
			t.Errorf("found in the data set: %+v", en)
		}
	}
	for _, u := range f.Unknown {
		if strings.Contains(u, dbName) {
			t.Errorf("Unknown lists the data set: %s", u)
		}
	}
}

// TestCleanupAfterKilledSession: a session journals an index "planned",
// creates it, and is killed before it can journal "created" or drop it.
// Find returns it, Run drops it.
func TestCleanupAfterKilledSession(t *testing.T) {
	e := newEnv(t)
	dbName, db := e.smallDB()
	name := runplan.IndexName(workspace.NewRunID(), 1)

	victim, err := db.Conn(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	var spid int
	if err := victim.QueryRowContext(e.ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}
	e.record(e.entry(dbName, workspace.KindHypotheticalIndex, name, workspace.StatePlanned))
	e.hypothetical(victim, name)
	e.exec(e.server, "KILL "+strconv.Itoa(spid))
	if err := victim.PingContext(e.ctx); err == nil {
		t.Fatalf("session %d still alive after KILL", spid)
	}
	if n := indexCount(t, e.ctx, db, name); n != 1 {
		t.Fatalf("%s: %d after the kill, want 1 (left behind)", name, n)
	}

	f := e.find()
	if !has(f.Journal, workspace.KindHypotheticalIndex, name) {
		t.Fatalf("Journal %+v lacks %s", f.Journal, name)
	}
	e.run(inDB(f.Journal, dbName))
	if n := indexCount(t, e.ctx, db, name); n != 0 {
		t.Errorf("%s still there after Run", name)
	}
	if e.pendingNamed(name) {
		t.Errorf("%s still pending", name)
	}
}

// TestCleanupAfterBlockedDrop reproduces what TestDropRetriedThenJournaled
// (task 10) leaves: an index journaled "created" whose DROP failed on the
// lock timeout while another session held a shared table lock. The lock
// released, Run drops it and the entry turns "removed".
func TestCleanupAfterBlockedDrop(t *testing.T) {
	e := newEnv(t)
	dbName, db := e.smallDB()
	name := runplan.IndexName(workspace.NewRunID(), 1)
	e.record(e.entry(dbName, workspace.KindHypotheticalIndex, name, workspace.StatePlanned))
	e.hypothetical(db, name)
	e.record(e.entry(dbName, workspace.KindHypotheticalIndex, name, workspace.StateCreated))

	holder, err := db.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var one int
	if err := holder.QueryRowContext(e.ctx, "SELECT TOP (1) 1 FROM dbo.godta_h WITH (TABLOCK, HOLDLOCK)").Scan(&one); err != nil {
		holder.Rollback()
		t.Fatal(err)
	}
	dropper, err := db.Conn(e.ctx)
	if err != nil {
		holder.Rollback()
		t.Fatal(err)
	}
	e.exec(dropper, tsql.SetLockTimeout(300))
	_, dropErr := dropper.ExecContext(e.ctx, tsql.DropIndex(godtaH, name))
	dropper.Close()
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if dropErr == nil || !strings.Contains(dropErr.Error(), "1222") && !strings.Contains(strings.ToLower(dropErr.Error()), "lock request time out") {
		t.Fatalf("the blocked DROP gave %v, want a lock timeout (1222)", dropErr)
	}
	if n := indexCount(t, e.ctx, db, name); n != 1 {
		t.Fatalf("%s: %d after the blocked drop, want 1", name, n)
	}

	f := e.find()
	if !has(f.Journal, workspace.KindHypotheticalIndex, name) {
		t.Fatalf("Journal %+v lacks %s", f.Journal, name)
	}
	removed := e.run(inDB(f.Journal, dbName))
	if len(removed) != 1 || removed[0].State != workspace.StateRemoved {
		t.Errorf("removed %+v", removed)
	}
	if n := indexCount(t, e.ctx, db, name); n != 0 {
		t.Errorf("%s still there after Run", name)
	}
	if e.pendingNamed(name) {
		t.Errorf("%s still pending", name)
	}
}

// TestCleanupMissingBackupFile: a backup-file entry whose file no longer
// exists turns "removed" without error (xp_delete_file is not called on it).
func TestCleanupMissingBackupFile(t *testing.T) {
	e := newEnv(t)
	dir := strings.TrimRight(e.inst.DefaultBackupPath, `/\`)
	sep := "/"
	if strings.Contains(dir, `\`) {
		sep = `\`
	}
	run := workspace.NewRunID()
	path := dir + sep + "godta_" + run + "_missing.bak"
	if n := count(t, e.ctx, e.server, "DECLARE @r TABLE (f int, d int, p int); INSERT @r EXEC master.dbo.xp_fileexist "+tsql.QuoteString(path)+"; SELECT f FROM @r"); n != 0 {
		t.Fatalf("%s exists", path)
	}
	en := workspace.Entry{RunID: run, Server: e.inst.ServerName, Database: "master", Kind: workspace.KindBackupFile,
		Name: "godta_" + run + "_missing.bak", Path: path, At: time.Now().UTC(), State: workspace.StateCreated}
	e.record(en)

	f := e.find()
	var mine []workspace.Entry
	for _, j := range f.Journal {
		if j.Kind == workspace.KindBackupFile && j.Path == path {
			mine = append(mine, j)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("Journal %+v lacks the backup file", f.Journal)
	}
	removed := e.run(mine)
	if len(removed) != 1 || removed[0].Path != path || removed[0].State != workspace.StateRemoved {
		t.Errorf("removed %+v", removed)
	}
	if e.pendingNamed(en.Name) {
		t.Errorf("backup file still pending")
	}
}

// TestLikeUnderscoreEscaped: a statistics object godtaXab12cd34_1, which an
// unescaped godta_ pattern would match, is not found.
func TestLikeUnderscoreEscaped(t *testing.T) {
	e := newEnv(t)
	dbName, db := e.smallDB()
	e.exec(db, tsql.CreateStatistics(godtaH, "godtaXab12cd34_1", []string{"y"}))

	f := e.find()
	if mine := inDB(f.Entries(), dbName); len(mine) != 0 {
		t.Errorf("found %+v, want nothing", mine)
	}
	for _, u := range f.Unknown {
		if strings.Contains(u, "godtaXab12cd34_1") {
			t.Errorf("Unknown lists it: %s", u)
		}
	}
}
