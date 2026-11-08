package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// The password of the unit test profiles, from this variable.
const testPasswordEnv = "GODTA_UNIT_TEST_PASSWORD"

// writeProfiles writes a profiles file with a readonly and a readwrite
// profile, both SQL logins reading testPasswordEnv.
func writeProfiles(t *testing.T) string {
	t.Helper()
	body := `{
  "ro": {"server": "lab01", "database": "master", "auth": "sql", "user": "u", "passwordEnv": "` + testPasswordEnv + `", "mode": "readonly"},
  "rw": {"server": "lab01", "database": "master", "auth": "sql", "user": "u", "passwordEnv": "` + testPasswordEnv + `", "mode": "readwrite", "environment": "lab"}
}`
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// noOpen is an opener that fails the test if it is called: no connection
// may be opened.
func noOpen(t *testing.T) opener {
	return func(context.Context, sqlq.Profile, string, string) (*sql.DB, error) {
		t.Errorf("a connection was opened")
		return nil, errors.New("no connection in this test")
	}
}

// invoke runs the binary's run with the opener and returns the exit code
// and the decoded output, failing the test when stdout is not one JSON
// object.
func invoke(t *testing.T, open opener, args ...string) (int, output) {
	t.Helper()
	var buf bytes.Buffer
	d := deps{
		stdout:  &buf,
		open:    open,
		resolve: sqlq.NewResolver(os.Getenv, "", func(string) {}).Resolve,
	}
	code := run(context.Background(), args, d)
	var out output
	dec := json.NewDecoder(&buf)
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, buf.String())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("more than one JSON object on stdout: %s", buf.String())
	}
	return code, out
}

// baseArgs are the flags every test passes: a fresh project, the profiles
// file, a missing .env.
func baseArgs(t *testing.T, profiles, profile string) []string {
	t.Helper()
	dir := t.TempDir()
	return []string{"--project", filepath.Join(dir, "project"), "--profiles", profiles,
		"--profile", profile, "--env", filepath.Join(dir, "absent.env")}
}

// writeCommands are the write phases on the source, with their flags.
var writeCommands = [][]string{
	{"evaluate", "--target", "server"},
	{"cleanup"},
	{"clone", "--host-dir", "/nonexistent"},
}

func TestWriteRefusedOnReadonly(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	profiles := writeProfiles(t)
	for _, cmd := range writeCommands {
		args := append(append(append([]string{}, cmd...), baseArgs(t, profiles, "ro")...),
			"--allow-write", "--plan", filepath.Join(t.TempDir(), "plan.json"))
		code, out := invoke(t, noOpen(t), args...)
		if code != exitRefused || out.OK || out.Error == nil || !strings.Contains(out.Error.Message, "read-only") {
			t.Errorf("%s on a readonly profile: code %d, output %+v; want code 3, refused as read-only", cmd[0], code, out)
		}
	}
}

func TestWriteRefusedWithoutFlag(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	profiles := writeProfiles(t)
	for _, cmd := range writeCommands {
		args := append(append(append([]string{}, cmd...), baseArgs(t, profiles, "rw")...),
			"--plan", filepath.Join(t.TempDir(), "plan.json"))
		code, out := invoke(t, noOpen(t), args...)
		if code != exitRefused || out.OK || out.Error == nil || !strings.Contains(out.Error.Message, "--allow-write") {
			t.Errorf("%s without --allow-write: code %d, output %+v; want code 3", cmd[0], code, out)
		}
	}
}

func TestWriteRefusedWithoutPlan(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	profiles := writeProfiles(t)
	for _, cmd := range writeCommands {
		args := append(append(append([]string{}, cmd...), baseArgs(t, profiles, "rw")...), "--allow-write")
		code, out := invoke(t, noOpen(t), args...)
		if code != exitRefused || out.OK || out.Error == nil || !strings.Contains(out.Error.Message, "--plan") {
			t.Errorf("%s without --plan: code %d, output %+v; want code 3", cmd[0], code, out)
		}
	}
}

func TestJSONOnFailure(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	profiles := writeProfiles(t)
	refused := func(context.Context, sqlq.Profile, string, string) (*sql.DB, error) {
		return nil, errors.New("dial tcp 10.0.0.1:1433: connect: connection refused")
	}
	code, out := invoke(t, refused, append([]string{"collect"}, baseArgs(t, profiles, "ro")...)...)
	if code != exitConnect {
		t.Errorf("code %d, want %d", code, exitConnect)
	}
	if out.OK || out.Command != "collect" || out.Error == nil || out.Error.Kind != "connection" ||
		!strings.Contains(out.Error.Message, "connection refused") || out.Databases == nil {
		t.Errorf("output %+v: want ok false, command collect, the connection error and a databases list", out)
	}
}

func TestEnvFileLoaded(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "godta.env")
	if err := os.WriteFile(envFile, []byte("GODTA_SERVER=from-file\nGODTA_AUTH=sql\nGODTA_USER=u\nGODTA_PASSWORD=pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every key of the file is unset for the test and restored after it.
	for _, k := range []string{"GODTA_SERVER", "GODTA_AUTH", "GODTA_USER", "GODTA_PASSWORD"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	var got string
	record := func(_ context.Context, p sqlq.Profile, _, _ string) (*sql.DB, error) {
		got = p.Server
		return nil, errors.New("not connecting")
	}
	project := filepath.Join(t.TempDir(), "project")
	args := []string{"collect", "--project", project, "--env", envFile}

	invoke(t, record, args...)
	if got != "from-file" {
		t.Errorf("no variable set: the profile's server is %q, want the file's from-file", got)
	}

	for _, k := range []string{"GODTA_SERVER", "GODTA_AUTH", "GODTA_USER", "GODTA_PASSWORD"} {
		os.Unsetenv(k)
	}
	t.Setenv("GODTA_SERVER", "from-env")
	got = ""
	invoke(t, record, args...)
	if got != "from-env" {
		t.Errorf("GODTA_SERVER set: the profile's server is %q, want the environment's from-env", got)
	}
}

func TestJSONErrorRedacted(t *testing.T) {
	const secret = "S3cr!t pa'ss@word"
	t.Setenv(testPasswordEnv, secret)
	profiles := writeProfiles(t)
	leak := func(_ context.Context, _ sqlq.Profile, pw, _ string) (*sql.DB, error) {
		u := url.URL{Scheme: "sqlserver", User: url.UserPassword("u", pw), Host: "lab01"}
		return nil, fmt.Errorf("login failed for %s (password=%s)", u.String(), pw)
	}
	var buf bytes.Buffer
	d := deps{stdout: &buf, open: leak, resolve: sqlq.NewResolver(os.Getenv, "", func(string) {}).Resolve}
	code := run(context.Background(), append([]string{"collect"}, baseArgs(t, profiles, "ro")...), d)
	if code != exitConnect {
		t.Errorf("code %d, want %d", code, exitConnect)
	}
	text := buf.String()
	for _, form := range []string{secret, url.QueryEscape(secret), strings.TrimPrefix(url.UserPassword("", secret).String(), ":")} {
		if strings.Contains(text, form) {
			t.Errorf("the output holds the password as %q:\n%s", form, text)
		}
	}
	if !strings.Contains(text, "login failed") || !strings.Contains(text, "***") {
		t.Errorf("the output lost the error instead of redacting it:\n%s", text)
	}
}

// TestDryRunWritesPlanOnly: evaluate --dry-run writes plan-evaluate.json and
// sends nothing but reads.
func TestDryRunWritesPlanOnly(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	profiles := writeProfiles(t)
	args := append([]string{"evaluate", "--target", "server", "--dry-run"}, baseArgs(t, profiles, "rw")...)
	project := args[5]
	ws, err := workspace.Open(project)
	if err != nil {
		t.Fatal(err)
	}
	inst := conn.Instance{ServerName: "lab01", MachineName: "LABM", Edition: "Developer Edition (64-bit)",
		EngineEdition: 3, Major: 16, Level: "RTM", Version: "16.0.4250.1"}
	tbl := &model.Table{ObjectID: 1001, Schema: "dbo", Name: "godta_t", Rows: 1000,
		Columns: []model.Column{{ID: 1, Name: "id", Type: "int", AvgWidth: 4}, {ID: 2, Name: "a", Type: "int", AvgWidth: 4}, {ID: 3, Name: "b", Type: "int", AvgWidth: 4}},
		Indexes: []model.Index{{ObjectID: 1001, IndexID: 1, Name: "pk", Kind: model.KindClustered, Keys: []model.KeyCol{{ColumnID: 1}}, Unique: true, PrimaryKey: true}}}
	w := &model.Workload{Server: inst, Database: model.DBInfo{ID: 5, Name: "app"}, Source: model.SourceQueryStore,
		Tables: map[int]*model.Table{1001: tbl}}
	c := model.Candidate{Table: 1001, Keys: []model.KeyCol{{ColumnID: 2}}, Include: []int{3}, Origin: candidate.OriginAlgorithm,
		Queries: []int64{1}, Decision: model.DecisionNew}
	c.ID = candidate.ID(c)
	dir := ws.DBDir("lab01", "app")
	if err := ws.WriteJSON(dir, workspace.CollectFile, w); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(dir, workspace.CandidatesFile, []model.Candidate{c}); err != nil {
		t.Fatal(err)
	}

	fake := &fakeServer{inst: inst, tables: []*model.Table{tbl}}
	code, out := invoke(t, fake.open, args...)
	if code != exitOK || !out.OK {
		t.Fatalf("dry-run: code %d, output %+v", code, out)
	}
	want := filepath.Join(dir, workspace.PlanFile(runplan.PhaseEvaluate))
	if out.Plan != want {
		t.Errorf("plan %q, want %q", out.Plan, want)
	}
	var plan runplan.Plan
	if err := ws.ReadJSON(dir, workspace.PlanFile(runplan.PhaseEvaluate), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Checksum == "" || len(plan.Steps) == 0 || len(plan.Schema) != 1 {
		t.Errorf("plan %+v: want a sealed plan with steps and the fingerprint of the table", plan)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.execs) > 0 {
		t.Errorf("the dry-run executed statements: %q", fake.execs)
	}
	for _, q := range fake.queries {
		if !strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
			t.Errorf("the dry-run sent a statement that is not a read: %q", q)
		}
	}
	if len(fake.queries) == 0 {
		t.Errorf("the dry-run read nothing from the server: the fake was not used")
	}
}

// fakeServer is a database/sql driver that answers the identity and the
// fingerprint queries and records every statement. With lenient, it also
// answers DB_NAME() and DB_ID() with dbName and database_id 5, and any other
// query with no row, which is enough for evaluate to run a plan: the
// compilations return no plan and their measures come out not evaluated.
// A statement sent on a done context fails with the context's error;
// otherwise onExec, when set, sees it, and failExec gives its error (nil
// runs it).
type fakeServer struct {
	inst     conn.Instance
	tables   []*model.Table
	lenient  bool
	dbName   string
	failExec func(q string) error
	onExec   func(q string)
	mu       sync.Mutex
	queries  []string
	execs    []string
}

func (f *fakeServer) open(context.Context, sqlq.Profile, string, string) (*sql.DB, error) {
	return sql.OpenDB(fakeConnector{f}), nil
}

type fakeConnector struct{ f *fakeServer }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{c.f}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return fakeDriver{c.f} }

type fakeDriver struct{ f *fakeServer }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{d.f}, nil }

type fakeConn struct{ f *fakeServer }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no prepare") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("fake: no transaction") }

func (c *fakeConn) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.f.mu.Lock()
	c.f.execs = append(c.f.execs, q)
	c.f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.f.onExec != nil {
		c.f.onExec(q)
	}
	if c.f.failExec != nil {
		if err := c.f.failExec(q); err != nil {
			return nil, err
		}
	}
	return driver.RowsAffected(0), nil
}

func (c *fakeConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.f.mu.Lock()
	c.f.queries = append(c.f.queries, q)
	c.f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i := c.f.inst
	switch {
	case strings.Contains(q, "@@SERVERNAME"):
		return &fakeRows{cols: 10, rows: [][]driver.Value{{i.ServerName, i.MachineName, i.Edition, int64(i.EngineEdition),
			int64(i.Major), i.Level, i.Version, "", int64(4), int64(0)}}}, nil
	case strings.Contains(q, "FROM sys.objects AS o"):
		r := &fakeRows{cols: 4}
		for _, t := range c.f.tables {
			r.rows = append(r.rows, []driver.Value{int64(t.ObjectID), t.Schema, t.Name, time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)})
		}
		return r, nil
	case strings.Contains(q, "FROM sys.indexes i LEFT JOIN sys.data_spaces"):
		// collect's index reader, which the --dry-run uses to compare the
		// live indexes with collect.json: the indexes of tables.
		r := &fakeRows{cols: 14}
		for _, t := range c.f.tables {
			for _, ix := range t.Indexes {
				r.rows = append(r.rows, []driver.Value{int64(ix.ObjectID), int64(ix.IndexID), ix.Name, kindType[ix.Kind],
					ix.Unique, ix.PrimaryKey, ix.UniqueConstraint, ix.Disabled, false, ix.Filter, int64(ix.FillFactor),
					ix.DataSpace, ix.DataPages, ix.Compression})
			}
		}
		return r, nil
	case strings.Contains(q, "FROM sys.index_columns ic"):
		r := &fakeRows{cols: 6}
		for _, t := range c.f.tables {
			for _, ix := range t.Indexes {
				for n, k := range ix.Keys {
					r.rows = append(r.rows, []driver.Value{int64(ix.ObjectID), int64(ix.IndexID), int64(k.ColumnID), int64(n + 1), k.Desc, false})
				}
				for _, id := range ix.Include {
					r.rows = append(r.rows, []driver.Value{int64(ix.ObjectID), int64(ix.IndexID), int64(id), int64(0), false, true})
				}
			}
		}
		return r, nil
	case strings.Contains(q, "FROM sys.indexes AS i"):
		return &fakeRows{cols: 12}, nil
	case !c.f.lenient:
	case strings.Contains(q, "FROM sys.indexes WHERE object_id = OBJECT_ID(@p1)"):
		// cleanup's check that a journaled hypothetical index is there.
		return &fakeRows{cols: 2, rows: [][]driver.Value{{int64(1), int64(1)}}}, nil
	case q == "SELECT DB_NAME()":
		return &fakeRows{cols: 1, rows: [][]driver.Value{{c.f.dbName}}}, nil
	case q == "SELECT DB_ID(), DB_NAME()":
		return &fakeRows{cols: 2, rows: [][]driver.Value{{int64(5), c.f.dbName}}}, nil
	default:
		return &fakeRows{cols: 1}, nil
	}
	return nil, fmt.Errorf("fake: unexpected query %q", q)
}

// kindType is the sys.indexes type of an index kind.
var kindType = map[string]int64{model.KindHeap: 0, model.KindClustered: 1, model.KindNonclustered: 2,
	model.KindClusteredColumnstore: 5, model.KindNonclusteredColumnstore: 6}

type fakeRows struct {
	cols int
	rows [][]driver.Value
	n    int
}

func (r *fakeRows) Columns() []string {
	names := make([]string, r.cols)
	for i := range names {
		names[i] = fmt.Sprintf("c%d", i)
	}
	return names
}
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.n >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.n])
	r.n++
	return nil
}

// TestLabRefusedBeforeConnection: evaluate --target lab with a lab profile
// that is not a lab (readonly, no environment), or without --allow-write,
// is refused with code 3 before any connection.
func TestLabRefusedBeforeConnection(t *testing.T) {
	t.Setenv(testPasswordEnv, "pw")
	profiles := writeProfiles(t)
	lab := []string{"evaluate", "--target", "lab", "--host-dir", t.TempDir(), "--container-dir", "/var/opt/mssql/godta"}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"not a lab", []string{"--lab-profile", "ro", "--allow-write"}, "environment"},
		{"no --allow-write", []string{"--lab-profile", "rw"}, "--allow-write"},
	} {
		args := append(append(append([]string{}, lab...), baseArgs(t, profiles, "rw")...), c.args...)
		code, out := invoke(t, noOpen(t), args...)
		if code != exitRefused || out.OK || out.Error == nil || !strings.Contains(out.Error.Message, c.want) {
			t.Errorf("%s: code %d, output %+v; want code 3, refused on %q", c.name, code, out, c.want)
		}
	}
}

// TestCloneSizeEstimate: the side effect of the clone plan that gives the
// size from msdb.dbo.backupset says it is an estimate, with the sizes and
// the date of the backup, and says when there is no estimate at all.
func TestCloneSizeEstimate(t *testing.T) {
	at := time.Date(2026, 11, 8, 10, 30, 0, 0, time.UTC)
	found := backupEstimate{Found: true, Finished: at, Bytes: 3039232, CompressedBytes: 1024000}.text("Sales", 2<<30)
	for _, want := range []string{"estimate only", "[Sales]", "3039232 bytes", "1024000 compressed", "2026-11-08T10:30:00Z", "max_bytes (2147483648)"} {
		if !strings.Contains(found, want) {
			t.Errorf("estimate %q does not contain %q", found, want)
		}
	}
	none := backupEstimate{}.text("Sales", 1000)
	if !strings.Contains(none, "no full backup of [Sales]") || !strings.Contains(none, "no estimate") {
		t.Errorf("no backup: %q", none)
	}
	for _, c := range []struct {
		in   string
		want int64
	}{{"2GiB", 2 << 30}, {"256MiB", 256 << 20}, {"64KiB", 64 << 10}, {"1000", 1000}, {"10B", 10}} {
		if got, err := parseBytes(c.in); err != nil || got != c.want {
			t.Errorf("parseBytes(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "0", "-1MiB", "2GB", "x"} {
		if _, err := parseBytes(bad); err == nil {
			t.Errorf("parseBytes(%q) accepted", bad)
		}
	}
}
