package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/evaluate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

func warnedWith(ws []string, parts ...string) bool {
	return slices.ContainsFunc(ws, func(w string) bool {
		for _, p := range parts {
			if !strings.Contains(w, p) {
				return false
			}
		}
		return true
	})
}

func (lr *labRun) dropOnLab(db string) {
	lr.t.Helper()
	q := tsql.QuoteName(db)
	if _, err := lr.labServer.ExecContext(lr.ctx, "ALTER DATABASE "+q+" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE "+q+";"); err != nil {
		lr.t.Fatalf("dropping %s from the lab: %v", db, err)
	}
}

// measuredCount is the number of measured entries of evaluate-lab.json.
func measuredCount(t *testing.T, dir string) int {
	t.Helper()
	var ev evaluateFile
	readJSONFile(t, filepath.Join(dir, workspace.EvaluateLabFile), &ev)
	n := 0
	for _, m := range ev.Measures {
		if m.Status == evaluate.StatusMeasured {
			n++
		}
	}
	return n
}

// TestLabRestoredAgain: the restored clone dropped from the lab after a
// first evaluate --target lab, the next one restores it again from the host
// file, says so, and measures, instead of refusing with exit code 3.
func TestLabRestoredAgain(t *testing.T) {
	lr := newLabRun(t)
	dir := lr.collectAndClone()
	lr.step(lr.labArgs...)
	st, err := lab.LoadState(lr.ws, dir)
	if err != nil || st.Prepared == nil {
		t.Fatalf("lab.json after evaluate --target lab: %+v, %v", st, err)
	}
	db := st.Prepared.Database
	lr.dropOnLab(db)
	if err := os.Remove(filepath.Join(dir, workspace.EvaluateLabFile)); err != nil {
		t.Fatal(err)
	}

	out := lr.step(lr.labArgs...)
	if len(out.Databases) != 1 || out.Databases[0].Status != "evaluated on the lab" {
		t.Fatalf("evaluate --target lab after the drop: %+v", out.Databases)
	}
	if !warnedWith(out.Databases[0].Warnings, db, "restored it again") {
		t.Errorf("no warning says %s was restored again: %q", db, out.Databases[0].Warnings)
	}
	if err := lab.CheckCloneDB(lr.ctx, lr.labServer, db); err != nil {
		t.Errorf("after the second evaluate --target lab: %v", err)
	}
	if n := measuredCount(t, dir); n == 0 {
		t.Errorf("evaluate-lab.json has no measure after the restore again")
	}
	lr.checkSourceClean()
}

// TestLabSupersededClone: a second clone of the same database records the
// first in lab.json and says what becomes of it; the next evaluate --target
// lab drops the first restored database from the lab, lists it, and leaves
// the first file on the host with the command that removes it. Once that
// file is gone, the entry leaves lab.json.
func TestLabSupersededClone(t *testing.T) {
	lr := newLabRun(t)
	dir := lr.collectAndClone()
	lr.step(lr.labArgs...)
	first, err := lab.LoadState(lr.ws, dir)
	if err != nil || first.Prepared == nil {
		t.Fatalf("lab.json after evaluate --target lab: %+v, %v", first, err)
	}
	db1, file1 := first.Prepared.Database, first.File.HostPath
	rm1 := "rm -- '" + file1 + "'"

	out := lr.cloneOnly(dir)
	if len(out.Databases) != 1 {
		t.Fatalf("second clone: %+v", out.Databases)
	}
	if w := out.Databases[0].Warnings; !warnedWith(w, db1, "dropped from the lab") || !warnedWith(w, rm1) {
		t.Errorf("the second clone does not say what becomes of %s and %s: %q", db1, file1, w)
	}
	second, err := lab.LoadState(lr.ws, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []lab.Superseded{{File: first.File, Database: db1}}; !slices.Equal(second.Superseded, want) {
		t.Errorf("lab.json superseded after the second clone: %+v, want %+v", second.Superseded, want)
	}

	out = lr.step(lr.labArgs...)
	w := out.Databases[0].Warnings
	if !warnedWith(w, "dropped "+db1+" from the lab") {
		t.Errorf("evaluate --target lab does not list the drop of %s: %q", db1, w)
	}
	if !warnedWith(w, rm1) {
		t.Errorf("evaluate --target lab does not give the command that removes %s: %q", file1, w)
	}
	if err := lab.CheckCloneDB(lr.ctx, lr.labServer, db1); !errors.Is(err, lab.ErrCloneDBMissing) {
		t.Errorf("the replaced clone %s on the lab: %v, want it dropped", db1, err)
	}
	third, err := lab.LoadState(lr.ws, dir)
	if err != nil || third.Prepared == nil {
		t.Fatalf("lab.json: %+v, %v", third, err)
	}
	if err := lab.CheckCloneDB(lr.ctx, lr.labServer, third.Prepared.Database); err != nil || third.Prepared.Database == db1 {
		t.Errorf("the current clone %s: %v", third.Prepared.Database, err)
	}
	if _, err := os.Stat(file1); err != nil {
		t.Errorf("the replaced clone's file was touched on the host: %v", err)
	}
	if want := []lab.Superseded{{File: first.File}}; !slices.Equal(third.Superseded, want) {
		t.Errorf("lab.json superseded after the lab preparation: %+v, want %+v", third.Superseded, want)
	}
	if n := measuredCount(t, dir); n == 0 {
		t.Errorf("evaluate-lab.json has no measure")
	}

	if err := os.Remove(file1); err != nil {
		t.Fatal(err)
	}
	out = lr.step(lr.labArgs...)
	if warnedWith(out.Databases[0].Warnings, file1) {
		t.Errorf("%s removed, still reported: %q", file1, out.Databases[0].Warnings)
	}
	last, err := lab.LoadState(lr.ws, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Superseded) != 0 {
		t.Errorf("lab.json superseded once the file is removed: %+v", last.Superseded)
	}
	lr.checkSourceClean()
}
