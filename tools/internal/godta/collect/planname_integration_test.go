package collect

import (
	"context"
	"crypto/rand"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
)

// TestCollectRenamedDatabase: a database renamed after its workload ran has
// Query Store plans that name it by its old name, and, as it keeps running,
// new plans that name it by the new one. Collected under the new name, its
// tables are found, the old name is recorded and a warning says so.
func TestCollectRenamedDatabase(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	b := make([]byte, 8)
	rand.Read(b)
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	old := "godta_it_" + string(b)
	renamed := old + "_renamed"
	// final is the name to drop: the database is dropped under the name it
	// has when the test ends, renamed or not.
	final := old
	exec(t, server, "CREATE DATABASE ["+old+"]")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := server.ExecContext(ctx,
			"ALTER DATABASE ["+final+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+final+"];"); err != nil {
			t.Errorf("dropping %s: %v", final, err)
		}
	})
	exec(t, server, "ALTER DATABASE ["+old+"] SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = ALL);")

	db, err := conn.Open(ctxT(t), p, r, old)
	if err != nil {
		t.Fatal(err)
	}
	ittest.LoadDataset(t, db)
	ittest.RunWorkload(t, db, 3)
	db.Close()

	exec(t, server, "ALTER DATABASE ["+old+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; ALTER DATABASE ["+old+"] MODIFY NAME = ["+renamed+"];")
	final = renamed
	exec(t, server, "ALTER DATABASE ["+renamed+"] SET MULTI_USER;")

	f := fixture{p: p, r: r, server: server, name: renamed, db: ittest.OpenDB(t, p, r, renamed)}
	// The database keeps running under its new name: a collect and the
	// drift check and fingerprints of a dry-run read its catalog, another
	// tool does too (an untagged query on sys objects only), and part of
	// the workload runs again, under texts of its own (a comment inside the
	// statement: one after it is not part of the statement text) so that
	// the Query Store keeps new plans, which name the new name.
	first := f.collect(t)
	if _, err := CatalogDrift(ctxT(t), f.db, first); err != nil {
		t.Fatal(err)
	}
	if _, err := runplan.ReadFingerprints(ctxT(t), f.db, slices.Sorted(maps.Keys(first.Tables))); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"SELECT COUNT(*) FROM sys.indexes i JOIN sys.objects o ON o.object_id = i.object_id;",
		"SELECT /* renamed */ y FROM dbo.godta_h WHERE x = 42;",
		"SELECT /* renamed */ a, COUNT_BIG(*) FROM dbo.godta_t GROUP BY a;",
	} {
		exec(t, f.db, stmt)
	}
	exec(t, f.db, "EXEC sys.sp_query_store_flush_db;")

	w := f.collect(t)
	var underNew bool
	for _, q := range append(slices.Clone(w.Queries), w.Writes...) {
		if q.Plan == nil {
			continue
		}
		for _, st := range q.Plan.Statements {
			for _, a := range st.Accesses {
				if strings.EqualFold(a.Obj.Database, renamed) && a.Obj.Schema == "dbo" {
					underNew = true
				}
			}
		}
	}
	if !underNew {
		t.Fatal("no plan names a table of the data set under the new name: the test does not mix old and new plans")
	}
	if !slices.Equal(w.Database.PlanNames, []string{old}) {
		t.Errorf("PlanNames %v, want [%s]", w.Database.PlanNames, old)
	}
	if !slices.ContainsFunc(w.Warnings, func(s string) bool {
		for n := 1; n <= len(w.Queries)+len(w.Writes); n++ {
			if s == WarnPlanName(old, n) {
				return true
			}
		}
		return false
	}) {
		t.Errorf("warnings %q, want %q for some n", w.Warnings, WarnPlanName(old, 0))
	}
	if slices.Contains(w.Warnings, WarnNoTable) {
		t.Errorf("warnings %q, want no %q", w.Warnings, WarnNoTable)
	}
	table(t, w, "godta_t")
	table(t, w, "godta_h")
	table(t, w, "godta_cs")
}
