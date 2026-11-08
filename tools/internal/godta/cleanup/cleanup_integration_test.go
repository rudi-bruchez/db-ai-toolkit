package cleanup_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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

// find runs Find limited to dbs, the test's own databases, as cleanup
// --include does: it never reads the other databases of the instance,
// which other tests create and drop meanwhile.
func (e *env) find(dbs ...string) cleanup.Found {
	e.t.Helper()
	f, err := cleanup.Find(e.ctx, e.server, e.open, e.ws, only(dbs...))
	if err != nil {
		e.t.Fatalf("Find: %v", err)
	}
	return f
}

func only(dbs ...string) func(string) bool {
	return func(db string) bool { return slices.Contains(dbs, db) }
}

// run builds, seals and runs the cleanup plan of entries.
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

	f := e.find(dbName)
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

	f := e.find(dbName)
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

	f := e.find(dbName)
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

	f := e.find(dbName)
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

	f := e.find(dbName)
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

	f := e.find(dbName)
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
	// The source database need not exist: Find keeps the file for it.
	source := "godta_it_" + run
	en := workspace.Entry{RunID: run, Server: e.inst.ServerName, Database: "master", Kind: workspace.KindBackupFile,
		Name: "godta_" + run + "_missing.bak", Path: path, Source: source, At: time.Now().UTC(), State: workspace.StateCreated}
	e.record(en)

	f := e.find(source)
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

	f := e.find(dbName)
	if mine := inDB(f.Entries(), dbName); len(mine) != 0 {
		t.Errorf("found %+v, want nothing", mine)
	}
	for _, u := range f.Unknown {
		if strings.Contains(u, "godtaXab12cd34_1") {
			t.Errorf("Unknown lists it: %s", u)
		}
	}
}

// TestFindOpensSelectedOnly: Find limited to one database opens that
// database alone, and finds nothing in the others.
func TestFindOpensSelectedOnly(t *testing.T) {
	e := newEnv(t)
	a, _ := e.smallDB()
	b, dbB := e.smallDB()
	e.hypothetical(dbB, runplan.IndexName(workspace.NewRunID(), 1))
	var opened []string
	open := func(db string) (*sql.DB, error) {
		opened = append(opened, db)
		return e.open(db)
	}
	f, err := cleanup.Find(e.ctx, e.server, open, e.ws, only(a))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(opened, []string{a}) {
		t.Errorf("opened %q, want %s alone", opened, a)
	}
	if mine := inDB(f.Entries(), b); len(mine) > 0 {
		t.Errorf("found in %s, which is not selected: %+v", b, mine)
	}
}

// TestFindUnreadableIsWarning: a selected database that cannot be opened is
// a warning naming it; Find still lists what the others hold.
func TestFindUnreadableIsWarning(t *testing.T) {
	e := newEnv(t)
	a, dbA := e.smallDB()
	b, _ := e.smallDB()
	ours := runplan.IndexName(workspace.NewRunID(), 1)
	e.hypothetical(dbA, ours)
	open := func(db string) (*sql.DB, error) {
		if db == b {
			return nil, errors.New("simulated: the database cannot be opened")
		}
		return e.open(db)
	}
	f, err := cleanup.Find(e.ctx, e.server, open, e.ws, only(a, b))
	if err != nil {
		t.Fatalf("Find failed on one unreadable database: %v", err)
	}
	if !slices.ContainsFunc(f.Warnings, func(w string) bool { return strings.Contains(w, b) && strings.Contains(w, "simulated") }) {
		t.Errorf("warnings %q do not name %s", f.Warnings, b)
	}
	if !has(inDB(f.Entries(), a), workspace.KindHypotheticalIndex, ours) {
		t.Errorf("%s of %s not found: %+v", ours, a, f.Entries())
	}
}

// TestCleanupIncludeKeepsClone: limited to a database, Find keeps the clone
// database and the backup file journaled from it (Source), and lists with a
// warning, never silently, those whose source it cannot tell: a journal
// entry without Source, a clone found by its name only.
func TestCleanupIncludeKeepsClone(t *testing.T) {
	e := newEnv(t)
	dbName, _ := e.smallDB()
	run, old := workspace.NewRunID(), workspace.NewRunID()
	clone := runplan.CloneDatabaseName(run)
	server := func(kind, name, path, source string, r string) workspace.Entry {
		return workspace.Entry{RunID: r, Server: e.inst.ServerName, Database: cleanup.ServerDatabase, Kind: kind,
			Name: name, Path: path, Source: source, At: time.Now().UTC(), State: workspace.StateCreated}
	}
	e.record(
		server(workspace.KindCloneDatabase, clone, "", dbName, run),
		server(workspace.KindBackupFile, clone+".bak", "/nonexistent/"+clone+".bak", dbName, run),
		server(workspace.KindCloneDatabase, runplan.CloneDatabaseName(old), "", "", old),
	)
	found := runplan.CloneDatabaseName(workspace.NewRunID())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := e.server.ExecContext(ctx, "IF DB_ID("+tsql.QuoteString(found)+") IS NOT NULL "+tsql.DropDatabase(found)+";"); err != nil {
			t.Errorf("dropping %s: %v", found, err)
		}
	})
	e.exec(e.server, tsql.CloneDatabase(dbName, found))

	f := e.find(dbName)
	if !has(f.Journal, workspace.KindCloneDatabase, clone) || !has(f.Journal, workspace.KindBackupFile, clone+".bak") {
		t.Errorf("Journal %+v lacks the clone database and the backup file of %s", f.Journal, dbName)
	}
	for _, name := range []string{runplan.CloneDatabaseName(old), found} {
		if has(f.Entries(), workspace.KindCloneDatabase, name) {
			t.Errorf("%s, of unknown source, is in the plan of %s", name, dbName)
		}
		if !slices.ContainsFunc(f.Warnings, func(w string) bool { return strings.Contains(w, name) && strings.Contains(w, "without them") }) {
			t.Errorf("warnings %q do not list %s", f.Warnings, name)
		}
	}
}
