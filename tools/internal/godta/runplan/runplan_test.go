package runplan_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

var inst = conn.Instance{ServerName: "SQL01", MachineName: "HOST01"}

const runID = "ab12cd34"

// workload has godta_h (x, y), object_id 100, and a second table t2 (k),
// object_id 200.
func workload() *model.Workload {
	return &model.Workload{
		Server:   inst,
		Database: model.DBInfo{ID: 5, Name: "Sales"},
		Tables: map[int]*model.Table{
			100: {ObjectID: 100, Schema: "dbo", Name: "godta_h", IsHeap: true,
				Columns: []model.Column{{ID: 1, Name: "x"}, {ID: 2, Name: "y"}}},
			200: {ObjectID: 200, Schema: "dbo", Name: "t2",
				Columns: []model.Column{{ID: 1, Name: "k"}}},
		},
	}
}

func fingerprints() []runplan.TableFingerprint {
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	return []runplan.TableFingerprint{
		{ObjectID: 100, Schema: "dbo", Name: "godta_h", ModifyDate: at, IndexesSHA256: "aa"},
		{ObjectID: 200, Schema: "dbo", Name: "t2", ModifyDate: at, IndexesSHA256: "bb"},
	}
}

// twoCandidates are (x) INCLUDE (y) and (x) on godta_h.
func twoCandidates() []model.Candidate {
	return []model.Candidate{
		{ID: "c1", Table: 100, Keys: []model.KeyCol{{ColumnID: 1}}, Include: []int{2}, Decision: model.DecisionNew},
		{ID: "c2", Table: 100, Keys: []model.KeyCol{{ColumnID: 1}}, Decision: model.DecisionNew},
	}
}

func sealedPlan() runplan.Plan {
	return runplan.ForEvaluate(workload(), twoCandidates(), runID, fingerprints(), 4, 2000).Seal()
}

func TestSealDetectsEdit(t *testing.T) {
	p := sealedPlan()
	if err := runplan.Verify(p, inst, "Sales", fingerprints()); err != nil {
		t.Fatalf("Verify of the sealed plan: %v", err)
	}
	// Through JSON, as the plan file goes: the checksum must survive it.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var read runplan.Plan
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatal(err)
	}
	if err := runplan.Verify(read, inst, "Sales", fingerprints()); err != nil {
		t.Fatalf("Verify of the plan read back from JSON: %v", err)
	}
	read.Steps[1].SQL = strings.Replace(read.Steps[1].SQL, "[x]", "[y]", 1)
	err = runplan.Verify(read, inst, "Sales", fingerprints())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || !errors.Is(err, runplan.ErrChecksum) {
		t.Fatalf("Verify of an edited plan: %v, want checksum mismatch", err)
	}
}

func TestVerifyWrongServer(t *testing.T) {
	p := sealedPlan()
	for _, other := range []conn.Instance{
		{ServerName: "SQL02", MachineName: "HOST01"},
		{ServerName: "SQL01", MachineName: "HOST02"},
	} {
		err := runplan.Verify(p, other, "Sales", fingerprints())
		if !errors.Is(err, runplan.ErrWrongServer) {
			t.Errorf("Verify on %+v: %v, want %v", other, err, runplan.ErrWrongServer)
		}
	}
	if err := runplan.Verify(p, inst, "Sales2", fingerprints()); !errors.Is(err, runplan.ErrWrongDatabase) {
		t.Errorf("Verify on another database: %v, want %v", err, runplan.ErrWrongDatabase)
	}
}

func TestVerifySchemaDrift(t *testing.T) {
	p := sealedPlan()
	live := fingerprints()
	live[0].IndexesSHA256 = "cc" // an index added to godta_h
	err := runplan.Verify(p, inst, "Sales", live)
	if !errors.Is(err, runplan.ErrSchemaDrift) {
		t.Fatalf("Verify after an index was added: %v, want %v", err, runplan.ErrSchemaDrift)
	}
	if !strings.Contains(err.Error(), "[dbo].[godta_h]") || !strings.Contains(err.Error(), "index definitions changed") {
		t.Errorf("error %q does not name the table and what changed", err)
	}
	if strings.Contains(err.Error(), "[t2]") {
		t.Errorf("error %q names a table that did not change", err)
	}

	live = fingerprints()
	live[1].ModifyDate = live[1].ModifyDate.Add(time.Second)
	err = runplan.Verify(p, inst, "Sales", live)
	if !errors.Is(err, runplan.ErrSchemaDrift) || !strings.Contains(err.Error(), "[dbo].[t2]: modify_date changed") {
		t.Errorf("Verify after modify_date moved: %v", err)
	}
}

func TestEvaluatePlanOrder(t *testing.T) {
	p := runplan.ForEvaluate(workload(), twoCandidates(), runID, fingerprints(), 4, 2000)
	tbl := tsql.Obj{Schema: "dbo", Name: "godta_h"}
	n1, n2 := "godta_ab12cd34_1", "godta_ab12cd34_2"
	want := []string{
		tsql.SetLockTimeout(2000),
		tsql.CreateHypothetical(tsql.IndexDef{Table: tbl, Name: n1, Keys: []tsql.KeyCol{{Name: "x"}}, Include: []string{"y"}}),
		tsql.CreateHypothetical(tsql.IndexDef{Table: tbl, Name: n2, Keys: []tsql.KeyCol{{Name: "x"}}}),
		tsql.SetLockTimeout(runplan.DropLockTimeoutMS),
		tsql.DropIndex(tbl, n1),
		tsql.DropIndex(tbl, n2),
	}
	if len(p.Steps) != len(want) {
		for _, s := range p.Steps {
			t.Logf("step %d: %s", s.N, s.SQL)
		}
		t.Fatalf("%d steps, want %d", len(p.Steps), len(want))
	}
	lastCreate, firstDrop := -1, -1
	for i, s := range p.Steps {
		if s.N != i+1 {
			t.Errorf("step %d numbered %d", i+1, s.N)
		}
		if s.SQL != want[i] {
			t.Errorf("step %d:\n got %s\nwant %s", i+1, s.SQL, want[i])
		}
		switch {
		case strings.HasPrefix(s.SQL, "CREATE "):
			lastCreate = i
			name := []string{n1, n2}[i-1]
			if s.Journal == nil || s.Journal.State != workspace.StatePlanned || s.Journal.Name != name ||
				s.Journal.Kind != workspace.KindHypotheticalIndex || s.Journal.Table != "godta_h" ||
				s.Journal.Database != "Sales" || s.Journal.Server != "SQL01" || s.Journal.RunID != runID {
				t.Errorf("step %d journal %+v, want a planned entry for %s", i+1, s.Journal, name)
			}
			if want := runplan.ResolveIndexID(100, name); s.Resolve != want {
				t.Errorf("step %d resolve %q, want %q", i+1, s.Resolve, want)
			}
			if strings.Contains(s.SQL, runplan.IndexIDMarker) {
				t.Errorf("step %d carries the index_id marker", i+1)
			}
		case strings.HasPrefix(s.SQL, "DROP "):
			if firstDrop < 0 {
				firstDrop = i
			}
			if s.Journal == nil || s.Journal.State != workspace.StateRemoved {
				t.Errorf("step %d journal %+v, want a removed entry", i+1, s.Journal)
			}
		}
	}
	// The combined measure, after every creation, needs every index: no drop
	// may come before the last creation of the table.
	if firstDrop < lastCreate {
		t.Errorf("a DROP (step %d) comes before the last creation (step %d)", firstDrop+1, lastCreate+1)
	}
	if want := "SELECT index_id FROM sys.indexes WHERE object_id = 100 AND name = N'godta_ab12cd34_1'"; p.Steps[1].Resolve != want {
		t.Errorf("resolve %q, want %q", p.Steps[1].Resolve, want)
	}
	if p.MaxCombined != 4 || p.LockTimeoutMS != 2000 {
		t.Errorf("max_combined %d, lock_timeout_ms %d", p.MaxCombined, p.LockTimeoutMS)
	}
}

// TestEvaluatePlanPerTable: each table gets its own creations then drops,
// in the order tables appear among the candidates.
func TestEvaluatePlanPerTable(t *testing.T) {
	cands := []model.Candidate{
		{ID: "c1", Table: 200, Keys: []model.KeyCol{{ColumnID: 1}}},
		{ID: "c2", Table: 100, Keys: []model.KeyCol{{ColumnID: 1}}},
	}
	p := runplan.ForEvaluate(workload(), cands, runID, fingerprints(), 4, 2000)
	var got []string
	for _, s := range p.Steps {
		got = append(got, strings.Fields(s.SQL)[0]+" "+s.Candidate)
	}
	want := []string{"SET ", "CREATE c1", "SET ", "DROP c1", "SET ", "CREATE c2", "SET ", "DROP c2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("steps %v, want %v", got, want)
	}
}

// TestEvaluateSkipsCovered: a covered candidate and one on a columnstore
// table get no step and are listed in Skipped; names stay dense.
func TestEvaluateSkipsCovered(t *testing.T) {
	w := workload()
	w.Tables[200].HasColumnstore = true
	cands := []model.Candidate{
		{ID: "cov", Table: 100, Keys: []model.KeyCol{{ColumnID: 2}}, Decision: model.DecisionCovered, Existing: "nix_h_x"},
		{ID: "cs", Table: 200, Keys: []model.KeyCol{{ColumnID: 1}}},
		{ID: "ok", Table: 100, Keys: []model.KeyCol{{ColumnID: 1}}},
	}
	p := runplan.ForEvaluate(w, cands, runID, nil, 4, 2000)
	for _, s := range p.Steps {
		if s.Candidate == "cov" || s.Candidate == "cs" {
			t.Errorf("step %d for skipped candidate %s", s.N, s.Candidate)
		}
	}
	if len(p.Steps) != 4 || !strings.Contains(p.Steps[1].SQL, "[godta_ab12cd34_1]") {
		t.Errorf("steps %+v", p.Steps)
	}
	if len(p.Skipped) != 2 {
		t.Errorf("skipped %v, want two", p.Skipped)
	}
}

func TestForCleanupSteps(t *testing.T) {
	entries := []workspace.Entry{
		{RunID: runID, Server: "SQL01", Database: "Sales", Kind: workspace.KindHypotheticalIndex, Schema: "dbo", Table: "godta_h", Name: "godta_ab12cd34_1", State: workspace.StateCreated},
		{RunID: runID, Server: "SQL01", Database: "Sales", Kind: workspace.KindStatistics, Schema: "dbo", Table: "godta_h", Name: "godta_ab12cd34_2", State: workspace.StatePlanned},
		{RunID: runID, Server: "SQL01", Database: "master", Kind: workspace.KindCloneDatabase, Name: "godta_ab12cd34_clone", State: workspace.StateCreated},
		{RunID: runID, Server: "SQL01", Database: "master", Kind: workspace.KindBackupFile, Path: "/var/opt/mssql/data/godta_ab12cd34.bak", State: workspace.StateCreated},
		{RunID: runID, Server: "SQL02", Database: "Sales", Kind: workspace.KindHypotheticalIndex, Schema: "dbo", Table: "t", Name: "godta_ab12cd34_9", State: workspace.StateCreated},
	}
	p := runplan.ForCleanup(entries)
	h := tsql.Obj{Schema: "dbo", Name: "godta_h"}
	want := []string{
		tsql.DropIndex(h, "godta_ab12cd34_1"),
		tsql.DropStatistics(h, "godta_ab12cd34_2"),
		tsql.DropDatabase("godta_ab12cd34_clone"),
		tsql.DeleteBackupFile("/var/opt/mssql/data/godta_ab12cd34.bak"),
	}
	if len(p.Steps) != len(want) {
		t.Fatalf("%d steps, want %d: %+v", len(p.Steps), len(want), p.Steps)
	}
	for i, s := range p.Steps {
		if s.SQL != want[i] {
			t.Errorf("step %d: %s, want %s", i+1, s.SQL, want[i])
		}
		if s.Journal == nil || s.Journal.State != workspace.StateRemoved || s.Journal.Database != entries[i].Database {
			t.Errorf("step %d journal %+v", i+1, s.Journal)
		}
	}
	if p.Server != "SQL01" || len(p.Skipped) != 1 {
		t.Errorf("server %q, skipped %v", p.Server, p.Skipped)
	}
	// MachineName is the caller's to set: unset, Verify refuses.
	if err := runplan.Verify(p.Seal(), inst, "", nil); !errors.Is(err, runplan.ErrWrongServer) {
		t.Errorf("Verify without MachineName: %v", err)
	}
	p.MachineName = inst.MachineName
	if err := runplan.Verify(p.Seal(), inst, "", nil); err != nil {
		t.Errorf("Verify with MachineName: %v", err)
	}
}

func TestPlanHasNoSecret(t *testing.T) {
	const password, user = "Pw-7f3a'kQ9!zz", "godta_svc_x81"
	t.Setenv("GODTA_SERVER", "SQL01")
	t.Setenv("GODTA_AUTH", "sql")
	t.Setenv("GODTA_USER", user)
	t.Setenv("GODTA_MODE", "readwrite")
	t.Setenv(conn.PasswordEnv, password)
	prof, ok, err := conn.ProfileFromEnv(os.Getenv)
	if err != nil || !ok {
		t.Fatalf("ProfileFromEnv: %v, %v", ok, err)
	}
	if prof.User != user || prof.PasswordEnv != conn.PasswordEnv {
		t.Fatalf("profile %+v is not the sql profile of the test", prof)
	}
	w := workload()
	w.Server.ServerName = prof.Server
	entries := []workspace.Entry{{RunID: runID, Server: prof.Server, Database: "Sales",
		Kind: workspace.KindHypotheticalIndex, Schema: "dbo", Table: "godta_h", Name: "godta_ab12cd34_1", State: workspace.StateCreated}}
	for _, p := range []runplan.Plan{
		runplan.ForEvaluate(w, twoCandidates(), runID, fingerprints(), 4, 2000).Seal(),
		runplan.ForCleanup(entries).Seal(),
	} {
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, secret := range []string{password, strings.ReplaceAll(password, "'", "''"), user} {
			if strings.Contains(text, secret) {
				t.Errorf("%s plan JSON contains %q", p.Phase, secret)
			}
		}
	}
}

// TestForCloneSteps: statistics, clone, drops under the short lock timeout,
// backup, transfer, drop of the clone and of the file, in that order, each
// object journaled with its state, and the side effects of spec 8.4.
func TestForCloneSteps(t *testing.T) {
	w := workload()
	w.Tables[100].Columns = append(w.Tables[100].Columns, model.Column{ID: 3, Name: "z"})
	cands := []model.Candidate{{ID: "c1", Table: 100, Keys: []model.KeyCol{{ColumnID: 1}, {ColumnID: 3}}, Decision: model.DecisionNew}}
	p := runplan.ForClone(w, cands, runID, fingerprints(), `/var/opt/mssql/data/`, true, 0)
	clone := "godta_" + runID + "_c"
	path := "/var/opt/mssql/data/" + clone + ".bak"
	want := []struct {
		sql, kind, state string
	}{
		{"CREATE STATISTICS [godta_ab12cd34_s1] ON [dbo].[godta_h] ([x], [z])", workspace.KindStatistics, workspace.StatePlanned},
		{"CREATE STATISTICS [godta_ab12cd34_s2] ON [dbo].[godta_h] ([x])", workspace.KindStatistics, workspace.StatePlanned},
		{"CREATE STATISTICS [godta_ab12cd34_s3] ON [dbo].[godta_h] ([z])", workspace.KindStatistics, workspace.StatePlanned},
		{"DBCC CLONEDATABASE([Sales], [" + clone + "]) WITH NO_QUERYSTORE, NO_INFOMSGS", workspace.KindCloneDatabase, workspace.StatePlanned},
		{"SET LOCK_TIMEOUT 1000", "", ""},
		{"DROP STATISTICS [dbo].[godta_h].[godta_ab12cd34_s1]", workspace.KindStatistics, workspace.StateRemoved},
		{"DROP STATISTICS [dbo].[godta_h].[godta_ab12cd34_s2]", workspace.KindStatistics, workspace.StateRemoved},
		{"DROP STATISTICS [dbo].[godta_h].[godta_ab12cd34_s3]", workspace.KindStatistics, workspace.StateRemoved},
		{"SET LOCK_TIMEOUT -1", "", ""},
		{"BACKUP DATABASE [" + clone + "] TO DISK = N'" + path + "' WITH INIT, COPY_ONLY, COMPRESSION", workspace.KindBackupFile, workspace.StatePlanned},
		{"SELECT BulkColumn FROM OPENROWSET(BULK N'" + path + "', SINGLE_BLOB) AS x", "", ""},
		{"DROP DATABASE [" + clone + "]", workspace.KindCloneDatabase, workspace.StateRemoved},
		{"EXEC master.dbo.xp_delete_file 0, N'" + path + "'", workspace.KindBackupFile, workspace.StateRemoved},
	}
	if len(p.Steps) != len(want) {
		for _, s := range p.Steps {
			t.Logf("step %d: %s", s.N, s.SQL)
		}
		t.Fatalf("%d steps, want %d", len(p.Steps), len(want))
	}
	for i, s := range p.Steps {
		w := want[i]
		if s.N != i+1 || s.SQL != w.sql {
			t.Errorf("step %d (N %d): %q, want %q", i+1, s.N, s.SQL, w.sql)
		}
		if w.kind == "" {
			if s.Journal != nil {
				t.Errorf("step %d: journal %+v, want none", i+1, *s.Journal)
			}
			continue
		}
		if s.Journal == nil {
			t.Errorf("step %d: no journal entry", i+1)
			continue
		}
		e := *s.Journal
		if e.Kind != w.kind || e.State != w.state || e.RunID != runID || e.Server != "SQL01" {
			t.Errorf("step %d: journal %+v, want kind %s state %s", i+1, e, w.kind, w.state)
		}
		switch e.Kind {
		case workspace.KindStatistics:
			if e.Database != "Sales" || e.Schema != "dbo" || e.Table != "godta_h" || !tsql.IsRunName(e.Name) {
				t.Errorf("step %d: statistics entry %+v", i+1, e)
			}
		case workspace.KindBackupFile:
			if e.Path != path {
				t.Errorf("step %d: backup path %q, want %q", i+1, e.Path, path)
			}
		case workspace.KindCloneDatabase:
			if e.Name != clone {
				t.Errorf("step %d: clone name %q, want %q", i+1, e.Name, clone)
			}
		}
	}
	if p.Phase != runplan.PhaseClone || p.Database != "Sales" || p.MachineName != "HOST01" {
		t.Errorf("plan header %s %s %s", p.Phase, p.Database, p.MachineName)
	}
	if p.MaxBytes != runplan.MaxFileBytes || p.ChunkBytes != runplan.DefaultChunkBytes {
		t.Errorf("limits max %d chunk %d, want %d and %d", p.MaxBytes, p.ChunkBytes, runplan.MaxFileBytes, runplan.DefaultChunkBytes)
	}
	effects := strings.Join(p.SideEffects, "\n")
	for _, s := range []string{"Sch-S", "Sch-M", "recompile", "ceil(size / chunk_bytes) + 1", "sysadmin", "COPY_ONLY", "compressed", "xp_delete_file", path} {
		if !strings.Contains(effects, s) {
			t.Errorf("side effects lack %q:\n%s", s, effects)
		}
	}
	if err := runplan.Verify(p.Seal(), inst, "Sales", fingerprints()); err != nil {
		t.Errorf("Verify of the sealed clone plan: %v", err)
	}
}

// TestForCloneUncompressedWindows: without compression the BACKUP has no
// COMPRESSION, a Windows directory keeps its backslash, and a maxBytes is
// kept.
func TestForCloneUncompressedWindows(t *testing.T) {
	p := runplan.ForClone(workload(), nil, runID, fingerprints(), `D:\Backup`, false, 1000)
	var backup string
	for _, s := range p.Steps {
		if strings.HasPrefix(s.SQL, "BACKUP") {
			backup = s.SQL
		}
	}
	want := `BACKUP DATABASE [godta_ab12cd34_c] TO DISK = N'D:\Backup\godta_ab12cd34_c.bak' WITH INIT, COPY_ONLY`
	if backup != want {
		t.Errorf("backup %q, want %q", backup, want)
	}
	if p.MaxBytes != 1000 {
		t.Errorf("MaxBytes %d, want 1000", p.MaxBytes)
	}
	if !strings.Contains(strings.Join(p.SideEffects, "\n"), "uncompressed") {
		t.Errorf("side effects do not say the backup is uncompressed: %v", p.SideEffects)
	}
}

// TestCloneStatsJoinAndGroupColumns: a column a query only joins on, and one
// it only groups by, each get a single-column statistics (spec 8.4, step 1).
// Each case has one plan that names the column in that role alone.
func TestCloneStatsJoinAndGroupColumns(t *testing.T) {
	x := showplan.ColRef{Database: "Sales", Schema: "dbo", Table: "godta_h", Alias: "h", Column: "x"}
	k := showplan.ColRef{Database: "Sales", Schema: "dbo", Table: "t2", Column: "k"}
	for _, tc := range []struct {
		name  string
		st    showplan.Statement
		table int
	}{
		{"join", showplan.Statement{JoinCols: []showplan.ColRef{x}}, 100},
		{"group", showplan.Statement{GroupCols: []showplan.ColRef{k}}, 200},
	} {
		w := workload()
		w.Queries = []model.Query{{QueryID: 1, Plan: &showplan.Plan{Statements: []showplan.Statement{tc.st}}}}
		got := runplan.CloneStats(w, nil)
		if len(got) != 1 || got[0].Table != tc.table || len(got[0].Columns) != 1 || got[0].Columns[0] != 1 || got[0].Multi {
			t.Errorf("%s: CloneStats %+v, want one statistics on table %d column 1", tc.name, got, tc.table)
		}
	}
}
