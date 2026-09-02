package sqlq

import (
	"os"
	"path/filepath"
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
			if v := FindWrites(string(body)); len(v) > 0 {
				t.Errorf("the read-only guard refuses this bundled query: statement %q uses %s",
					v[0].Statement, v[0].Keyword)
			}
		})
	}
}
