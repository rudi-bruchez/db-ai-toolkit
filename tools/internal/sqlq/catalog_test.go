package sqlq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCatalogConfig(profile string) CatalogConfig {
	root := filepath.Join("testdata", "catalog")
	return CatalogConfig{
		BundledDir:     filepath.Join(root, "bundled"),
		PersonalDir:    filepath.Join(root, "personal"),
		TsqlScriptsDir: filepath.Join(root, "tsql"),
		Profile:        profile,
		ProfileNames:   []string{"AU-PRD/node1", "other"},
	}
}

func entryByPath(c Catalog, source Source, path string) (Entry, bool) {
	for _, e := range c.Entries {
		if e.Source == source && e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

func TestCatalogListsExactlyTheFilesPresent(t *testing.T) {
	c := LoadCatalog(testCatalogConfig(""))
	var got []string
	for _, e := range c.Entries {
		got = append(got, string(e.Source)+":"+e.Path)
	}
	want := []string{
		"bundled:Bad_Name.sql", "bundled:lying-header.sql", "bundled:object-refs.sql", "bundled:tables-largest.sql",
		"personal:_generic/my-generic.sql", "personal:_generic/tables-largest.sql",
		"tsql-scripts:a/dup.sql", "tsql-scripts:b/dup.sql",
		"tsql-scripts:diagnostics/sessions-from-host.sql", "tsql-scripts:diagnostics/waits.sql",
		"tsql-scripts:x/takes-bundled.sql",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("entries:\n got %v\nwant %v", got, want)
	}
}

func TestMarkedScriptRefusedByGuardIsListedAsRejected(t *testing.T) {
	e, _ := entryByPath(LoadCatalog(testCatalogConfig("")), SourceTsqlScripts, "diagnostics/waits.sql")
	if e.Rejected != "batch separator GO at line 4" {
		t.Errorf("rejected = %q", e.Rejected)
	}
}

func TestParametersLineIsOptionalButChecked(t *testing.T) {
	c := LoadCatalog(testCatalogConfig(""))
	lying, _ := entryByPath(c, SourceBundled, "lying-header.sql")
	if !strings.Contains(lying.Rejected, "Parameters line") {
		t.Errorf("a header that lies about its parameters: rejected = %q", lying.Rejected)
	}
	if e, _ := entryByPath(c, SourceBundled, "object-refs.sql"); e.Rejected != "" {
		t.Errorf("a truthful Parameter line was rejected: %q", e.Rejected)
	}
}

func TestInvalidFileNameIsRejected(t *testing.T) {
	e, _ := entryByPath(LoadCatalog(testCatalogConfig("")), SourceBundled, "Bad_Name.sql")
	if !strings.Contains(e.Rejected, "invalid name") {
		t.Errorf("rejected = %q", e.Rejected)
	}
}

func TestBundledWinsCollisionOthersRejected(t *testing.T) {
	c := LoadCatalog(testCatalogConfig(""))
	b, _ := entryByPath(c, SourceBundled, "tables-largest.sql")
	p, _ := entryByPath(c, SourcePersonal, "_generic/tables-largest.sql")
	x, _ := entryByPath(c, SourceTsqlScripts, "x/takes-bundled.sql")
	if b.Rejected != "" || !strings.Contains(p.Rejected, "taken by bundled") || !strings.Contains(x.Rejected, "taken by bundled") {
		t.Errorf("bundled %q, personal %q, tsql %q", b.Rejected, p.Rejected, x.Rejected)
	}
	if e, ok := c.Find("tables-largest"); !ok || e.Source != SourceBundled {
		t.Errorf("Find must return the bundled entry: %+v", e)
	}
}

func TestCollisionBetweenNonBundledRejectsAll(t *testing.T) {
	c := LoadCatalog(testCatalogConfig(""))
	a, _ := entryByPath(c, SourceTsqlScripts, "a/dup.sql")
	b, _ := entryByPath(c, SourceTsqlScripts, "b/dup.sql")
	if a.Rejected == "" || b.Rejected == "" {
		t.Errorf("both dup entries must be rejected: %q / %q", a.Rejected, b.Rejected)
	}
	if _, ok := c.Find("dup"); ok {
		t.Errorf("Find must not return a rejected entry")
	}
}

func TestProfileViewListsOnlyThatProfile(t *testing.T) {
	c := LoadCatalog(testCatalogConfig("AU-PRD/node1"))
	e, ok := entryByPath(c, SourcePersonal, "profiles/au-prd/node1/orders-late.sql")
	if !ok || e.Scope != "AU-PRD/node1" || e.Rejected != "" {
		t.Errorf("own profile entry: %+v ok=%v", e, ok)
	}
	if _, ok := entryByPath(c, SourcePersonal, "profiles/other/orders-late.sql"); ok {
		t.Errorf("another profile's directory must not be in the view")
	}
}

func TestProfileDirRejectsDotDotAndCaseTwins(t *testing.T) {
	if _, err := ProfileDir("/q", "../etc", nil); err == nil {
		t.Error("'..' accepted")
	}
	if _, err := ProfileDir("/q", "a/./b", nil); err == nil {
		t.Error("'.' accepted")
	}
	if _, err := ProfileDir("/q", "Prod-ERP", []string{"Prod-ERP", "prod-erp"}); err == nil {
		t.Error("case twins accepted")
	}
	got, err := ProfileDir("/q", "AU-PRD/dbcsqlaueprd01-nbr", []string{"AU-PRD/dbcsqlaueprd01-nbr"})
	if err != nil || got != filepath.Join("/q", "profiles", "au-prd", "dbcsqlaueprd01-nbr") {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestTsqlEntryCarriesTypedParamsAndDirtyReads(t *testing.T) {
	e, _ := entryByPath(LoadCatalog(testCatalogConfig("")), SourceTsqlScripts, "diagnostics/sessions-from-host.sql")
	if e.Rejected != "" || !e.DirtyReads || e.Name != "sessions-from-host" ||
		len(e.Params) != 1 || e.Params[0] != (CatalogParam{Name: "hostname", Type: "sysname"}) ||
		e.Overrides[0].Default != "N'%'" {
		t.Errorf("entry = %+v", e)
	}
}

func TestVerifiedSurvivesRenameAndMarkerEdit(t *testing.T) {
	dir := t.TempDir()
	body := "SET NOCOUNT ON;\nSELECT 1;\n"
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("-- One\n-- sqlq: name=one\n"+body), 0o600)
	cfg := CatalogConfig{TsqlScriptsDir: dir}
	e, _ := LoadCatalog(cfg).Find("one")
	reg := filepath.Join(t.TempDir(), "verified.json")
	RecordVerified(reg, e.Hash, Verified{Date: "2026-10-04", Profile: "p"})
	os.Remove(filepath.Join(dir, "a.sql"))
	os.WriteFile(filepath.Join(dir, "b.sql"), []byte("-- One\n-- sqlq: name=uno heavy\n"+body), 0o600)
	cfg.Registry, _ = LoadRegistry(reg)
	if e, _ := LoadCatalog(cfg).Find("uno"); e.Verified == nil {
		t.Errorf("verification lost by a rename and a marker edit")
	}
}

func TestVerifiedDropsWhenSQLChanges(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("-- One\n-- sqlq: name=one\nSELECT 1;\n"), 0o600)
	cfg := CatalogConfig{TsqlScriptsDir: dir}
	e, _ := LoadCatalog(cfg).Find("one")
	reg := filepath.Join(t.TempDir(), "verified.json")
	RecordVerified(reg, e.Hash, Verified{Date: "d", Profile: "p"})
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("-- One\n-- sqlq: name=one\nSELECT 2;\n"), 0o600)
	cfg.Registry, _ = LoadRegistry(reg)
	if e, _ := LoadCatalog(cfg).Find("one"); e.Verified != nil {
		t.Errorf("verification survived a change of SQL")
	}
}

func TestDotGitIsSkipped(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o700)
	os.WriteFile(filepath.Join(dir, ".git", "ignored.sql"), []byte("-- Ignored\n-- sqlq: name=ignored\nSELECT 1;\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "kept.sql"), []byte("-- Kept\n-- sqlq: name=kept\nSELECT 1;\n"), 0o600)
	// The bundled canon is loaded too: a marker that takes one of its names is
	// rejected by the real catalogue and must be rejected here.
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: dir, BundledDir: bundledQueriesDir})
	var tsql []string
	for _, e := range c.Entries {
		if e.Source == SourceTsqlScripts {
			tsql = append(tsql, e.Name+":"+e.Path+":"+e.Rejected)
		}
	}
	if strings.Join(tsql, ",") != "kept:kept.sql:" {
		t.Errorf("tsql-scripts entries = %v", tsql)
	}
}

// The registry key is the hash of the bytes -saved will execute, captured once
// at load: there is no second read of the file between hashing and running.
func TestVerifiedHashesBytesThatRan(t *testing.T) {
	for _, e := range LoadCatalog(testCatalogConfig("AU-PRD/node1")).Entries {
		want := ContentHash([]byte(e.SQL))
		if e.Source == SourceTsqlScripts {
			h, _, _ := ParseMarkedHeader(e.SQL)
			want = ContentHash([]byte(withoutLine(e.SQL, h.Marker.Line)))
		}
		if e.SQL == "" || e.Hash != want {
			t.Errorf("%s:%s: hash does not cover the SQL held for execution", e.Source, e.Path)
		}
	}
}

func TestMissingTsqlScriptsSourceIsAMessageNotAnError(t *testing.T) {
	c := LoadCatalog(CatalogConfig{BundledDir: filepath.Join("testdata", "catalog", "bundled")})
	if len(c.Messages) == 0 || !strings.Contains(c.Messages[0], "not configured") {
		t.Errorf("messages = %v", c.Messages)
	}
	c = LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(t.TempDir(), "absent")})
	if len(c.Messages) == 0 || !strings.Contains(c.Messages[0], "not found") {
		t.Errorf("messages = %v", c.Messages)
	}
}

func TestListQueriesPublishesNoPathServerOrSQL(t *testing.T) {
	cfg := testCatalogConfig("AU-PRD/node1")
	abs, _ := filepath.Abs(cfg.TsqlScriptsDir)
	cfg.TsqlScriptsDir = abs
	b, _ := json.Marshal(LoadCatalog(cfg))
	s := string(b)
	for _, forbidden := range []string{abs, "SELECT", "\"sql\"", "\"hash\"", "/home/", "\\\\Users"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("catalogue JSON contains %q", forbidden)
		}
	}
	if !strings.Contains(s, `"verified":null`) {
		t.Errorf("valid entries must carry verified:null")
	}
}
