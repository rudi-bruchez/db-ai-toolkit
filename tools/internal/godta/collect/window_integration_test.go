package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/ittest"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/report"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// TestCollectWindowEndsInsideInterval ends the window before the workload
// runs, inside the same Query Store interval (60 minutes by default). The
// Query Store cannot split the interval, so the executions after the end are
// counted: collect records the period the counters really cover, which ends
// after the window, and the report says so.
func TestCollectWindowEndsInsideInterval(t *testing.T) {
	f := newFixture(t, "", true)
	// The workload must fall in the interval the window ends in: away from
	// an hour boundary, where the 60-minute intervals start.
	if left := time.Until(time.Now().Truncate(time.Hour).Add(time.Hour)); left < 2*time.Minute {
		t.Logf("waiting %v for the next interval", left+5*time.Second)
		time.Sleep(left + 5*time.Second)
	}
	until := time.Now()
	ittest.RunWorkload(t, f.db, 3)

	ctx := ctxT(t)
	inst, err := conn.Identify(ctx, f.server)
	if err != nil {
		t.Fatal(err)
	}
	keep, _, err := Databases(ctx, f.server, Options{Include: []string{f.name}})
	if err != nil || len(keep) != 1 {
		t.Fatalf("kept %v: %v", keep, err)
	}
	win := model.Window{From: until.Add(-time.Hour), Until: until}
	w, err := Database(ctx, f.db, inst, keep[0], Options{Window: win})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, q := range w.Queries {
		found = found || strings.Contains(q.Text, "godta_h") && strings.Contains(q.Text, "42")
	}
	if !found {
		t.Fatal("the query x = 42, run after the window end inside its interval, is not counted: the reproduction did not reproduce")
	}
	if w.Database.QueryStoreIntervalMinutes != 60 {
		t.Errorf("QueryStoreIntervalMinutes %d, want 60", w.Database.QueryStoreIntervalMinutes)
	}
	if w.Measured == nil {
		t.Fatal("Measured is nil")
	}
	t.Logf("window %v to %v, measured %v to %v", win.From.UTC(), win.Until.UTC(), w.Measured.From, w.Measured.Until)
	if !w.Measured.Until.After(until) {
		t.Errorf("measured end %v is not after the requested end %v", w.Measured.Until, until.UTC())
	}

	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.DBDir(w.Server.ServerName, w.Database.Name)
	if err := report.Write(ws, dir, w, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, report.ReportFile))
	if err != nil {
		t.Fatal(err)
	}
	want := "counters measured from " + w.Measured.From.Format("2006-01-02 15:04:05") +
		" to " + w.Measured.Until.Format("2006-01-02 15:04:05") + " (UTC): the Query Store counts by interval of 60 minutes"
	if !strings.Contains(string(b), want) {
		t.Errorf("rapport.md does not say %q:\n%s", want, b)
	}
}
