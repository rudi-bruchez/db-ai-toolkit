package main

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

func tsqlEntry(t *testing.T) sqlq.Entry {
	t.Helper()
	src := "-- Sessions\n-- sqlq: name=sessions params=hostname,top_n\nDECLARE @hostname sysname = N'%';\nDECLARE @top_n int = 20;\nSELECT TOP (@top_n) host_name FROM sys.dm_exec_sessions WHERE host_name LIKE @hostname;\n"
	ps, err := sqlq.AnalyseOverrides(src, []string{"hostname", "top_n"})
	if err != nil {
		t.Fatal(err)
	}
	return sqlq.Entry{Name: "sessions", Source: sqlq.SourceTsqlScripts, Path: "d/s.sql", SQL: src, Overrides: ps}
}

// captureRun redirects os.Stdout to a pipe for the duration of run(o). The
// tests of cmd/sqlq run in sequence (no t.Parallel()), which makes this safe.
func captureRun(t *testing.T, o options) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := run(o)
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), code
}

func TestPrepareSavedRewritesAndBinds(t *testing.T) {
	text, args, run, err := prepareSaved(tsqlEntry(t), paramList{"HostName=SRV-APP01"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "DECLARE @hostname sysname = @sqlq_hostname;") || !strings.Contains(text, "DECLARE @top_n int = 20;") {
		t.Errorf("text = %s", text)
	}
	if len(args) != 1 || args[0] != sql.Named("sqlq_hostname", "SRV-APP01") {
		t.Errorf("args = %#v", args)
	}
	if run.Params["hostname"] != "SRV-APP01" || strings.Join(run.Defaults, ",") != "top_n" {
		t.Errorf("run = %+v", run)
	}
}

func TestUnknownParamRefusedBeforeConnecting(t *testing.T) {
	_, _, _, err := prepareSaved(tsqlEntry(t), paramList{"hostnme=SRV"})
	if err == nil || !strings.Contains(err.Error(), "hostnme") {
		t.Errorf("err = %v", err)
	}
}

func TestDuplicateParamIsRefused(t *testing.T) {
	if _, _, _, err := prepareSaved(tsqlEntry(t), paramList{"hostname=a", "HOSTNAME=b"}); err == nil {
		t.Error("a parameter passed twice must be refused")
	}
}

func TestInvalidValueRefusedBeforeConnecting(t *testing.T) {
	if _, _, _, err := prepareSaved(tsqlEntry(t), paramList{"top_n=many"}); err == nil {
		t.Error("top_n=many accepted for an int")
	}
}

func TestMissingParameterIsRefusedBeforeConnecting(t *testing.T) {
	e := sqlq.Entry{Name: "refs", Source: sqlq.SourceBundled, Path: "refs.sql",
		SQL: "SELECT name FROM sys.objects WHERE name = @name;", QueryParams: []string{"name"}}
	if _, _, _, err := prepareSaved(e, nil); err == nil || !strings.Contains(err.Error(), `"name" required`) {
		t.Errorf("err = %v", err)
	}
	if _, _, _, err := prepareSaved(e, paramList{"name=x", "extra=y"}); err == nil {
		t.Error("an unknown -param on a bundled query must be refused")
	}
	_, args, _, err := prepareSaved(e, paramList{"name=dbo.T"})
	if err != nil || len(args) != 1 || args[0] != sql.Named("name", "dbo.T") {
		t.Errorf("args = %#v, err = %v", args, err)
	}
}

func TestListQueriesWorksWithoutProfilesFile(t *testing.T) {
	t.Setenv("DB_AI_TOOLKIT_QUERIES", t.TempDir())
	t.Setenv("DB_AI_TOOLKIT_TSQL_SCRIPTS", "")
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", filepath.Join(t.TempDir(), "v.json"))
	o := options{listQueries: true, profilesPath: "/nonexistent/profiles.json", queriesDir: "../../internal/sqlq/testdata/catalog/bundled"}
	out, code := captureRun(t, o)
	if code != exitOK || !strings.Contains(out, `"name":"tables-largest"`) {
		t.Errorf("code %d, out %s", code, out)
	}
}

func TestReadQueryNeedsExactlyOneSource(t *testing.T) {
	if _, err := readQuery("SELECT 1", "", "tables-largest"); err == nil {
		t.Error("-query with -saved accepted")
	}
	if _, err := readQuery("", "", ""); err == nil {
		t.Error("no source accepted")
	}
	if text, err := readQuery("", "", "x"); err != nil || text != "" {
		t.Errorf("-saved alone: %q, %v", text, err)
	}
}

func TestBoundQueryRefusedOnAnotherProfile(t *testing.T) {
	q := t.TempDir()
	os.MkdirAll(filepath.Join(q, "profiles", "prd"), 0o700)
	os.WriteFile(filepath.Join(q, "profiles", "prd", "orders-late.sql"), []byte("/* Late.\n*/\nSELECT 1;\n"), 0o600)
	cfg := sqlq.CatalogConfig{PersonalDir: q, Profile: "dev", ProfileNames: []string{"dev", "prd"}}
	cat := sqlq.LoadCatalog(cfg)
	if _, ok := cat.Find("orders-late"); ok {
		t.Fatal("a query of profile prd is visible from dev")
	}
	err := missingQueryError(cat, cfg, "orders-late")
	if err == nil || !strings.Contains(err.Error(), `bound to profile directory "prd"`) {
		t.Errorf("err = %v", err)
	}
}

func saveEnv(t *testing.T) string {
	t.Helper()
	q := t.TempDir()
	t.Setenv("DB_AI_TOOLKIT_QUERIES", q)
	t.Setenv("DB_AI_TOOLKIT_TSQL_SCRIPTS", "")
	t.Setenv("DB_AI_TOOLKIT_REGISTRY", filepath.Join(t.TempDir(), "v.json"))
	return q
}

func TestQueryNameRejectsTraversalAndCase(t *testing.T) {
	saveEnv(t)
	p := sqlq.Profile{Name: "dev"}
	for _, name := range []string{"../../x", "Orders", "a", "x/y", "orders_late"} {
		o := options{saveQuery: name, summary: "S.", queriesDir: t.TempDir()}
		if _, _, err := checkSave(o, p, []string{"dev"}, "SELECT 1;"); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestSaveRefusesAWritingQuery(t *testing.T) {
	saveEnv(t)
	o := options{saveQuery: "purge", summary: "S.", queriesDir: t.TempDir(), allowWrite: true}
	if _, _, err := checkSave(o, sqlq.Profile{Name: "dev", Mode: sqlq.ModeReadWrite}, []string{"dev"}, "DELETE FROM dbo.T;"); err == nil {
		t.Error("a writing query was accepted for saving")
	}
}

func TestSaveRefusesAVisibleName(t *testing.T) {
	saveEnv(t)
	o := options{saveQuery: "tables-largest", summary: "S.", queriesDir: "../../internal/sqlq/testdata/catalog/bundled"}
	if _, _, err := checkSave(o, sqlq.Profile{Name: "dev"}, []string{"dev"}, "SELECT 1;"); err == nil {
		t.Error("saving over a bundled name was accepted")
	}
}

func TestSavedFileThatDoesNotReparseIsRemoved(t *testing.T) {
	q := saveEnv(t)
	o := options{saveQuery: "broken", queriesDir: t.TempDir()}
	path := filepath.Join(q, "profiles", "dev", "broken.sql")
	// Content without a header cannot read back as a valid entry, and neither
	// can a valid header over a body the loader refuses: SavedFileContent
	// accepts a lone CR, a NUL or an oversized body, the catalogue does not.
	for _, body := range []string{
		"SELECT 1;\n",
		"/*  S.\n\n    Parameters: none.\n*/\nSELECT 1;\rSELECT 2;\n",
		"/*  S.\n\n    Parameters: none.\n*/\nSELECT 'a\x00b';\n",
		"/*  S.\n\n    Parameters: none.\n*/\nSELECT 1;\n" + strings.Repeat("-- pad\n", 1<<18),
	} {
		if err := writeSavedQuery(o, sqlq.Profile{Name: "dev"}, []string{"dev"}, path, body); err == nil {
			t.Errorf("an unparseable saved file was accepted: %.60q", body)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("the rejected file was left behind (%.60q): %v", body, err)
			os.Remove(path)
		}
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("kept"), 0o600)
	if err := writeSavedQuery(o, sqlq.Profile{Name: "dev"}, []string{"dev"}, path, "/* S.\n*/\nSELECT 1;\n"); err == nil {
		t.Error("an existing file was overwritten")
	}
	if b, _ := os.ReadFile(path); string(b) != "kept" {
		t.Errorf("existing file changed to %q", b)
	}
}

func TestSaveRequiresSummary(t *testing.T) {
	saveEnv(t)
	o := options{saveQuery: "orders-late", queriesDir: t.TempDir()}
	if _, _, err := checkSave(o, sqlq.Profile{Name: "dev"}, []string{"dev"}, "SELECT 1;"); err == nil || !strings.Contains(err.Error(), "-summary") {
		t.Errorf("err = %v", err)
	}
}
