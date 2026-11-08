package lab_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// cloneFixture is a throwaway database loaded with the data set, a pool on
// it, the instance, and a workload of godta_t with the candidate (a, b).
type cloneFixture struct {
	server, db *sql.DB
	inst       conn.Instance
	name       string
	w          *model.Workload
	cands      []model.Candidate
	fp         []runplan.TableFingerprint
	ws         workspace.WS
}

func newCloneFixture(t *testing.T) *cloneFixture {
	t.Helper()
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	f := &cloneFixture{server: ittest.OpenDB(t, p, r, "")}
	f.name = ittest.NewDatabase(t, f.server, "")
	f.db = ittest.OpenDB(t, p, r, f.name)
	ittest.LoadDataset(t, f.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var err error
	if f.inst, err = conn.Identify(ctx, f.server); err != nil {
		t.Fatal(err)
	}
	tbl := &model.Table{Schema: "dbo", Name: "godta_t"}
	if err := f.db.QueryRowContext(ctx, "SELECT OBJECT_ID(N'dbo.godta_t')").Scan(&tbl.ObjectID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.QueryContext(ctx, "SELECT column_id, name, TYPE_NAME(user_type_id) FROM sys.columns WHERE object_id = @p1 ORDER BY column_id", tbl.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c model.Column
		if err := rows.Scan(&c.ID, &c.Name, &c.Type); err != nil {
			t.Fatal(err)
		}
		tbl.Columns = append(tbl.Columns, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	col := func(name string) int {
		for _, c := range tbl.Columns {
			if c.Name == name {
				return c.ID
			}
		}
		t.Fatalf("no column %s", name)
		return 0
	}
	f.w = &model.Workload{
		Server:   f.inst,
		Database: model.DBInfo{Name: f.name},
		Tables:   map[int]*model.Table{tbl.ObjectID: tbl},
	}
	f.cands = []model.Candidate{{ID: "c1", Table: tbl.ObjectID, Keys: []model.KeyCol{{ColumnID: col("a")}, {ColumnID: col("b")}}, Decision: model.DecisionNew}}
	if f.fp, err = runplan.ReadFingerprints(ctx, f.db, runplan.Tables(f.w)); err != nil {
		t.Fatal(err)
	}
	if f.ws, err = workspace.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return f
}

// plan builds the clone plan of the fixture, and registers the removal of
// whatever a failed Clone would leave on the server.
func (f *cloneFixture) plan(t *testing.T, maxBytes, chunk int64) runplan.Plan {
	t.Helper()
	runID := workspace.NewRunID()
	p := runplan.ForClone(f.w, f.cands, runID, f.fp, f.inst.DefaultBackupPath, f.inst.CanCompress, maxBytes)
	if chunk > 0 {
		p.ChunkBytes = chunk
	}
	clone := runplan.CloneDatabaseName(runID)
	path := runplan.BackupPath(f.inst.DefaultBackupPath, runID)
	dropOnCleanup(t, f.server, clone)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if fileExists(t, ctx, f.server, path) {
			if _, err := f.server.ExecContext(ctx, tsql.DeleteBackupFile(path)); err != nil {
				t.Errorf("deleting %s: %v", path, err)
			}
		}
	})
	return p.Seal()
}

func fileExists(t *testing.T, ctx context.Context, q conn.Querier, path string) bool {
	t.Helper()
	var file, dir, parent int
	if err := q.QueryRowContext(ctx, "EXEC master.dbo.xp_fileexist "+tsql.QuoteString(path)).Scan(&file, &dir, &parent); err != nil {
		t.Fatalf("xp_fileexist %s: %v", path, err)
	}
	return file == 1
}

// checkNothingLeft: no statistics of goDTA's run form on the source, no
// database of that form, no backup file of the run on the server, and every
// journaled object removed (with statistics, the clone and the file among
// them, so that "nothing left" is not "nothing done").
func (f *cloneFixture) checkNothingLeft(t *testing.T, p runplan.Plan) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var n int
	if err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.stats WHERE name LIKE @p1", tsql.RunPattern()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d statistics LIKE %s left on the source", n, tsql.RunPattern())
	}
	var dbs []string
	rows, err := f.server.QueryContext(ctx, "SELECT name FROM sys.databases WHERE name LIKE @p1", tsql.RunPattern())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		dbs = append(dbs, s)
	}
	rows.Close()
	if len(dbs) != 0 {
		t.Errorf("databases LIKE %s left: %v", tsql.RunPattern(), dbs)
	}
	path := runplan.BackupPath(f.inst.DefaultBackupPath, p.RunID)
	if fileExists(t, ctx, f.server, path) {
		t.Errorf("backup file %s left on the server", path)
	}
	pending, err := f.ws.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("journal: %d objects not removed: %+v", len(pending), pending)
	}
	data, err := os.ReadFile(filepath.Join(f.ws.Root, workspace.JournalFile))
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e workspace.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("journal line %q: %v", line, err)
		}
		if e.State == workspace.StateCreated {
			created[e.Kind]++
		}
	}
	// The fixture's candidate (a, b) gives (a, b), (a) and (b).
	want := map[string]int{workspace.KindStatistics: 3, workspace.KindCloneDatabase: 1, workspace.KindBackupFile: 1}
	for kind, n := range want {
		if created[kind] != n {
			t.Errorf("journal: %d %s entries created, want %d", created[kind], kind, n)
		}
	}
}

func sha256File(t *testing.T, path string) (string, int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data))
}

func TestCloneRoundTrip(t *testing.T) {
	f := newCloneFixture(t)
	p := f.plan(t, 0, 0)
	hostDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	file, err := lab.Clone(ctx, f.db, f.ws, p, hostDir)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Logf("file %s, %d bytes, %d chunk(s)", file.HostPath, file.Bytes, file.Chunks)
	if filepath.Dir(file.HostPath) != hostDir {
		t.Errorf("host path %s not in %s", file.HostPath, hostDir)
	}
	sum, size := sha256File(t, file.HostPath)
	if sum != file.SHA256 || size != file.Bytes {
		t.Errorf("host file %s: sha256 %s, %d bytes; Clone said %s, %d bytes", file.HostPath, sum, size, file.SHA256, file.Bytes)
	}
	st, err := lab.LoadState(f.ws, f.ws.DBDir(p.Server, p.Database))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.File != file || st.Prepared != nil {
		t.Errorf("lab.json has %+v, want File %+v and no Prepared", st, file)
	}
	f.checkNothingLeft(t, p)
}

func TestCloneChunked(t *testing.T) {
	hostDir, containerDir := os.Getenv("GODTA_TEST_LAB_HOSTDIR"), os.Getenv("GODTA_TEST_LAB_CONTAINERDIR")
	if hostDir == "" || containerDir == "" {
		t.Skip("GODTA_TEST_LAB_HOSTDIR and GODTA_TEST_LAB_CONTAINERDIR not set")
	}
	f := newCloneFixture(t)
	p := f.plan(t, 0, 64<<10)
	t.Cleanup(func() { os.Remove(filepath.Join(hostDir, runplan.CloneDatabaseName(p.RunID)+".bak")) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	file, err := lab.Clone(ctx, f.db, f.ws, p, hostDir)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Logf("file %s, %d bytes, %d chunk(s) of %d", file.HostPath, file.Bytes, file.Chunks, p.ChunkBytes)
	if file.Chunks < 2 {
		t.Errorf("%d chunk(s), want at least 2 for a %d-byte file in chunks of %d", file.Chunks, file.Bytes, p.ChunkBytes)
	}
	if want := int((file.Bytes + p.ChunkBytes - 1) / p.ChunkBytes); file.Chunks != want {
		t.Errorf("%d chunks, want ceil(%d / %d) = %d", file.Chunks, file.Bytes, p.ChunkBytes, want)
	}
	sum, size := sha256File(t, file.HostPath)
	if sum != file.SHA256 || size != file.Bytes {
		t.Errorf("host file: sha256 %s, %d bytes; Clone said %s, %d bytes", sum, size, file.SHA256, file.Bytes)
	}
	f.checkNothingLeft(t, p)

	// The lab sees the file through its mount: it must be a valid backup.
	lp, lr := ittest.Profile(t, "GODTA_TEST_LAB_PROFILE")
	labDB := ittest.OpenDB(t, lp, lr, "")
	seen := strings.TrimRight(containerDir, "/") + "/" + filepath.Base(file.HostPath)
	if _, err := labDB.ExecContext(ctx, "RESTORE VERIFYONLY FROM DISK = "+tsql.QuoteString(seen)); err != nil {
		t.Errorf("RESTORE VERIFYONLY of %s on the lab: %v", seen, err)
	}
}

func TestCloneRefusesTooBig(t *testing.T) {
	f := newCloneFixture(t)
	p := f.plan(t, 1, 0)
	hostDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, err := lab.Clone(ctx, f.db, f.ws, p, hostDir)
	if !errors.Is(err, lab.ErrTooBig) {
		t.Fatalf("Clone with max_bytes 1: %v, want ErrTooBig", err)
	}
	t.Logf("refused: %v", err)
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("host directory has %d entries after the refusal, want none", len(entries))
	}
	if _, err := lab.LoadState(f.ws, f.ws.DBDir(p.Server, p.Database)); err == nil {
		t.Error("lab.json written after a refused clone")
	}
	f.checkNothingLeft(t, p)
}

// TestCloneLeavesLockedStatistics: a second connection holds a shared lock
// on the table, so every DROP STATISTICS times out; after three attempts each
// stays "created" in the journal, and Clone still returns the file, with
// ErrLeftForCleanup. Released, the statistics are there to clean up.
func TestCloneLeavesLockedStatistics(t *testing.T) {
	f := newCloneFixture(t)
	p := f.plan(t, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	holder, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, "BEGIN TRAN; SELECT TOP (1) id FROM dbo.godta_t WITH (TABLOCK, HOLDLOCK);"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	file, err := lab.Clone(ctx, f.db, f.ws, p, t.TempDir())
	t.Logf("Clone took %s: %v", time.Since(start).Round(time.Second), err)
	if !errors.Is(err, lab.ErrLeftForCleanup) {
		t.Fatalf("Clone under a held lock: %v, want ErrLeftForCleanup", err)
	}
	// Three attempts per statistics, two pauses of 2 s between them: at
	// least 12 s for the three, whatever the lock timeouts add.
	if elapsed := time.Since(start); elapsed < 12*time.Second {
		t.Errorf("Clone gave up in %s, too soon for three attempts 2 s apart on each of three statistics", elapsed)
	}
	if file.HostPath == "" || file.Bytes == 0 {
		t.Errorf("Clone returned no file with ErrLeftForCleanup: %+v", file)
	}
	if _, err := holder.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	pending, err := f.ws.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var stats int
	for _, e := range pending {
		if e.Kind != workspace.KindStatistics || e.State != workspace.StateCreated {
			t.Errorf("pending %s %s in state %s, want only created statistics", e.Kind, e.Name, e.State)
			continue
		}
		stats++
		if _, err := f.db.ExecContext(ctx, tsql.DropStatistics(tsql.Obj{Schema: e.Schema, Name: e.Table}, e.Name)); err != nil {
			t.Errorf("dropping %s after the release: %v", e.Name, err)
		}
	}
	if stats != 3 {
		t.Errorf("%d statistics left for cleanup, want 3", stats)
	}
}

// TestCloneRefusesEditedPlan: a plan changed after Seal is refused before
// any statement or journal entry.
func TestCloneRefusesEditedPlan(t *testing.T) {
	f := newCloneFixture(t)
	p := f.plan(t, 0, 0)
	p.Steps[0].SQL += " WITH FULLSCAN"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, err := lab.Clone(ctx, f.db, f.ws, p, t.TempDir())
	if !errors.Is(err, runplan.ErrChecksum) {
		t.Fatalf("Clone of an edited plan: %v, want ErrChecksum", err)
	}
	if _, err := os.Stat(filepath.Join(f.ws.Root, workspace.JournalFile)); !os.IsNotExist(err) {
		t.Errorf("journal written for a refused plan (%v)", err)
	}
}
