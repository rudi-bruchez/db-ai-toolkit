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
// The expected hash is computed from the literal written, not from e.SQL, and
// rewriting the file after the load must not move the entry's hash.
func TestVerifiedHashesBytesThatRan(t *testing.T) {
	dir := t.TempDir()
	body := "SET NOCOUNT ON;\r\nSELECT 1;\r\n"
	writeTestFile(t, filepath.Join(dir, "t", "a.sql"), "\xEF\xBB\xBF-- One\r\n-- sqlq: name=one\r\n"+body)
	writeTestFile(t, filepath.Join(dir, "b", "blk.sql"), "\xEF\xBB\xBF"+validBlockSQL)
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(dir, "t"), BundledDir: filepath.Join(dir, "b")})
	one, _ := c.Find("one")
	blk, _ := c.Find("blk")
	if one.SQL != "-- One\r\n-- sqlq: name=one\r\n"+body || one.Hash != ContentHash([]byte("-- One\r\n"+body)) {
		t.Errorf("tsql entry: hash does not cover the SQL held for execution: %q", one.SQL)
	}
	if blk.SQL != validBlockSQL || blk.Hash != ContentHash([]byte(validBlockSQL)) {
		t.Errorf("bundled entry: hash does not cover the SQL held for execution: %q", blk.SQL)
	}
	writeTestFile(t, filepath.Join(dir, "t", "a.sql"), "-- One\n-- sqlq: name=one\nSELECT 2;\n")
	if one.Hash != ContentHash([]byte("-- One\r\n"+body)) {
		t.Errorf("the entry's hash moved with the file on disk")
	}
	if again, _ := LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(dir, "t")}).Find("one"); again.Hash == one.Hash {
		t.Errorf("a changed file kept its hash")
	}
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

// Tests from the code review of task 7: no file meant for the catalogue
// disappears in silence, and no rejection quotes what the author wrote.

const validBlockSQL = "/* A valid query.\n\n   Parameters: none.\n*/\nSELECT 1;\n"

func validMarkedSQL(name string) string {
	return "-- A valid script\n-- sqlq: name=" + name + "\nSELECT 1;\n"
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lockForTest makes path unreadable and gives it back before t.TempDir's
// own cleanup, which could not remove it otherwise.
func lockForTest(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 file: this test cannot run as root")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, st.Mode().Perm()) })
}

func catalogJSON(t *testing.T, c Catalog) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBlockDirWithBracketInPathIsLoaded(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "q[1]")
	personal := filepath.Join(root, "p[x]")
	writeTestFile(t, filepath.Join(bundled, "canon-one.sql"), validBlockSQL)
	writeTestFile(t, filepath.Join(personal, "_generic", "mine-one.sql"), validBlockSQL)
	c := LoadCatalog(CatalogConfig{BundledDir: bundled, PersonalDir: personal})
	if _, ok := c.Find("canon-one"); !ok {
		t.Errorf("bundled entry lost under a path holding '[': %+v", c)
	}
	if _, ok := c.Find("mine-one"); !ok {
		t.Errorf("personal entry lost under a path holding '[': %+v", c)
	}
}

func TestBlockDirReadErrorIsAMessage(t *testing.T) {
	bundled := filepath.Join(t.TempDir(), "queries")
	writeTestFile(t, filepath.Join(bundled, "canon-one.sql"), validBlockSQL)
	lockForTest(t, bundled)
	c := LoadCatalog(CatalogConfig{BundledDir: bundled})
	s := catalogJSON(t, c)
	if !strings.Contains(s, "bundled queries directory unreadable") {
		t.Errorf("an unreadable bundled directory must say so: %s", s)
	}
	if strings.Contains(s, bundled) || strings.Contains(s, "permission denied") {
		t.Errorf("message carries a path or an OS error: %s", s)
	}
}

func TestUpperCaseSQLExtensionIsListed(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "b", "canon-one.SQL"), validBlockSQL)
	writeTestFile(t, filepath.Join(root, "p", "_generic", "mine-one.Sql"), validBlockSQL)
	c := LoadCatalog(CatalogConfig{BundledDir: filepath.Join(root, "b"), PersonalDir: filepath.Join(root, "p")})
	b, okB := c.Find("canon-one")
	p, okP := c.Find("mine-one")
	if !okB || !okP || b.Path != "canon-one.SQL" || p.Path != "_generic/mine-one.Sql" {
		t.Errorf("upper-case .sql files: %+v", c.Entries)
	}
}

func TestMarkerAttemptsReachTheCatalogAsRejected(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "no-colon.sql"), "-- S\n-- sqlq name=no-colon\nSELECT 1;\n")
	writeTestFile(t, filepath.Join(dir, "nbsp.sql"), "-- S\n--\u00a0sqlq: name=nbsp\nSELECT 1;\n")
	writeTestFile(t, filepath.Join(dir, "plain.sql"), "-- S\nSELECT 'TSQLQuery';\n")
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: dir})
	for _, p := range []string{"no-colon.sql", "nbsp.sql"} {
		if e, ok := entryByPath(c, SourceTsqlScripts, p); !ok || e.Rejected == "" {
			t.Errorf("%s: a marker attempt vanished or passed: %+v ok=%v", p, e, ok)
		}
	}
	if _, ok := entryByPath(c, SourceTsqlScripts, "plain.sql"); ok {
		t.Errorf("an unmarked file must stay out of the catalogue")
	}
}

func TestUTF16FileIsRejected(t *testing.T) {
	dir := t.TempDir()
	var le []byte
	for _, r := range validMarkedSQL("wide") {
		le = append(le, byte(r), 0)
	}
	writeTestFile(t, filepath.Join(dir, "tsql", "wide.sql"), "\xFF\xFE"+string(le))
	writeTestFile(t, filepath.Join(dir, "b", "wide-canon.sql"), "\xFE\xFF\x00/\x00*")
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(dir, "tsql"), BundledDir: filepath.Join(dir, "b")})
	e, ok := entryByPath(c, SourceTsqlScripts, "wide.sql")
	if !ok || e.Rejected != "not UTF-8 text" || e.Name != "wide" {
		t.Errorf("tsql UTF-16: %+v ok=%v", e, ok)
	}
	if e, _ := entryByPath(c, SourceBundled, "wide-canon.sql"); e.Rejected != "not UTF-8 text" {
		t.Errorf("bundled UTF-16: %+v", e)
	}
}

func TestLoneCRFileIsListedAsRejected(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "mac.sql"), "-- Old Mac file\r-- sqlq: name=mac\r-- x\rDELETE FROM t;\r")
	e, ok := entryByPath(LoadCatalog(CatalogConfig{TsqlScriptsDir: dir}), SourceTsqlScripts, "mac.sql")
	if !ok || e.Name != "mac" || !strings.Contains(e.Rejected, "lone CR") {
		t.Errorf("lone CR: %+v ok=%v", e, ok)
	}
	if e.Hash != ContentHash([]byte("-- Old Mac file\r-- x\rDELETE FROM t;\r")) {
		t.Errorf("the hash must cover the bytes read, marker line excluded")
	}
}

func TestUnreadableFileIsRejected(t *testing.T) {
	root := t.TempDir()
	tsqlFile := filepath.Join(root, "tsql", "sub", "locked-script.sql")
	blockFile := filepath.Join(root, "b", "locked-canon.sql")
	writeTestFile(t, tsqlFile, validMarkedSQL("locked-script"))
	writeTestFile(t, blockFile, validBlockSQL)
	lockForTest(t, tsqlFile)
	lockForTest(t, blockFile)
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(root, "tsql"), BundledDir: filepath.Join(root, "b")})
	e, ok := entryByPath(c, SourceTsqlScripts, "sub/locked-script.sql")
	if !ok || e.Rejected != "unreadable" || e.Name != "locked-script" {
		t.Errorf("tsql: %+v ok=%v", e, ok)
	}
	if e, ok := entryByPath(c, SourceBundled, "locked-canon.sql"); !ok || e.Rejected != "unreadable" {
		t.Errorf("bundled: %+v ok=%v", e, ok)
	}
	if s := catalogJSON(t, c); strings.Contains(s, root) || strings.Contains(s, "permission denied") {
		t.Errorf("catalogue carries a path or an OS error: %s", s)
	}
}

func TestUnreadableSubdirectoryIsAMessage(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "private")
	writeTestFile(t, filepath.Join(sub, "inside.sql"), validMarkedSQL("inside"))
	lockForTest(t, sub)
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: root})
	s := catalogJSON(t, c)
	if !strings.Contains(s, `tsql-scripts directory unreadable: private`) {
		t.Errorf("an unreadable directory must be a message: %s", s)
	}
	if strings.Contains(s, root) || strings.Contains(s, "permission denied") {
		t.Errorf("message carries a path or an OS error: %s", s)
	}
}

func TestHeavyTypoRejectsBlockEntry(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "typo.sql"), "/* S.\n\n   Heavy: yes.\n*/\nSELECT 1;\n")
	if e, _ := entryByPath(LoadCatalog(CatalogConfig{BundledDir: dir}), SourceBundled, "typo.sql"); e.Rejected == "" {
		t.Errorf("Heavy: yes. accepted: %+v", e)
	}
}

func TestRejectedEntriesDoNotPublishAuthorText(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "bk.sql"), `-- S`+"\n"+`-- sqlq: name=bk C:\Users\rudi\acme\secret.xel`+"\nSELECT 1;\n")
	writeTestFile(t, filepath.Join(dir, "path-name.sql"), "-- S\n-- sqlq: name=/home/rudi/clients/acme-prod\nSELECT 1;\n")
	writeTestFile(t, filepath.Join(dir, "Bad_Stem.sql"), "-- S\n-- sqlq: name=Acme_Prod\nSELECT 1;\n")
	writeTestFile(t, filepath.Join(dir, "params.sql"), "-- S\n-- sqlq: name=p-q params=SRV-ACME-PROD01\nSELECT 1;\n")
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: dir})
	s := catalogJSON(t, c)
	for _, forbidden := range []string{"acme", "Acme", "ACME", `C:\\Users`, "/home/"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("catalogue publishes %q: %s", forbidden, s)
		}
	}
	if e, _ := entryByPath(c, SourceTsqlScripts, "path-name.sql"); e.Name != "path-name" || e.Rejected != "invalid name" {
		t.Errorf("an invalid name is published as the file stem: %+v", e)
	}
	if e, _ := entryByPath(c, SourceTsqlScripts, "Bad_Stem.sql"); e.Name != "" || e.Rejected == "" {
		t.Errorf("an invalid stem is not published: %+v", e)
	}
}

// A name taken from the file stem is not one the author claimed: it must not
// reject the entry that does claim it.
func TestFileStemNameTakesPartInNoCollision(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a", "shared.sql"), "-- S\n-- sqlq: name=/tmp/x\nSELECT 1;\n")
	writeTestFile(t, filepath.Join(dir, "b", "other.sql"), validMarkedSQL("shared"))
	if _, ok := LoadCatalog(CatalogConfig{TsqlScriptsDir: dir}).Find("shared"); !ok {
		t.Errorf("a stem-named rejected entry took the name of a valid one")
	}
}

func TestSymlinkIsRejectedNotFollowed(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.sql")
	writeTestFile(t, outside, validMarkedSQL("linked"))
	writeTestFile(t, filepath.Join(root, "outside-block.sql"), validBlockSQL)
	os.MkdirAll(filepath.Join(root, "tsql"), 0o700)
	os.MkdirAll(filepath.Join(root, "b"), 0o700)
	if err := os.Symlink(outside, filepath.Join(root, "tsql", "linked.sql")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside-block.sql"), filepath.Join(root, "b", "linked-canon.sql")); err != nil {
		t.Fatal(err)
	}
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(root, "tsql"), BundledDir: filepath.Join(root, "b")})
	for _, want := range []struct {
		src  Source
		path string
	}{{SourceTsqlScripts, "linked.sql"}, {SourceBundled, "linked-canon.sql"}} {
		e, ok := entryByPath(c, want.src, want.path)
		if !ok || e.Rejected != "symbolic link" || e.SQL != "" {
			t.Errorf("%s: %+v ok=%v", want.path, e, ok)
		}
	}
}

func TestOversizeFileIsRejected(t *testing.T) {
	root := t.TempDir()
	pad := strings.Repeat("-- padding padding padding padding padding padding\n", (1<<20)/50+10)
	writeTestFile(t, filepath.Join(root, "tsql", "big-marked.sql"), validMarkedSQL("big-marked")+pad)
	writeTestFile(t, filepath.Join(root, "tsql", "big-plain.sql"), "-- not marked\nSELECT 1;\n"+pad)
	writeTestFile(t, filepath.Join(root, "b", "big-canon.sql"), validBlockSQL+pad)
	c := LoadCatalog(CatalogConfig{TsqlScriptsDir: filepath.Join(root, "tsql"), BundledDir: filepath.Join(root, "b")})
	if e, ok := entryByPath(c, SourceTsqlScripts, "big-marked.sql"); !ok || e.Rejected != "file too large" || e.Name != "big-marked" {
		t.Errorf("marked: %+v ok=%v", e.Rejected, ok)
	}
	if _, ok := entryByPath(c, SourceTsqlScripts, "big-plain.sql"); ok {
		t.Errorf("an unmarked large file must stay out of the catalogue")
	}
	if e, _ := entryByPath(c, SourceBundled, "big-canon.sql"); e.Rejected != "file too large" {
		t.Errorf("bundled: %q", e.Rejected)
	}
}

func TestBOMIsStrippedInEverySource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "b", "bom-canon.sql"), "\xEF\xBB\xBF"+validBlockSQL)
	writeTestFile(t, filepath.Join(root, "p", "_generic", "bom-mine.sql"), "\xEF\xBB\xBF"+validBlockSQL)
	writeTestFile(t, filepath.Join(root, "t", "bom.sql"), "\xEF\xBB\xBF"+validMarkedSQL("bom-script"))
	c := LoadCatalog(CatalogConfig{BundledDir: filepath.Join(root, "b"), PersonalDir: filepath.Join(root, "p"), TsqlScriptsDir: filepath.Join(root, "t")})
	for _, name := range []string{"bom-canon", "bom-mine", "bom-script"} {
		e, ok := c.Find(name)
		if !ok || strings.HasPrefix(e.SQL, "\xEF\xBB\xBF") {
			t.Errorf("%s: BOM not stripped: %+v", name, e)
		}
	}
}

func TestProfileCaseTwinsDisablePersonalLayer(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profiles", "prod-erp", "orders-late.sql"), validBlockSQL)
	c := LoadCatalog(CatalogConfig{PersonalDir: root, Profile: "Prod-ERP", ProfileNames: []string{"Prod-ERP", "prod-erp"}})
	if _, ok := c.Find("orders-late"); ok {
		t.Errorf("case twins share a directory: its queries must not be in the view")
	}
	if !strings.Contains(strings.Join(c.Messages, ";"), "personal queries disabled") {
		t.Errorf("messages = %v", c.Messages)
	}
}

func TestBlockEntryCarriesDirtyReads(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "dirty.sql"), "/* Dirty.\n\n   Parameters: none.\n*/\nSELECT 1 FROM sys.objects WITH (NOLOCK);\n")
	e, ok := LoadCatalog(CatalogConfig{BundledDir: dir}).Find("dirty")
	if !ok || !e.DirtyReads {
		t.Errorf("dirty_reads lost on a bundled entry: %+v", e)
	}
}

func TestMissingBundledDirIsAMessage(t *testing.T) {
	c := LoadCatalog(CatalogConfig{BundledDir: filepath.Join(t.TempDir(), "absent")})
	if strings.Join(c.Messages, ";") != "bundled queries not found;tsql-scripts source not configured" {
		t.Errorf("messages = %v", c.Messages)
	}
}

// The registry key must come from the one read that also produced e.SQL: a
// second read could hash bytes that are not the ones -saved executes.
func TestCatalogReadsEachFileOnce(t *testing.T) {
	opens := map[string]int{}
	orig := openFile
	openFile = func(p string) (*os.File, error) { opens[p]++; return orig(p) }
	t.Cleanup(func() { openFile = orig })
	LoadCatalog(testCatalogConfig("AU-PRD/node1"))
	if len(opens) == 0 {
		t.Fatal("no file opened through openFile")
	}
	for p, n := range opens {
		if n != 1 {
			t.Errorf("%s read %d times", p, n)
		}
	}
}
