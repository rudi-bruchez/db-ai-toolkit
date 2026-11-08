package showplan

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"unicode/utf16"
)

// The fixtures come from testdata/capture/main.go, run on the lab instance.
// join_merge, join_loop_pred, group_stream and group_hash were captured by
// hand under SET SHOWPLAN_XML ON on SQL Server 2022 (a throwaway database
// godta_fix_joins with two tables ja and jb), with MERGE JOIN, LOOP JOIN,
// ORDER GROUP and HASH GROUP hints; merge and stream aggregate read ordered
// indexes, so no Sort gives their columns another way.

func load(t *testing.T, name string) *Plan {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return p
}

// planned returns the one statement of p that has a query plan.
func planned(t *testing.T, p *Plan) Statement {
	t.Helper()
	var found []Statement
	for _, s := range p.Statements {
		if s.Root != nil {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d statements with a plan, want 1", len(found))
	}
	return found[0]
}

func columns(refs []ColRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Column)
	}
	return out
}

func indexes(objs []ObjRef) []string {
	var out []string
	for _, o := range objs {
		out = append(out, o.Index)
	}
	return out
}

func accessByOp(t *testing.T, s Statement, op string) (int, Access) {
	t.Helper()
	for i, a := range s.Accesses {
		if a.Op == op {
			return i, a
		}
	}
	t.Fatalf("no %s access in %+v", op, s.Accesses)
	return -1, Access{}
}

func TestParseSeekLookup(t *testing.T) {
	s := planned(t, load(t, "seek_lookup.sqlplan"))
	si, seek := accessByOp(t, s, "Index Seek")
	if seek.Obj.Table != "godta_h" || seek.Obj.Index != "nix_h_x" || seek.Obj.Schema != "dbo" {
		t.Errorf("seek object %+v, want dbo.godta_h.nix_h_x", seek.Obj)
	}
	if got := columns(seek.SeekCols); !slices.Equal(got, []string{"x"}) {
		t.Errorf("SeekCols %v, want [x]", got)
	}
	if !slices.Equal(seek.SeekEq, []bool{true}) {
		t.Errorf("SeekEq %v, want [true]", seek.SeekEq)
	}
	if seek.LookupOf != -1 {
		t.Errorf("seek LookupOf %d, want -1", seek.LookupOf)
	}
	_, lookup := accessByOp(t, s, "RID Lookup")
	if lookup.LookupOf != si {
		t.Errorf("RID Lookup LookupOf %d, want %d (the seek)", lookup.LookupOf, si)
	}
	if len(lookup.SeekCols) != 0 {
		t.Errorf("RID Lookup SeekCols %v, want none: Bmk1000 is no column of the table", columns(lookup.SeekCols))
	}
	if !slices.Contains(columns(lookup.Output), "y") {
		t.Errorf("RID Lookup Output %v, want y", columns(lookup.Output))
	}
}

func TestParseScanPredicate(t *testing.T) {
	s := planned(t, load(t, "sort.sqlplan"))
	_, scan := accessByOp(t, s, "Clustered Index Scan")
	if got := columns(scan.PredEq); !slices.Equal(got, []string{"a"}) {
		t.Errorf("PredEq %v, want [a]", got)
	}
	if len(scan.PredRange) != 0 {
		t.Errorf("PredRange %v, want none", columns(scan.PredRange))
	}
	if len(scan.SeekCols) != 0 {
		t.Errorf("SeekCols %v, want none on a scan", columns(scan.SeekCols))
	}
}

func TestParseParams(t *testing.T) {
	s := planned(t, load(t, "scan_param.sqlplan"))
	want := []Param{{"@b", "int", "(9332)"}, {"@a", "int", "(933)"}}
	if !reflect.DeepEqual(s.Params, want) {
		t.Errorf("Params %v, want %v", s.Params, want)
	}
}

func TestParseTVFParamsIgnored(t *testing.T) {
	const frag = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.564"><BatchSequence><Batch><Statements>` +
		`<StmtSimple StatementText="SELECT * FROM dbo.f(@x)" StatementType="SELECT" StatementSubTreeCost="0.01"><QueryPlan>` +
		`<RelOp NodeId="0" PhysicalOp="Table-valued function" LogicalOp="Table-valued function" EstimateRows="1" EstimatedTotalSubtreeCost="0.01">` +
		`<OutputList/><TableValuedFunction><DefinedValues/><Object Database="[d]" Schema="[dbo]" Table="[f]"/>` +
		`<ParameterList><ColumnReference Column="@x" ParameterDataType="int"/></ParameterList>` +
		`</TableValuedFunction></RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`
	p, err := Parse([]byte(frag))
	if err != nil {
		t.Fatal(err)
	}
	s := planned(t, p)
	if len(s.Params) != 0 {
		t.Errorf("Params %v, want none: the ParameterList belongs to the function", s.Params)
	}
}

func TestParseViewBase(t *testing.T) {
	s := planned(t, load(t, "view.sqlplan"))
	if len(s.Accesses) != 1 {
		t.Fatalf("%d accesses, want 1", len(s.Accesses))
	}
	a := s.Accesses[0]
	if a.Obj.Index != "godta_v_cx" || a.Obj.IndexKind != "ViewClustered" || a.Obj.Table != "godta_v" {
		t.Errorf("Obj %+v, want godta_v.godta_v_cx ViewClustered", a.Obj)
	}
	var base []string
	for _, o := range a.ViewBase {
		base = append(base, o.Table)
	}
	if !slices.Contains(base, "godta_t") {
		t.Errorf("ViewBase %v, want godta_t", base)
	}
}

func writesOn(s Statement, table string) []Write {
	var out []Write
	for _, w := range s.Writes {
		for _, o := range w.Objs {
			if o.Table == table {
				out = append(out, w)
				break
			}
		}
	}
	return out
}

func TestParseNarrowWrites(t *testing.T) {
	for _, c := range []struct{ file, op string }{
		{"update_narrow.sqlplan", "Clustered Index Update"},
		{"insert_narrow.sqlplan", "Clustered Index Insert"},
	} {
		s := planned(t, load(t, c.file))
		ws := writesOn(s, "godta_t")
		if len(ws) != 1 {
			t.Errorf("%s: %d writes on godta_t, want 1", c.file, len(ws))
			continue
		}
		if ws[0].Op != c.op {
			t.Errorf("%s: write %q, want %q", c.file, ws[0].Op, c.op)
		}
		idx := indexes(ws[0].Objs)
		if !slices.Contains(idx, "pk_godta_t") || !slices.Contains(idx, "godta_hyp_c") {
			t.Errorf("%s: write objects %v, want pk_godta_t and godta_hyp_c", c.file, idx)
		}
		if c.op == "Clustered Index Update" && !slices.Equal(columns(ws[0].SetCols), []string{"c"}) {
			t.Errorf("%s: SetCols %v, want [c]", c.file, columns(ws[0].SetCols))
		}
	}
	s := planned(t, load(t, "insert_narrow.sqlplan"))
	vs := writesOn(s, "godta_v")
	if len(vs) != 1 {
		t.Fatalf("insert: %d writes on godta_v, want 1 (indexed view maintenance)", len(vs))
	}
	idx := indexes(vs[0].Objs)
	if !slices.Contains(idx, "godta_v_cx") || slices.Contains(idx, "godta_hyp_c") {
		t.Errorf("insert: view write objects %v, want godta_v_cx without godta_hyp_c", idx)
	}
}

func TestParseQueryStorePlan(t *testing.T) {
	s := planned(t, load(t, "qs_plan.sqlplan"))
	if len(s.Accesses) == 0 {
		t.Error("no access in the Query Store plan")
	}
	want := []Param{{"@b", "int", "(9332)"}, {"@a", "int", "(933)"}}
	if !reflect.DeepEqual(s.Params, want) {
		t.Errorf("Params %v, want %v", s.Params, want)
	}
}

func TestParseMissingIndex(t *testing.T) {
	s := planned(t, load(t, "scan_param.sqlplan"))
	if len(s.Missing) != 1 {
		t.Fatalf("%d missing indexes, want 1", len(s.Missing))
	}
	m := s.Missing[0]
	if m.Obj.Table != "godta_t" || m.Obj.Schema != "dbo" {
		t.Errorf("missing index object %+v, want dbo.godta_t", m.Obj)
	}
	if !slices.Equal(m.Equality, []string{"a", "b"}) {
		t.Errorf("Equality %v, want [a b]", m.Equality)
	}
	if !slices.Equal(m.Include, []string{"c"}) || len(m.Inequality) != 0 {
		t.Errorf("Include %v Inequality %v, want [c] and none", m.Include, m.Inequality)
	}
	if m.Impact < 99 || m.Impact > 100 {
		t.Errorf("Impact %v, want 99.8474", m.Impact)
	}
}

func TestParseSort(t *testing.T) {
	s := planned(t, load(t, "sort.sqlplan"))
	if len(s.Sorts) != 1 {
		t.Fatalf("%d sorts, want 1 (the Gather Streams order is not a sort)", len(s.Sorts))
	}
	if got := columns(s.Sorts[0].Cols); !slices.Equal(got, []string{"b"}) {
		t.Errorf("sort columns %v, want [b]", got)
	}
	if !slices.Equal(s.Sorts[0].Desc, []bool{true}) {
		t.Errorf("Desc %v, want [true]", s.Sorts[0].Desc)
	}
}

func TestParseWriteOwnCost(t *testing.T) {
	s := planned(t, load(t, "update.sqlplan"))
	var found bool
	for _, w := range s.Writes {
		if w.Op != "Index Update" || !slices.Contains(indexes(w.Objs), "godta_hyp_c") {
			continue
		}
		found = true
		if w.OwnCost <= 0 || w.OwnCost >= s.Cost {
			t.Errorf("OwnCost %v, want in (0, %v)", w.OwnCost, s.Cost)
		}
		// The wide operator assigns the index row's own columns (id1020), no table column
		if len(w.SetCols) != 0 {
			t.Errorf("SetCols %v, want none", columns(w.SetCols))
		}
		// 4.3436 for the operator, 4.29674 for its only child, the Sort
		if math.Abs(w.OwnCost-(4.3436-4.29674)) > 1e-9 {
			t.Errorf("OwnCost %v, want %v", w.OwnCost, 4.3436-4.29674)
		}
	}
	if !found {
		t.Errorf("no Index Update on godta_hyp_c in %+v", s.Writes)
	}
}

func TestShapeStable(t *testing.T) {
	a := planned(t, load(t, "seek_lookup.sqlplan"))
	b := planned(t, load(t, "seek_lookup.sqlplan"))
	if Shape(a) != Shape(b) {
		t.Errorf("two reads of one file differ:\n%s\n%s", Shape(a), Shape(b))
	}
	const want = "Nested Loops|Index Seek(dbo.godta_h.nix_h_x)|RID Lookup(dbo.godta_h.)"
	if Shape(a) != want {
		t.Errorf("Shape %q, want %q", Shape(a), want)
	}
	j := planned(t, load(t, "join.sqlplan"))
	if Shape(a) == Shape(j) {
		t.Errorf("seek_lookup and join have the same shape %q", Shape(a))
	}
	if !SameCost(a.Cost, b.Cost) || SameCost(a.Cost, j.Cost) {
		t.Errorf("SameCost: %v %v %v", a.Cost, b.Cost, j.Cost)
	}
	if !SameCost(1, 1+1e-7) || SameCost(1, 1+1e-5) {
		t.Error("SameCost does not use a relative gap of 1e-6")
	}
}

func TestParseUTF16(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "seek_lookup.sqlplan"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// The way SSMS saves a .sqlplan: BOM, then a declaration naming utf-16
	text := `<?xml version="1.0" encoding="utf-16"?>` + string(data)
	enc := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(text)) {
		enc = append(enc, byte(u), byte(u>>8))
	}
	got, err := Parse(enc)
	if err != nil {
		t.Fatalf("parsing UTF-16: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UTF-16 read differs from UTF-8 read:\n%+v\n%+v", got, want)
	}
}

// joinGroup returns the Table.Column names of the join and grouping columns
// of the one planned statement of a fixture.
func joinGroup(t *testing.T, name string) (join, group []string) {
	t.Helper()
	s := planned(t, load(t, name))
	for _, c := range s.JoinCols {
		join = append(join, c.Table+"."+c.Column)
	}
	for _, c := range s.GroupCols {
		group = append(group, c.Table+"."+c.Column)
	}
	return join, group
}

// TestJoinColumns: the columns each join operator compares, once each, and
// only those of a table.
func TestJoinColumns(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"join.sqlplan", []string{"godta_h.x", "godta_t.a"}}, // Hash Match: HashKeysBuild, HashKeysProbe
		{"join_merge.sqlplan", []string{"jb.k", "ja.k"}},     // Merge Join: inner, outer side, residual
		{"join_loop_pred.sqlplan", []string{"ja.v", "jb.w"}}, // Nested Loops: Predicate
		{"insert_narrow.sqlplan", []string{"godta_t.a"}},     // Nested Loops: OuterReferences
		{"seek_lookup.sqlplan", nil},                         // OuterReferences of a bookmark only
		{"group_hash.sqlplan", nil},                          // a Hash Match aggregate is no join
	} {
		join, _ := joinGroup(t, tc.file)
		if !slices.Equal(join, tc.want) {
			t.Errorf("%s: JoinCols %v, want %v", tc.file, join, tc.want)
		}
	}
}

// TestGroupColumns: the grouping columns of a Stream Aggregate (GroupBy) and
// of a Hash Match aggregate (HashKeysBuild), and none for a hash join.
func TestGroupColumns(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"group_stream.sqlplan", []string{"ja.g"}},
		{"group_hash.sqlplan", []string{"ja.g"}},
		{"join.sqlplan", nil},
	} {
		_, group := joinGroup(t, tc.file)
		if !slices.Equal(group, tc.want) {
			t.Errorf("%s: GroupCols %v, want %v", tc.file, group, tc.want)
		}
	}
}
