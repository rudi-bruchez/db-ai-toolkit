package sqlq

import (
	"os"
	"testing"
)

// Run by the user after adding markers: every marked script of the real clone
// must be usable. Skipped without a clone. The bundled queries are loaded too,
// so that a marker name colliding with a bundled query is caught here rather
// than on the first -saved.
func TestRealCloneHasNoRejectedEntry(t *testing.T) {
	dir := os.Getenv("DB_AI_TOOLKIT_TSQL_SCRIPTS")
	if dir == "" {
		t.Skip("DB_AI_TOOLKIT_TSQL_SCRIPTS not set")
	}
	c := LoadCatalog(CatalogConfig{BundledDir: bundledQueriesDir, TsqlScriptsDir: dir})
	for _, m := range c.Messages {
		t.Errorf("catalogue message: %s", m)
	}
	marked := 0
	for _, e := range c.Entries {
		if e.Source == SourceTsqlScripts {
			marked++
		}
		if e.Rejected != "" {
			t.Errorf("%s:%s (%s): %s", e.Source, e.Path, e.Name, e.Rejected)
		}
	}
	if marked == 0 {
		t.Fatal("no marked script found: the clone path is wrong or no marker was added")
	}
	t.Logf("%d marked scripts, %d entries in all", marked, len(c.Entries))
}
