package cleanup

import (
	"errors"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// TestCheckStep: the steps runplan.ForCleanup writes pass; a step whose
// entry is not of the run name form, whose SQL is not the template of its
// entry, without entry, or a backup file whose path names another file, is
// refused with ErrForeignStep.
func TestCheckStep(t *testing.T) {
	entries := []workspace.Entry{
		{Server: "s", Database: "app", Kind: workspace.KindHypotheticalIndex, Schema: "dbo", Table: "t", Name: "godta_abcd1234_1"},
		{Server: "s", Database: "app", Kind: workspace.KindStatistics, Schema: "dbo", Table: "t", Name: "godta_abcd1234_s1"},
		{Server: "s", Database: ServerDatabase, Kind: workspace.KindCloneDatabase, Name: "godta_abcd1234_c"},
		{Server: "s", Database: ServerDatabase, Kind: workspace.KindBackupFile, Name: "godta_abcd1234_c.bak", Path: `D:\backup\godta_abcd1234_c.bak`},
	}
	p := runplan.ForCleanup(entries)
	if len(p.Steps) != len(entries) {
		t.Fatalf("steps %+v, want one per entry", p.Steps)
	}
	for _, s := range p.Steps {
		if err := checkStep(s); err != nil {
			t.Errorf("step %d of ForCleanup refused: %v", s.N, err)
		}
	}
	with := func(n int, f func(s *runplan.Step, e *workspace.Entry)) runplan.Step {
		s := p.Steps[n]
		e := *s.Journal
		f(&s, &e)
		s.Journal = &e
		return s
	}
	for name, s := range map[string]runplan.Step{
		"SQL of a real index":   with(0, func(s *runplan.Step, _ *workspace.Entry) { s.SQL = "DROP INDEX [pk] ON [dbo].[t]" }),
		"entry of a real index": with(0, func(s *runplan.Step, e *workspace.Entry) { e.Name = "pk"; s.SQL = "DROP INDEX [pk] ON [dbo].[t]" }),
		"real database":         with(2, func(s *runplan.Step, e *workspace.Entry) { e.Name = "app"; s.SQL = "DROP DATABASE [app]" }),
		"other backup file": with(3, func(s *runplan.Step, e *workspace.Entry) {
			e.Path = `D:\backup\app_full.bak`
			s.SQL = "EXEC master.dbo.xp_delete_file 0, N'D:\\backup\\app_full.bak'"
		}),
		"no entry": {N: 9, SQL: "DROP DATABASE [app]"},
	} {
		if err := checkStep(s); !errors.Is(err, ErrForeignStep) {
			t.Errorf("%s: checkStep %v, want ErrForeignStep", name, err)
		}
	}
}
