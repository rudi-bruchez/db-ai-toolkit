package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRunIDShape(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z0-9]{8}$`)
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := NewRunID()
		if !shape.MatchString(id) {
			t.Fatalf("run ID %q does not match %s", id, shape)
		}
		if seen[id] {
			t.Fatalf("run ID %q drawn twice in %d draws", id, i+1)
		}
		seen[id] = true
	}
}

func TestSafeNameDistinguishes(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slash := ws.DBDir("srv", "a/b")
	under := ws.DBDir("srv", "a_b")
	if slash == under {
		t.Fatalf("a/b and a_b share the directory %s", slash)
	}
	if got := filepath.Base(under); got != "a_b" {
		t.Errorf("a_b needs no replacement, got directory %q", got)
	}
	if rel, err := filepath.Rel(ws.Root, slash); err != nil || strings.Count(rel, string(filepath.Separator)) != 1 {
		t.Errorf("a/b must give one path component under the server, got %q (%v)", rel, err)
	}
}

func TestDBDirStaysUnderRoot(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ server, db string }{
		{"..", "x"},
		{"srv", ".."},
		{"srv", "."},
		{"", ""},
	}
	for _, c := range cases {
		dir := ws.DBDir(c.server, c.db)
		rel, err := filepath.Rel(ws.Root, dir)
		if err != nil {
			t.Fatalf("server %q db %q: %v", c.server, c.db, err)
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 2 {
			t.Errorf("server %q db %q: want two components under root, got %q", c.server, c.db, rel)
		}
		for _, p := range parts {
			if p == ".." || p == "." || p == "" {
				t.Errorf("server %q db %q: component %q in %q", c.server, c.db, p, rel)
			}
		}
	}
}

func entry(name, state string) Entry {
	return Entry{
		RunID: "abcd1234", Server: "srv", Database: "db", Kind: KindHypotheticalIndex,
		Schema: "dbo", Table: "t", Name: name, At: time.Now().UTC(), State: state,
	}
}

func TestJournalPending(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		entry("godta_abcd1234_x", StatePlanned),
		entry("godta_abcd1234_y", StatePlanned),
		entry("godta_abcd1234_x", StateCreated),
		entry("godta_abcd1234_x", StateRemoved),
	} {
		if err := ws.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ws.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "godta_abcd1234_y" || got[0].State != StatePlanned {
		t.Fatalf("Pending = %+v, want only y in state planned", got)
	}
	info, err := os.Stat(filepath.Join(ws.Root, JournalFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("journal mode = %o, want 600", perm)
	}
}

func TestJournalSurvivesTruncatedLine(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Record(entry("godta_abcd1234_x", StateCreated)); err != nil {
		t.Fatal(err)
	}
	// A crash in the middle of a write leaves a partial last line.
	f, err := os.OpenFile(filepath.Join(ws.Root, JournalFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"run_id":"abcd1234","server":"srv","name":"godta_ab`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var warnings []string
	old := Warnf
	Warnf = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { Warnf = old })

	got, err := ws.Pending()
	if err != nil {
		t.Fatalf("Pending with a truncated last line: %v", err)
	}
	if len(got) != 1 || got[0].Name != "godta_abcd1234_x" {
		t.Fatalf("Pending = %+v, want the complete entry x", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("want one warning for the truncated line, got %q", warnings)
	}

	// The next Record must not glue its line onto the fragment.
	warnings = nil
	if err := ws.Record(entry("godta_abcd1234_y", StatePlanned)); err != nil {
		t.Fatal(err)
	}
	got, err = ws.Pending()
	if err != nil {
		t.Fatalf("Pending after a Record that follows a truncated line: %v", err)
	}
	if len(got) != 2 || got[1].Name != "godta_abcd1234_y" {
		t.Fatalf("Pending = %+v, want x then y", got)
	}
}

func TestJournalCorruptMiddleLineFails(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws.Root, JournalFile)
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ws.Record(entry("godta_abcd1234_x", StateCreated)); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Pending(); err == nil {
		t.Fatal("a corrupt line followed by others must be an error, not skipped")
	}
}

func TestWriteReadJSON(t *testing.T) {
	ws, err := Open(filepath.Join(t.TempDir(), "project"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(ws.Root); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("root mode = %o, want 700", perm)
	}
	dir := ws.DBDir("srv", "db")
	in := map[string]int{"a": 1}
	if err := ws.WriteJSON(dir, CollectFile, in); err != nil {
		t.Fatal(err)
	}
	var out map[string]int
	if err := ws.ReadJSON(dir, CollectFile, &out); err != nil {
		t.Fatal(err)
	}
	if out["a"] != 1 {
		t.Fatalf("read back %v", out)
	}
	info, err := os.Stat(filepath.Join(dir, CollectFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "escape.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ws.ReadJSON(dir, "../escape.json", &out); err == nil {
		t.Error("ReadJSON must refuse a name with a path separator")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp*"))
	if len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
