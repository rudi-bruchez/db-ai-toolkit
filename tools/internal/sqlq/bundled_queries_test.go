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
		t.Fatalf("no bundled queries under %s: the library moved or the path is wrong", bundledQueriesDir)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if r := Refusals(string(body)); len(r) > 0 {
				t.Errorf("sqlq would refuse this bundled query: %s", r[0].Reason())
			}
		})
	}
}
