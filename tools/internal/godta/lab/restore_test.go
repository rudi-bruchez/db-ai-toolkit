package lab_test

import (
	"context"
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/evaluate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/lab"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
)

// TestStatsHookLookup: a candidate of two keys whose stream is in Prepared
// gets exactly one UPDATE STATISTICS of that stream on its hypothetical
// index and no note; one of two keys without a stream gets nothing sent and
// the "unreliable estimate" note; one of one key without a stream gets
// nothing sent and no note.
func TestStatsHookLookup(t *testing.T) {
	const table = 1001
	w := &model.Workload{Tables: map[int]*model.Table{table: {ObjectID: table, Schema: "dbo", Name: "t"}}}
	ab := []model.KeyCol{{ColumnID: 2}, {ColumnID: 3, Desc: true}}
	stream := []byte{0x01, 0x02, 0xAB}
	p := lab.Prepared{Database: "godta_abcd1234_c", Streams: map[string][]byte{lab.StreamKey(table, ab): stream}}
	h := lab.StatsHook(p, w)
	if h.AfterCreateHypothetical == nil {
		t.Fatal("StatsHook has no AfterCreateHypothetical")
	}
	ctx := context.Background()
	want := tsql.UpdateStatisticsStream(tsql.Obj{Schema: "dbo", Name: "t"}, "godta_abcd1234_1", stream)

	s, rec := evaluate.NewRecordingSession(conn.Instance{})
	// Same keys in the same order; the direction is not part of the key.
	present := model.Candidate{ID: "c1", Table: table, Keys: []model.KeyCol{{ColumnID: 2, Desc: true}, {ColumnID: 3}}}
	notes, err := h.AfterCreateHypothetical(ctx, s, present, "godta_abcd1234_1")
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if !slices.Equal(rec.Statements, []string{want}) {
		t.Errorf("present: sent %q, want exactly %q", rec.Statements, want)
	}
	if len(notes) != 0 {
		t.Errorf("present: notes %q, want none", notes)
	}

	s, rec = evaluate.NewRecordingSession(conn.Instance{})
	absent := model.Candidate{ID: "c2", Table: table, Keys: []model.KeyCol{{ColumnID: 3}, {ColumnID: 2}}}
	notes, err = h.AfterCreateHypothetical(ctx, s, absent, "godta_abcd1234_2")
	if err != nil {
		t.Fatalf("absent: %v", err)
	}
	if len(rec.Statements) != 0 {
		t.Errorf("absent: sent %q, want nothing", rec.Statements)
	}
	if !slices.Equal(notes, []string{lab.NoteStatsNotTransferred}) {
		t.Errorf("absent: notes %q, want [%q]", notes, lab.NoteStatsNotTransferred)
	}
	if lab.NoteStatsNotTransferred != "unreliable estimate: statistics not transferred; evaluate on the server or run clone again" {
		t.Errorf("note %q is not the plan's", lab.NoteStatsNotTransferred)
	}

	s, rec = evaluate.NewRecordingSession(conn.Instance{})
	single := model.Candidate{ID: "c3", Table: table, Keys: []model.KeyCol{{ColumnID: 2}}}
	notes, err = h.AfterCreateHypothetical(ctx, s, single, "godta_abcd1234_3")
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	if len(rec.Statements) != 0 || len(notes) != 0 {
		t.Errorf("single key: sent %q, notes %q; want nothing", rec.Statements, notes)
	}
}

// TestEffectiveMaxDOP: the database's MAXDOP, else the instance's, else
// every processor, bounded by the processor count.
func TestEffectiveMaxDOP(t *testing.T) {
	db := func(v string) []tsql.ScopedConfig {
		return []tsql.ScopedConfig{{Name: "LEGACY_CARDINALITY_ESTIMATION", Value: "0", BaseType: "bit"}, {Name: "MAXDOP", Value: v, BaseType: "int"}}
	}
	for _, c := range []struct {
		configs  []tsql.ScopedConfig
		instance int
		cpus     int
		want     int
	}{
		{db("2"), 8, 16, 2},
		{db("0"), 8, 16, 8},
		{db("0"), 0, 16, 16},
		{nil, 4, 16, 4},
		{db("32"), 0, 16, 16},
		{db("0"), 32, 16, 16},
	} {
		got := lab.EffectiveMaxDOP(c.configs, conn.Instance{MaxDOP: c.instance, CPUCount: c.cpus})
		if got != c.want {
			t.Errorf("EffectiveMaxDOP(database %v, instance %d, %d processors) = %d, want %d", c.configs, c.instance, c.cpus, got, c.want)
		}
	}
}
