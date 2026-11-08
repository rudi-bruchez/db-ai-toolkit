package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
)

// runMainEnv makes the test binary run main instead of the tests: a test
// that must kill godta with SIGKILL runs it as a child process this way.
const runMainEnv = "GODTA_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
	}
	os.Exit(m.Run())
}

// srvRun is a throwaway database loaded with the data set and the workload,
// collected, with its candidates and its evaluate plan, through the
// binary's run.
type srvRun struct {
	t        *testing.T
	ctx      context.Context
	db       *sql.DB
	name     string
	project  string
	common   []string
	planFile string
	plan     runplan.Plan
}

func newSrvRun(t *testing.T) *srvRun {
	t.Helper()
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	s := &srvRun{t: t}
	s.name = ittest.NewDatabase(t, server, "")
	s.db = ittest.OpenDB(t, p, r, s.name)
	ittest.LoadDataset(t, s.db)
	// As in TestEndToEndServer: the load of the data set is not the
	// workload under test.
	if _, err := server.Exec("ALTER DATABASE [" + s.name + "] SET QUERY_STORE CLEAR;"); err != nil {
		t.Fatal(err)
	}
	ittest.RunWorkload(t, s.db, 3)
	s.project = filepath.Join(t.TempDir(), "project")
	s.common = []string{"--project", s.project, "--profiles", os.Getenv("GODTA_TEST_PROFILES"),
		"--profile", os.Getenv("GODTA_TEST_PROFILE"), "--env", filepath.Join(t.TempDir(), "absent.env"),
		"--include", s.name}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	s.ctx = ctx
	for _, args := range [][]string{{"collect"}, {"candidates"}} {
		if code, out := s.step(args...); code != exitOK {
			t.Fatalf("%s: code %d, %+v", args[0], code, out)
		}
	}
	code, out := s.step("evaluate", "--target", "server", "--dry-run")
	if code != exitOK || out.Plan == "" {
		t.Fatalf("evaluate --dry-run: code %d, %+v", code, out)
	}
	s.planFile = out.Plan
	readJSONFile(t, out.Plan, &s.plan)
	return s
}

func (s *srvRun) step(args ...string) (int, output) {
	s.t.Helper()
	var buf bytes.Buffer
	code := run(s.ctx, append(args, s.common...), realDepsTo(&buf))
	var out output
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		s.t.Fatalf("%s: stdout is not JSON: %v\n%s", args[0], err, buf.String())
	}
	return code, out
}

// lockH opens a transaction that holds a shared table lock on godta_h: the
// S of a hypothetical index creation is compatible with it, the Sch-M of
// its drop is not. The lock is released when the test ends.
func (s *srvRun) lockH() {
	s.t.Helper()
	c, err := s.db.Conn(s.ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := c.ExecContext(s.ctx, "BEGIN TRAN; SELECT TOP (1) 1 FROM dbo.godta_h WITH (TABLOCK, HOLDLOCK);"); err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() {
		c.ExecContext(context.Background(), "IF @@TRANCOUNT > 0 ROLLBACK")
		c.Close()
	})
}

// hypotheticals lists the hypothetical indexes of the run on godta_h.
func (s *srvRun) hypotheticals() []string {
	s.t.Helper()
	rows, err := s.db.QueryContext(s.ctx, "SELECT name FROM sys.indexes WHERE is_hypothetical = 1 AND object_id = OBJECT_ID('dbo.godta_h') AND name LIKE 'godta[_]"+s.plan.RunID+"[_]%' ORDER BY name")
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// TestDropLockTimeoutFailsRun: another session holds a shared lock on
// godta_h during the whole evaluation, so every drop of a hypothetical index
// of godta_h fails three times on its lock timeout. The run exits 2 with ok
// false; the status of the database says objects are left, and a warning
// names each index left and says to run cleanup.
func TestDropLockTimeoutFailsRun(t *testing.T) {
	s := newSrvRun(t)
	s.lockH()
	code, out := s.step("evaluate", "--target", "server", "--allow-write", "--plan", s.planFile)
	left := s.hypotheticals()
	t.Logf("code %d, left on godta_h %q, databases %+v", code, left, out.Databases)
	if len(left) == 0 {
		t.Fatalf("no hypothetical index left on godta_h: the lock did not block the drops")
	}
	if code != exitFailed || out.OK || len(out.Databases) != 1 {
		t.Fatalf("code %d, ok %v, databases %+v; want code 2, ok false, one database", code, out.OK, out.Databases)
	}
	r := out.Databases[0]
	if !strings.Contains(r.Status, "left for cleanup") {
		t.Errorf("status %q, want it to say objects are left for cleanup", r.Status)
	}
	for _, n := range left {
		if !hasWarning(r, n, "godta cleanup") {
			t.Errorf("warnings %q: none names %s and says to run godta cleanup", r.Warnings, n)
		}
	}
}
