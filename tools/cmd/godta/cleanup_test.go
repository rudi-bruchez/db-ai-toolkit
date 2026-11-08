package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// TestCleanupIncludeListsClone: cleanup --include app --dry-run plans the
// clone database and the backup file journaled from app, under app, and
// warns about a clone journaled without its source, instead of dropping
// them from the plan in silence.
func TestCleanupIncludeListsClone(t *testing.T) {
	f := newEvalFix(t)
	entry := func(kind, name, path, source, run string) workspace.Entry {
		return workspace.Entry{RunID: run, Server: "lab01", Database: "master", Kind: kind, Name: name, Path: path,
			Source: source, State: workspace.StateCreated, At: time.Now().UTC()}
	}
	for _, e := range []workspace.Entry{
		entry(workspace.KindCloneDatabase, "godta_abcd1234_c", "", "app", "abcd1234"),
		entry(workspace.KindBackupFile, "godta_abcd1234_c.bak", "/var/opt/mssql/backup/godta_abcd1234_c.bak", "app", "abcd1234"),
		entry(workspace.KindCloneDatabase, "godta_ffff0000_c", "", "", "ffff0000"),
	} {
		if err := f.ws.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	code, out := invoke(t, f.fake.open, append([]string{"cleanup", "--dry-run", "--include", "app"}, f.args...)...)
	if code != exitOK || out.Plan == "" {
		t.Fatalf("cleanup --dry-run: code %d, output %+v", code, out)
	}
	if len(out.Databases) != 1 || out.Databases[0].Name != "app" || out.Databases[0].Status != "2 to remove" {
		t.Fatalf("databases %+v, want app with 2 to remove", out.Databases)
	}
	got := strings.Join(out.Databases[0].Warnings, "\n")
	for _, want := range []string{"clone-database godta_abcd1234_c", "backup-file godta_abcd1234_c.bak"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan of app lacks %q:\n%s", want, got)
		}
	}
	if !slices.ContainsFunc(out.Warnings, func(w string) bool { return strings.Contains(w, "godta_ffff0000_c") }) {
		t.Errorf("warnings %q do not list the clone without source", out.Warnings)
	}
}
