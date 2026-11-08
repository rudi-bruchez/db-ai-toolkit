package main

// journal.go has what the write phases check in the journal of the project:
// what a run left on a server when it ends.

import (
	"errors"
	"fmt"
	"strings"

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
