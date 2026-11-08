package main

// journal.go has what the write phases check in the journal of the project:
// what a run left on a server when it ends, and whether a plan has already
// run or was replaced by a newer --dry-run.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// cleanupHint is what a user does about objects left on a server.
const cleanupHint = "run godta cleanup --dry-run, then godta cleanup --allow-write --plan <the plan it wrote>"

// describeEntry is one journaled object, as the output lists it.
func describeEntry(e workspace.Entry) string {
	desc := e.Kind + " " + e.Name
	if e.Table != "" {
		desc = e.Kind + " " + e.Schema + "." + e.Table + "." + e.Name
	}
	if e.Path != "" {
		desc += " (" + e.Path + ")"
	}
	return desc
}

func describeEntries(list []workspace.Entry) string {
	descs := make([]string, len(list))
	for i, e := range list {
		descs[i] = describeEntry(e)
	}
	return strings.Join(descs, ", ")
}

// objects is "1 object" or "n objects".
func objects(n int) string {
	if n == 1 {
		return "1 object"
	}
	return fmt.Sprintf("%d objects", n)
}

// noteLeft ends the result of a write phase of run runID: every object of
// the run whose last journal state is "created" is still on the server, so
// the database's status says so, a warning names the objects and says to
// run cleanup, and the run exits 2 (spec section 7.1: a run that leaves
// objects behind is not a success). A temporary procedure disappears with
// its session and does not count.
func (a *app) noteLeft(r *dbResult, runID string) {
	if runID == "" {
		return
	}
	pending, err := a.ws.Pending()
	if err != nil {
		a.failed = true
		r.Warnings = append(r.Warnings, fmt.Sprintf("reading the journal to check what run %s left: %v; %s", runID, err, cleanupHint))
		return
	}
	var left []workspace.Entry
	for _, e := range pending {
		if e.RunID == runID && e.State == workspace.StateCreated && e.Kind != workspace.KindTempProcedure {
			left = append(left, e)
		}
	}
	if len(left) == 0 {
		return
	}
	a.failed = true
	r.Status += fmt.Sprintf(", %s left for cleanup", objects(len(left)))
	r.Warnings = append(r.Warnings, fmt.Sprintf("run %s left %s on the server: %s; %s",
		runID, objects(len(left)), describeEntries(left), cleanupHint))
}

// planUnused refuses a plan whose run ID already has entries in the
// journal: the plan was executed before (a run killed with its objects
// created, or one that ended), and executing it again would create objects
// under names that exist, measure nothing and leave the first run's objects
// in place. Nothing is sent.
func (a *app) planUnused(file string, plan runplan.Plan) error {
	entries, err := a.ws.Entries()
	if err != nil {
		return failedf(fmt.Errorf("--plan %s: reading the journal: %w", file, err))
	}
	n := 0
	for _, e := range entries {
		if e.RunID == plan.RunID {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return refusedf("--plan %s refused: run %s of this plan has already been executed (%d journal entries); an execution plan runs once. "+
		"To start again, %s, to remove what it left, then run godta %s --dry-run for a new plan",
		file, plan.RunID, n, cleanupHint, plan.Phase)
}

// planCurrent refuses an evaluate plan that a later --dry-run replaced: the
// candidates of the workspace are those of the current plan, not of the one
// passed, and the plan approved is not the plan that would run.
func (a *app) planCurrent(file string, plan runplan.Plan) error {
	dir := a.ws.DBDir(plan.Server, plan.Database)
	name := workspace.PlanFile(plan.Phase)
	var cur runplan.Plan
	if err := a.ws.ReadJSON(dir, name, &cur); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return failedf(fmt.Errorf("--plan %s: reading the current plan: %w", file, err))
	}
	if cur.RunID == plan.RunID {
		return nil
	}
	return refusedf("--plan %s refused: it is run %s, made at %s, and a later %s --dry-run replaced it: "+
		"the current plan of %s is %s, run %s, made at %s, and the candidates of the workspace are that plan's. "+
		"Show the current plan and pass it to --plan once it is accepted, or run --dry-run again",
		file, plan.RunID, plan.CreatedAt.Format(time.RFC3339), plan.Phase,
		plan.Database, filepath.Join(dir, name), cur.RunID, cur.CreatedAt.Format(time.RFC3339))
}

// refuse records a database whose plan was refused before anything ran.
func (a *app) refuse(r *dbResult, err error) {
	var ce *codeError
	if errors.As(err, &ce) && ce.code == exitRefused {
		a.refused = true
		r.Status = "refused"
		r.Error = err.Error()
		return
	}
	a.dbFailed(r, err)
}
