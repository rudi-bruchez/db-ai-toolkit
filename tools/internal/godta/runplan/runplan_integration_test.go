package runplan_test

import (
	"context"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// TestReadFingerprintsLive proves the live read: on godta_h, a hypothetical
// index created then dropped changes neither field, DROP STATISTICS of a user
// statistic moves modify_date, and a real index changes IndexesSHA256.
func TestReadFingerprintsLive(t *testing.T) {
	p, r := ittest.Profile(t, "GODTA_TEST_PROFILE")
	server := ittest.OpenDB(t, p, r, "")
	dbName := ittest.NewDatabase(t, server, "")
	db := ittest.OpenDB(t, p, r, dbName)
	ittest.LoadDataset(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var id int
	if err := db.QueryRowContext(ctx, "SELECT OBJECT_ID(N'dbo.godta_h')").Scan(&id); err != nil {
		t.Fatal(err)
	}
	read := func() runplan.TableFingerprint {
		t.Helper()
		fp, err := runplan.ReadFingerprints(ctx, db, []int{id, -1})
		if err != nil {
			t.Fatal(err)
		}
		if len(fp) != 1 || fp[0].ObjectID != id {
			t.Fatalf("fingerprints %+v, want one for object %d", fp, id)
		}
		return fp[0]
	}
	exec := func(stmt string) {
		t.Helper()
		// modify_date is a datetime: leave it a tick to move.
		time.Sleep(50 * time.Millisecond)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	h := tsql.Obj{Schema: "dbo", Name: "godta_h"}
	run := workspace.NewRunID()

	base := read()
	t.Logf("base: %+v", base)
	if base.Schema != "dbo" || base.Name != "godta_h" || base.ModifyDate.IsZero() || len(base.IndexesSHA256) != 64 {
		t.Fatalf("base fingerprint %+v", base)
	}

	hypo := runplan.IndexName(run, 1)
	exec(tsql.CreateHypothetical(tsql.IndexDef{Table: h, Name: hypo, Keys: []tsql.KeyCol{{Name: "x"}}, Include: []string{"y"}}))
	if with := read(); with != base {
		t.Errorf("with a hypothetical index: %+v, want %+v", with, base)
	}
	exec(tsql.DropIndex(h, hypo))
	if after := read(); after != base {
		t.Errorf("after a hypothetical index created and dropped: %+v, want %+v", after, base)
	}

	stat := runplan.IndexName(run, 2)
	exec(tsql.CreateStatistics(h, stat, []string{"y"}))
	withStat := read()
	t.Logf("with the statistics: %+v", withStat)
	exec(tsql.DropStatistics(h, stat))
	dropped := read()
	if dropped.ModifyDate.Equal(withStat.ModifyDate) {
		t.Errorf("DROP STATISTICS left modify_date at %v", dropped.ModifyDate)
	}
	if dropped.IndexesSHA256 != base.IndexesSHA256 {
		t.Errorf("DROP STATISTICS changed the index hash")
	}

	exec("CREATE INDEX " + tsql.QuoteName(runplan.IndexName(run, 3)) + " ON " + h.Quoted() + " (y)")
	if real := read(); real.IndexesSHA256 == dropped.IndexesSHA256 {
		t.Errorf("a real index left the index hash at %s", real.IndexesSHA256)
	}
}
