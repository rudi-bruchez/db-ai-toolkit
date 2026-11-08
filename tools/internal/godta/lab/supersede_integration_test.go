package lab_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
)

// TestRestoreKeepsSuperseded: Restore saves lab.json with Prepared and keeps
// the Superseded list clone wrote there, so that a run stopped between the
// restore and lab.Supersede does not forget the replaced clones.
func TestRestoreKeepsSuperseded(t *testing.T) {
	e := newLabEnv(t)
	f := newCloneFixture(t)
	p, file := cloneToLab(t, f, e)
	db := runplan.CloneDatabaseName(p.RunID)
	dropOnCleanup(t, e.server, db)
	dir := f.ws.DBDir(p.Server, p.Database)
	want := []lab.Superseded{{File: lab.File{HostPath: "/nowhere/godta_zz99zz99_c.bak", Bytes: 1, SHA256: "00"}, Database: "godta_zz99zz99_c"}}
	if err := lab.SaveState(f.ws, dir, lab.State{File: file, Superseded: want}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := lab.Restore(ctx, f.ws, dir, e.server, e.open, f.inst, e.inst, f.w, file, e.containerDir, "", db); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	st, err := lab.LoadState(f.ws, dir)
	if err != nil || st.Prepared == nil {
		t.Fatalf("lab.json after Restore: %+v, %v", st, err)
	}
	if !slices.Equal(st.Superseded, want) {
		t.Errorf("lab.json superseded after Restore: %+v, want %+v", st.Superseded, want)
	}
}
