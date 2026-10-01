package sqlq

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bundledQueriesDir is the skill's canonical query library, relative to this
// package.
const bundledQueriesDir = "../../../plugins/sqlserver-toolkit/skills/live-query/queries"

// The guard and the query library have to agree: a canonical query the guard
// refuses is a query nobody can run. This catches that at build time rather
// than in front of an instance.
func TestBundledQueriesPassTheReadOnlyGuard(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(bundledQueriesDir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skipf("no bundled queries under %s; nothing to check", bundledQueriesDir)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if r := refusals(string(body)); len(r) > 0 {
				t.Errorf("sqlq would refuse this bundled query: %s", strings.Join(r, "; "))
			}
		})
	}
}

// refusals lists why sqlq would refuse to run this text, in the order sqlq
// checks: writes, then context changes, then batch separators.
func refusals(body string) []string {
	var out []string
	if v := FindWrites(body); len(v) > 0 {
		out = append(out, fmt.Sprintf("statement %q uses %s", v[0].Statement, v[0].Keyword))
	}
	if c := FindContextChanges(body); len(c) > 0 {
		out = append(out, fmt.Sprintf("statement %q uses %s", c[0].Statement, c[0].Keyword))
	}
	if lines := FindBatchSeparators(body); len(lines) > 0 {
		out = append(out, fmt.Sprintf("GO batch separator on line %d", lines[0]))
	}
	return out
}

func TestRefusalsCatchesWhatSqlqRefuses(t *testing.T) {
	cases := map[string]string{
		"write":     "DELETE FROM dbo.T;",
		"use":       "USE master;\nSELECT 1;",
		"separator": "SELECT 1;\nGO\nSELECT 2;",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if len(refusals(body)) == 0 {
				t.Errorf("refusals(%q) found nothing; sqlq would refuse it", body)
			}
		})
	}
	if r := refusals("SELECT TOP (1) name FROM sys.objects;"); len(r) > 0 {
		t.Errorf("a plain SELECT was refused: %v", r)
	}
}
