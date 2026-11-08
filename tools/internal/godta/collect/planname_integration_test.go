package collect

import (
	"context"
	"crypto/rand"
	"slices"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
)

// TestCollectRenamedDatabase: a database renamed after its workload ran has
// Query Store plans that name it by its old name. Collected under the new
// name, its tables are found, the old name is recorded and a warning says so.
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
	w := f.collect(t)
	if !slices.Equal(w.Database.PlanNames, []string{old}) {
		t.Errorf("PlanNames %v, want [%s]", w.Database.PlanNames, old)
	}
	if !slices.Contains(w.Warnings, WarnPlanName(old)) {
		t.Errorf("warnings %q, want %q", w.Warnings, WarnPlanName(old))
	}
	if slices.Contains(w.Warnings, WarnNoTable) {
		t.Errorf("warnings %q, want no %q", w.Warnings, WarnNoTable)
	}
	table(t, w, "godta_t")
	table(t, w, "godta_h")
}
