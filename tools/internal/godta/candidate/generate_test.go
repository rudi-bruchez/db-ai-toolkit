package candidate

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
)

// The object_ids and column_ids of the test workload follow the Annex A DDL:
// godta_t (id, a, b, c, pad) clustered on id, godta_h (x, y) a heap with
// nix_h_x on x, godta_v the indexed view over godta_t, godta_cs with a
// nonclustered columnstore.
const (
	tblT  = 101
	tblH  = 102
	tblV  = 103
	tblCS = 104
	// dbName is the database the fixtures were captured in.
	dbName = "godta_it_3c6x48yu"
)

const (
	colID = iota + 1
	colA
	colB
	colC
	colPad
	colD
)

func workload() *model.Workload {
	cols := func(names ...string) []model.Column {
		var out []model.Column
		for i, n := range names {
			out = append(out, model.Column{ID: i + 1, Name: n, Type: "int"})
		}
		return out
	}
	return &model.Workload{
		Database: model.DBInfo{Name: dbName, Collation: "SQL_Latin1_General_CP1_CI_AS"},
		Tables: map[int]*model.Table{
			tblT: {ObjectID: tblT, Schema: "dbo", Name: "godta_t", Columns: cols("id", "a", "b", "c", "pad", "d"),
				Indexes: []model.Index{{ObjectID: tblT, IndexID: 1, Name: "pk_godta_t", Kind: model.KindClustered,
					Keys: []model.KeyCol{{ColumnID: colID}}, Unique: true, PrimaryKey: true}},
				Views: []int{tblV}},
			tblH: {ObjectID: tblH, Schema: "dbo", Name: "godta_h", IsHeap: true, Columns: cols("x", "y"),
				Indexes: []model.Index{
					{ObjectID: tblH, IndexID: 0, Kind: model.KindHeap},
					{ObjectID: tblH, IndexID: 2, Name: "nix_h_x", Kind: model.KindNonclustered, Keys: []model.KeyCol{{ColumnID: 1}}},
				}},
			tblV: {ObjectID: tblV, Schema: "dbo", Name: "godta_v", Columns: cols("a", "n"),
				Indexes: []model.Index{{ObjectID: tblV, IndexID: 1, Name: "godta_v_cx", Kind: model.KindViewClustered,
					Keys: []model.KeyCol{{ColumnID: 1}}, Unique: true}}},
			tblCS: {ObjectID: tblCS, Schema: "dbo", Name: "godta_cs", HasColumnstore: true, Columns: cols("k", "v"),
				Indexes: []model.Index{
					{ObjectID: tblCS, IndexID: 1, Name: "pk_cs", Kind: model.KindClustered, Keys: []model.KeyCol{{ColumnID: 1}}, Unique: true},
					{ObjectID: tblCS, IndexID: 2, Name: "ncci_cs", Kind: model.KindNonclusteredColumnstore},
				}},
		},
	}
}

func col(table, name string) showplan.ColRef {
	return showplan.ColRef{Database: dbName, Schema: "dbo", Table: table, Column: name}
}

func cs(table string, names ...string) []showplan.ColRef {
	var out []showplan.ColRef
	for _, n := range names {
		out = append(out, col(table, n))
	}
	return out
}

func scan(table string) showplan.Access {
	return showplan.Access{
		Obj:      showplan.ObjRef{Database: dbName, Schema: "dbo", Table: table},
		Op:       "Clustered Index Scan",
		LookupOf: -1,
	}
}

// query wraps statements with a plan into a Query: the parser gives a
// planned statement a Root, the generator reads only those.
func query(id int64, stmts ...showplan.Statement) model.Query {
	for i := range stmts {
		stmts[i].Root = &showplan.RelOp{PhysicalOp: "Clustered Index Scan"}
	}
	return model.Query{QueryID: id, Plan: &showplan.Plan{Statements: stmts}}
}

func keys(kc ...model.KeyCol) []model.KeyCol { return kc }
func asc(id int) model.KeyCol                { return model.KeyCol{ColumnID: id} }
func desc(id int) model.KeyCol               { return model.KeyCol{ColumnID: id, Desc: true} }

// find returns the candidates on table with the given keys.
func find(cands []model.Candidate, table int, k []model.KeyCol) []model.Candidate {
	var out []model.Candidate
	for _, c := range cands {
		if c.Table == table && slices.Equal(c.Keys, k) {
			out = append(out, c)
		}
	}
	return out
}

func one(t *testing.T, cands []model.Candidate, table int, k []model.KeyCol) model.Candidate {
	t.Helper()
	got := find(cands, table, k)
	if len(got) != 1 {
		t.Fatalf("%d candidates on %d with keys %v, want 1; all: %+v", len(got), table, k, cands)
	}
	return got[0]
}

func TestEqualityThenRange(t *testing.T) {
	w := workload()
	a := scan("godta_t")
	a.PredEq = cs("godta_t", "a")
	a.PredRange = cs("godta_t", "b")
	a.Output = cs("godta_t", "c")
	w.Queries = []model.Query{query(1, showplan.Statement{Accesses: []showplan.Access{a}})}
	cands := Generate(w)
	if len(cands) != 1 {
		t.Fatalf("%d candidates, want 1: %+v", len(cands), cands)
	}
	c := cands[0]
	if c.Table != tblT || !slices.Equal(c.Keys, keys(asc(colA), asc(colB))) {
		t.Errorf("keys %v on %d, want (a, b) on godta_t", c.Keys, c.Table)
	}
	if !slices.Equal(c.Include, []int{colC}) {
		t.Errorf("include %v, want (c)", c.Include)
	}
	if c.EqCount != 1 {
		t.Errorf("EqCount %d, want 1", c.EqCount)
	}
	if c.Origin != OriginAlgorithm {
		t.Errorf("Origin %q, want %q", c.Origin, OriginAlgorithm)
	}
	if !slices.Equal(c.Queries, []int64{1}) {
		t.Errorf("Queries %v, want [1]", c.Queries)
	}
	for _, r := range []string{RuleEqFirst, RuleOneRangeOrSort, RuleIncludeOutputs} {
		if !slices.Contains(c.Rules, r) {
			t.Errorf("Rules %v, want %q", c.Rules, r)
		}
	}
	if c.ID == "" {
		t.Error("empty ID")
	}
}

func TestSortAfterRangeDropped(t *testing.T) {
	w := workload()
	a := scan("godta_t")
	a.PredEq = cs("godta_t", "a")
	a.PredRange = cs("godta_t", "b")
	a.Output = cs("godta_t", "c")
	st := showplan.Statement{Accesses: []showplan.Access{a},
		Sorts: []showplan.SortReq{{Cols: cs("godta_t", "c"), Desc: []bool{false}}}}
	w.Queries = []model.Query{query(1, st)}
	c := one(t, Generate(w), tblT, keys(asc(colA), asc(colB)))
	if !slices.Contains(c.Rules, RuleSortAfterRangeDropped) {
		t.Errorf("Rules %v, want %q", c.Rules, RuleSortAfterRangeDropped)
	}
}

func TestRangeIsFirstSortColumn(t *testing.T) {
	w := workload()
	a := scan("godta_t")
	a.PredEq = cs("godta_t", "a")
	a.PredRange = cs("godta_t", "b")
	a.Output = cs("godta_t", "c")
	st := showplan.Statement{Accesses: []showplan.Access{a},
		Sorts: []showplan.SortReq{{Cols: cs("godta_t", "b"), Desc: []bool{true}}}}
	w.Queries = []model.Query{query(1, st)}
	c := one(t, Generate(w), tblT, keys(asc(colA), desc(colB)))
	if slices.Contains(c.Rules, RuleSortAfterRangeDropped) {
		t.Errorf("Rules %v: the sort was kept, %q does not apply", c.Rules, RuleSortAfterRangeDropped)
	}
}

func TestClusteredKeyNotIncluded(t *testing.T) {
	w := workload()
	a := scan("godta_t")
	a.PredEq = cs("godta_t", "a")
	a.Output = cs("godta_t", "id", "c")
	w.Queries = []model.Query{query(1, showplan.Statement{Accesses: []showplan.Access{a}})}
	c := one(t, Generate(w), tblT, keys(asc(colA)))
	if !slices.Equal(c.Include, []int{colC}) {
		t.Errorf("include %v, want (c) without the clustered key id", c.Include)
	}
	if !slices.Contains(c.Rules, RuleClusteredKeyImplicit) {
		t.Errorf("Rules %v, want %q", c.Rules, RuleClusteredKeyImplicit)
	}
}

func TestMergeSameKeys(t *testing.T) {
	w := workload()
	a1 := scan("godta_t")
	a1.PredEq = cs("godta_t", "a")
	a1.Output = cs("godta_t", "c")
	a2 := scan("godta_t")
	a2.PredEq = cs("godta_t", "a")
	a2.Output = cs("godta_t", "d")
	w.Queries = []model.Query{
		query(1, showplan.Statement{Accesses: []showplan.Access{a1}}),
		query(2, showplan.Statement{Accesses: []showplan.Access{a2}}),
	}
	cands := Generate(w)
	if len(cands) != 1 {
		t.Fatalf("%d candidates, want 1: %+v", len(cands), cands)
	}
	c := cands[0]
	if !slices.Equal(c.Include, []int{colC, colD}) {
		t.Errorf("include %v, want (c, d)", c.Include)
	}
	if !slices.Equal(c.Queries, []int64{1, 2}) {
		t.Errorf("Queries %v, want [1 2]", c.Queries)
	}
	if !slices.Contains(c.Rules, RuleMerged) {
		t.Errorf("Rules %v, want %q", c.Rules, RuleMerged)
	}
}

func TestColumnstoreTableSkipped(t *testing.T) {
	w := workload()
	a := scan("godta_cs")
	a.PredEq = cs("godta_cs", "v")
	a.Output = cs("godta_cs", "k")
	w.Queries = []model.Query{query(1, showplan.Statement{Accesses: []showplan.Access{a}})}
	w.MissingIndexes = []model.MissingIndexDMV{{Table: tblCS, Equality: []int{2}}}
	if cands := Generate(w); len(cands) != 0 {
		t.Errorf("%d candidates on a columnstore table, want none: %+v", len(cands), cands)
	}
	if !slices.Contains(w.Skipped, "dbo.godta_cs: "+RuleColumnstore) {
		t.Errorf("Skipped %v, want the columnstore rule for dbo.godta_cs", w.Skipped)
	}
}

func TestDeterministic(t *testing.T) {
	build := func(seed uint64) []byte {
		w := workload()
		var qs []model.Query
		for i, cols := range [][]string{{"a", "c"}, {"a", "d"}, {"b", "c"}, {"c", "a"}, {"d", "b"}} {
			a := scan("godta_t")
			a.PredEq = cs("godta_t", cols[0])
			a.Output = cs("godta_t", cols[1], "id")
			qs = append(qs, query(int64(10+i), showplan.Statement{Accesses: []showplan.Access{a},
				Missing: []showplan.MissingIndex{{Obj: a.Obj, Equality: []string{cols[0]}, Include: []string{"pad"}}}}))
		}
		w.MissingIndexes = []model.MissingIndexDMV{
			{Table: tblT, Equality: []int{colB, colA}, Include: []int{colPad}},
			{Table: tblT, Equality: []int{colA}, Inequality: []int{colC}},
			{Table: tblH, Equality: []int{2}, Include: []int{1}},
		}
		r := rand.New(rand.NewPCG(seed, seed))
		r.Shuffle(len(qs), func(i, j int) { qs[i], qs[j] = qs[j], qs[i] })
		r.Shuffle(len(w.MissingIndexes), func(i, j int) {
			w.MissingIndexes[i], w.MissingIndexes[j] = w.MissingIndexes[j], w.MissingIndexes[i]
		})
		w.Queries = qs
		out, err := json.Marshal(Generate(w))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first, second := build(1), build(7)
	if string(first) != string(second) {
		t.Errorf("outputs differ:\n%s\n%s", first, second)
	}
}

func loadPlan(t *testing.T, name string) *showplan.Plan {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "showplan", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p, err := showplan.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLookupOutputIncluded(t *testing.T) {
	w := workload()
	w.Queries = []model.Query{{QueryID: 1, Plan: loadPlan(t, "seek_lookup.sqlplan")}}
	cands := Generate(w)
	c := one(t, cands, tblH, keys(asc(1)))
	if !slices.Equal(c.Include, []int{2}) {
		t.Errorf("include %v, want (y), the output of the RID Lookup", c.Include)
	}
	if c.EqCount != 1 {
		t.Errorf("EqCount %d, want 1", c.EqCount)
	}
}

func TestScanPredicateBecomesKey(t *testing.T) {
	w := workload()
	w.Queries = []model.Query{{QueryID: 1, Plan: loadPlan(t, "sort.sqlplan")}}
	cands := Generate(w)
	c := one(t, cands, tblT, keys(asc(colA), desc(colB)))
	if !slices.Equal(c.Include, []int{colC}) {
		t.Errorf("include %v, want (c)", c.Include)
	}
	if c.EqCount != 1 {
		t.Errorf("EqCount %d, want 1", c.EqCount)
	}
	if c.Origin != OriginAlgorithm {
		t.Errorf("Origin %q, want %q", c.Origin, OriginAlgorithm)
	}
}

func TestViewAccessCountsForBase(t *testing.T) {
	w := workload()
	w.Queries = []model.Query{{QueryID: 7, Plan: loadPlan(t, "view.sqlplan")}}
	cands := Generate(w)
	for _, c := range cands {
		if c.Table == tblV {
			t.Errorf("candidate on the view godta_v: %+v", c)
		}
	}
	// The captured plan has no predicate, so it would give no candidate
	// anyway: the same read with a predicate on the view must not either.
	v := scan("godta_v")
	v.ViewBase = []showplan.ObjRef{{Database: dbName, Schema: "dbo", Table: "godta_t"}}
	v.PredEq = cs("godta_v", "a")
	v.Output = cs("godta_v", "n")
	wv := workload()
	direct := v // a NOEXPAND read names the view alone
	direct.ViewBase = nil
	wv.Queries = []model.Query{
		query(8, showplan.Statement{Accesses: []showplan.Access{v}}),
		query(9, showplan.Statement{Accesses: []showplan.Access{direct}}),
	}
	if got := Generate(wv); len(got) != 0 {
		t.Errorf("%d candidates for a predicate on the view, want none: %+v", len(got), got)
	}
	// The view's read counts for its base table: a candidate on godta_t
	// without a query of its own receives the query.
	bound := BindQueries([]model.Candidate{{Table: tblT, Keys: keys(asc(colA)), Origin: OriginAI}}, w)
	if !slices.Equal(bound[0].Queries, []int64{7}) {
		t.Errorf("Queries %v, want [7]: the plan reads godta_t through godta_v", bound[0].Queries)
	}
}

func TestBindQueries(t *testing.T) {
	w := workload()
	w.Queries = []model.Query{
		{QueryID: 1, Plan: loadPlan(t, "seek_lookup.sqlplan")},
		{QueryID: 2, Plan: loadPlan(t, "sort.sqlplan")},
	}
	in := []model.Candidate{
		{Table: tblH, Keys: keys(asc(2)), Origin: OriginAI},
		{Table: tblH, Keys: keys(asc(1)), Origin: OriginAlgorithm, Queries: []int64{99}},
	}
	out := BindQueries(in, w)
	if !slices.Equal(out[0].Queries, []int64{1}) {
		t.Errorf("Queries %v, want [1], the x = 42 query", out[0].Queries)
	}
	if !slices.Equal(out[1].Queries, []int64{99}) {
		t.Errorf("Queries %v, want [99]: a candidate with queries is left alone", out[1].Queries)
	}
	if len(in[0].Queries) != 0 {
		t.Errorf("input changed: %v", in[0].Queries)
	}
}

// TestBindQueriesIncludesPureWrites: INSERT ... VALUES writes godta_t without
// reading it (its plan reads only godta_v, for the view's maintenance). It is
// bound to a candidate on godta_t, which has no query, and to one that has
// queries, since the maintenance of every candidate of the table is measured
// from the writes of the table (spec section 8.6). A candidate on godta_h is
// not given it.
func TestBindQueriesIncludesPureWrites(t *testing.T) {
	w := workload()
	insert := loadPlan(t, "insert_narrow.sqlplan")
	for _, st := range insert.Statements {
		for _, a := range st.Accesses {
			if a.Obj.Table == "godta_t" || slices.ContainsFunc(a.ViewBase, func(o showplan.ObjRef) bool { return o.Table == "godta_t" }) {
				t.Fatalf("the fixture reads godta_t (%+v): it no longer tests a pure write", a.Obj)
			}
		}
	}
	w.Writes = []model.Query{{QueryID: 5, IsWrite: true, Plan: insert}}
	in := []model.Candidate{
		{Table: tblT, Keys: keys(asc(colC)), Origin: OriginAI},
		{Table: tblT, Keys: keys(asc(colB)), Origin: OriginAlgorithm, Queries: []int64{99}},
		{Table: tblH, Keys: keys(asc(1)), Origin: OriginAI},
	}
	out := BindQueries(in, w)
	if !slices.Equal(out[0].Queries, []int64{5}) {
		t.Errorf("Queries %v, want [5]: the INSERT writes godta_t", out[0].Queries)
	}
	if !slices.Equal(out[1].Queries, []int64{5, 99}) {
		t.Errorf("Queries %v, want [5 99]: a write of the table joins the candidate's own queries", out[1].Queries)
	}
	if len(out[2].Queries) != 0 {
		t.Errorf("Queries %v, want none: the INSERT does not write godta_h", out[2].Queries)
	}
	if !slices.Equal(in[1].Queries, []int64{99}) {
		t.Errorf("input changed: %v", in[1].Queries)
	}
}

func TestMissingIndexDMVBecomesCandidate(t *testing.T) {
	w := workload()
	w.MissingIndexes = []model.MissingIndexDMV{{Table: tblT, Equality: []int{colB}, Include: []int{colC, colID}}}
	cands := Generate(w)
	if len(cands) != 1 {
		t.Fatalf("%d candidates, want 1: %+v", len(cands), cands)
	}
	c := cands[0]
	want := model.Candidate{ID: c.ID, Table: tblT, Keys: keys(asc(colB)), EqCount: 1, Include: []int{colC},
		Origin: OriginMissingIndex, Rules: c.Rules}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("candidate %+v, want %+v", c, want)
	}
}

func TestEqOrderFromExisting(t *testing.T) {
	w := workload()
	tb := w.Tables[tblT]
	tb.Indexes = append(tb.Indexes, model.Index{ObjectID: tblT, IndexID: 2, Name: "ix_c_b", Kind: model.KindNonclustered,
		Keys: []model.KeyCol{{ColumnID: colC}, {ColumnID: colB}}})
	w.MissingIndexes = []model.MissingIndexDMV{
		{Table: tblT, Equality: []int{colB, colC}},
		{Table: tblT, Equality: []int{colA, colB}},
	}
	cands := Generate(w)
	c := one(t, cands, tblT, keys(asc(colC), asc(colB)))
	if !slices.Contains(c.Rules, RuleEqOrderFromExisting) {
		t.Errorf("Rules %v, want %q", c.Rules, RuleEqOrderFromExisting)
	}
	// No existing index leads with a or b: column_id order.
	d := one(t, cands, tblT, keys(asc(colA), asc(colB)))
	if slices.Contains(d.Rules, RuleEqOrderFromExisting) {
		t.Errorf("Rules %v, want no %q", d.Rules, RuleEqOrderFromExisting)
	}
}
