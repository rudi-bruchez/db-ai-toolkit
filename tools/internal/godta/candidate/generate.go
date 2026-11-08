// Package candidate turns a workload into index candidates (spec section
// 8.3): the generation from the plans and the missing index groups here, the
// coverage model and the confrontation with the existing indexes in
// coverage.go.
package candidate

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/sqlferret"
)

// Origins of a candidate.
const (
	OriginAlgorithm    = "algorithm"
	OriginMissingIndex = "missing_index"
	OriginSqlferret    = "sqlferret"
	OriginAI           = "ai"
)

// Rule names, written in Candidate.Rules and shown as is in the report.
const (
	RuleEqFirst               = "eq-first"
	RuleOneRangeOrSort        = "one-range-or-sort"
	RuleSortAfterRangeDropped = "sort-after-range-dropped"
	RuleIncludeOutputs        = "include-outputs"
	RuleClusteredKeyImplicit  = "clustered-key-implicit"
	RuleEqOrderFromExisting   = "eq-order-from-existing"
	RuleMerged                = "merged"
	// RuleColumnstore is written in Workload.Skipped for a table that has a
	// columnstore index: it produces no candidate.
	RuleColumnstore = "columnstore table: not evaluated"
)

// ruleOrder is the order rules are listed in, whatever the order they were
// applied in, so that the output does not depend on the input order.
var ruleOrder = []string{RuleEqFirst, RuleEqOrderFromExisting, RuleOneRangeOrSort, RuleSortAfterRangeDropped,
	RuleIncludeOutputs, RuleClusteredKeyImplicit, RuleMerged}

var originOrder = []string{OriginAlgorithm, OriginMissingIndex, OriginSqlferret, OriginAI}

// resolver maps the names of a plan to object_ids and column_ids, by case
// only when the database collation is case sensitive.
type resolver struct {
	w  *model.Workload
	cs bool
}

func newResolver(w *model.Workload) resolver {
	c := w.Database.Collation
	return resolver{w: w, cs: strings.Contains(c, "_CS") || strings.Contains(c, "_BIN")}
}

func (r resolver) same(a, b string) bool {
	return a == b || (!r.cs && strings.EqualFold(a, b))
}

// table returns the table an object names, nil for another database, a
// temporary table or an object collect did not read.
func (r resolver) table(o showplan.ObjRef) *model.Table {
	if o.Table == "" || (o.Database != "" && !r.same(o.Database, r.w.Database.Name)) {
		return nil
	}
	ids := make([]int, 0, len(r.w.Tables))
	for id := range r.w.Tables {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		t := r.w.Tables[id]
		if r.same(t.Schema, o.Schema) && r.same(t.Name, o.Table) {
			return t
		}
	}
	return nil
}

// column returns the column_id of c in t when c belongs to the read of obj
// (same table, same alias), 0 otherwise.
func (r resolver) column(t *model.Table, obj showplan.ObjRef, c showplan.ColRef) int {
	if c.Table == "" || !r.same(c.Table, obj.Table) || !r.same(c.Schema, obj.Schema) || c.Alias != obj.Alias {
		return 0
	}
	return r.columnByName(t, c.Column)
}

func (r resolver) columnByName(t *model.Table, name string) int {
	for _, col := range t.Columns {
		if r.same(col.Name, name) {
			return col.ID
		}
	}
	return 0
}

func isView(t *model.Table) bool {
	for _, ix := range t.Indexes {
		if ix.Kind == model.KindViewClustered {
			return true
		}
	}
	return false
}

// need is what one read asks of a table, by column_id.
type need struct {
	eq, rng []int
	sort    []model.KeyCol
	out     []int
}

func appendNew(list []int, ids ...int) []int {
	for _, id := range ids {
		if id != 0 && !slices.Contains(list, id) {
			list = append(list, id)
		}
	}
	return list
}

// Generate builds the candidates of a workload: one per read of a table in
// the plans of the queries, one per missing index group of the plans and of
// the DMV, candidates of the same keys merged. A table with a columnstore
// index produces none and is listed in w.Skipped. The result is sorted by
// (Table, Keys, Include) and does not depend on the order of the input.
func Generate(w *model.Workload) []model.Candidate {
	g := newGenerator(w)
	for _, q := range w.Queries {
		g.plan(q.Plan, OriginAlgorithm, OriginMissingIndex, []int64{q.QueryID})
	}
	for _, m := range w.MissingIndexes {
		t := w.Tables[m.Table]
		if t == nil {
			continue
		}
		n := need{eq: appendNew(nil, m.Equality...), out: appendNew(nil, m.Include...)}
		for _, id := range m.Inequality {
			if !slices.Contains(n.eq, id) {
				n.rng = appendNew(n.rng, id)
			}
		}
		g.add(t, n, OriginMissingIndex, nil)
	}
	return g.finish()
}

// FromSources builds the candidates of the sqlferret plans of the database
// of w (spec section 8.2), by the rules of Generate, with Origin
// OriginSqlferret for the reads and the missing index groups alike. A source
// of another database, or whose database is unknown, is ignored. The
// candidates have no query of their own: BindQueries gives them the queries
// of the workload that read their table, which is how their gain is
// measured.
func FromSources(srcs []sqlferret.Source, w *model.Workload) []model.Candidate {
	g := newGenerator(w)
	for _, s := range srcs {
		if s.Database == "" || !g.r.same(s.Database, w.Database.Name) {
			continue
		}
		g.plan(s.Plan, OriginSqlferret, OriginSqlferret, nil)
	}
	return BindQueries(g.finish(), w)
}

// generator gathers the raw candidates of Generate and FromSources.
type generator struct {
	w       *model.Workload
	r       resolver
	raw     []model.Candidate
	skipped map[int]bool
}

func newGenerator(w *model.Workload) *generator {
	return &generator{w: w, r: newResolver(w), skipped: map[int]bool{}}
}

func (g *generator) add(t *model.Table, n need, origin string, queries []int64) {
	if t.HasColumnstore {
		g.skipped[t.ObjectID] = true
		return
	}
	if isView(t) {
		return
	}
	if c, ok := build(t, n); ok {
		c.Origin = origin
		c.Queries = queries
		g.raw = append(g.raw, c)
	}
}

// plan adds the candidates of the reads and of the missing index groups of
// one plan.
func (g *generator) plan(p *showplan.Plan, readOrigin, missingOrigin string, queries []int64) {
	if p == nil {
		return
	}
	for _, st := range p.Statements {
		if st.Root == nil {
			continue
		}
		for _, rd := range reads(g.r, st) {
			g.add(rd.t, rd.n, readOrigin, queries)
		}
		for _, m := range st.Missing {
			t := g.r.table(m.Obj)
			if t == nil {
				continue
			}
			n := need{}
			for _, c := range m.Equality {
				n.eq = appendNew(n.eq, g.r.columnByName(t, c))
			}
			for _, c := range m.Inequality {
				n.rng = appendNew(n.rng, g.r.columnByName(t, c))
			}
			for _, c := range m.Include {
				n.out = appendNew(n.out, g.r.columnByName(t, c))
			}
			g.add(t, n, missingOrigin, queries)
		}
	}
}

// finish lists the columnstore tables in w.Skipped and merges the
// candidates.
func (g *generator) finish() []model.Candidate {
	ids := make([]int, 0, len(g.skipped))
	for id := range g.skipped {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		t := g.w.Tables[id]
		msg := t.Schema + "." + t.Name + ": " + RuleColumnstore
		if !slices.Contains(g.w.Skipped, msg) {
			g.w.Skipped = append(g.w.Skipped, msg)
		}
	}
	return merge(g.raw)
}

// TableByName returns the table schema.name that collect read, the names
// compared as the database collation compares them, nil when there is none.
func TableByName(w *model.Workload, schema, name string) *model.Table {
	return newResolver(w).table(showplan.ObjRef{Schema: schema, Table: name})
}

// ColumnByName returns the column_id of the column name of t, compared as
// the database collation compares names, 0 when t has none.
func ColumnByName(w *model.Workload, t *model.Table, name string) int {
	return newResolver(w).columnByName(t, name)
}

type read struct {
	t *model.Table
	n need
}

// reads lists what each read of a statement asks of its table. A lookup is
// merged with the read it completes. A read of an indexed view gives no
// candidate (Generate skips the view, whose columns are not its base
// table's); it counts for the base tables in BindQueries.
func reads(r resolver, st showplan.Statement) []read {
	var out []read
	for i, a := range st.Accesses {
		if a.LookupOf >= 0 {
			continue
		}
		t := r.table(a.Obj)
		if t == nil {
			continue
		}
		n := need{}
		group := []showplan.Access{a}
		for _, l := range st.Accesses {
			if l.LookupOf == i {
				group = append(group, l)
			}
		}
		for _, g := range group {
			for k, c := range g.SeekCols {
				id := r.column(t, a.Obj, c)
				if k < len(g.SeekEq) && g.SeekEq[k] {
					n.eq = appendNew(n.eq, id)
				} else {
					n.rng = appendNew(n.rng, id)
				}
			}
			for _, c := range g.PredEq {
				n.eq = appendNew(n.eq, r.column(t, a.Obj, c))
			}
			for _, c := range g.PredRange {
				n.rng = appendNew(n.rng, r.column(t, a.Obj, c))
			}
			for _, c := range g.Output {
				n.out = appendNew(n.out, r.column(t, a.Obj, c))
			}
		}
		n.rng = slices.DeleteFunc(n.rng, func(id int) bool { return slices.Contains(n.eq, id) })
		n.sort = sortOf(r, st, t, a.Obj)
		out = append(out, read{t, n})
	}
	return out
}

// sortOf returns the first sort of the statement whose columns all belong to
// the read of obj, nil if there is none.
func sortOf(r resolver, st showplan.Statement, t *model.Table, obj showplan.ObjRef) []model.KeyCol {
	for _, s := range st.Sorts {
		var kc []model.KeyCol
		for i, c := range s.Cols {
			id := r.column(t, obj, c)
			if id == 0 {
				kc = nil
				break
			}
			kc = append(kc, model.KeyCol{ColumnID: id, Desc: i < len(s.Desc) && s.Desc[i]})
		}
		if len(kc) > 0 {
			return kc
		}
	}
	return nil
}

func computed(t *model.Table, id int) bool {
	for _, c := range t.Columns {
		if c.ID == id {
			return c.Computed
		}
	}
	return false
}

func clusteredKey(t *model.Table) []int {
	for _, ix := range t.Indexes {
		if ix.Kind == model.KindClustered {
			var out []int
			for _, k := range ix.Keys {
				out = append(out, k.ColumnID)
			}
			return out
		}
	}
	return nil
}

// orderEq orders the equality columns as the closest existing index has
// them: the real, enabled, unfiltered rowstore index whose leading keys hold
// the most of them (lowest index_id on a tie) gives the order of those, the
// others follow by column_id. Without such an index, column_id order.
func orderEq(t *model.Table, eq []int) ([]int, bool) {
	rest := slices.Clone(eq)
	slices.Sort(rest)
	if len(eq) < 2 {
		return rest, false
	}
	var best []int
	for _, ix := range t.Indexes {
		if ix.Disabled || ix.Hypothetical || ix.Filter != "" ||
			(ix.Kind != model.KindClustered && ix.Kind != model.KindNonclustered) {
			continue
		}
		var lead []int
		for _, k := range ix.Keys {
			if !slices.Contains(eq, k.ColumnID) {
				break
			}
			lead = append(lead, k.ColumnID)
		}
		if len(lead) > len(best) {
			best = lead
		}
	}
	if len(best) == 0 {
		return rest, false
	}
	rest = slices.DeleteFunc(rest, func(id int) bool { return slices.Contains(best, id) })
	return append(best, rest...), true
}

// build applies the rules of spec section 8.3 to what one read asks.
func build(t *model.Table, n need) (model.Candidate, bool) {
	var c model.Candidate
	rules := map[string]bool{}
	keyable := func(id int) bool { return !computed(t, id) }

	eq, fromExisting := orderEq(t, slices.DeleteFunc(slices.Clone(n.eq), func(id int) bool { return !keyable(id) }))
	for _, id := range eq {
		c.Keys = append(c.Keys, model.KeyCol{ColumnID: id})
	}
	c.EqCount = len(eq)
	if len(eq) > 0 {
		rules[RuleEqFirst] = true
	}
	if fromExisting {
		rules[RuleEqOrderFromExisting] = true
	}

	rng := slices.DeleteFunc(slices.Clone(n.rng), func(id int) bool { return !keyable(id) })
	var sort []model.KeyCol
	for _, k := range n.sort {
		if !slices.Contains(n.eq, k.ColumnID) { // a column held equal is already in order
			sort = append(sort, k)
		}
	}
	if slices.ContainsFunc(sort, func(k model.KeyCol) bool { return !keyable(k.ColumnID) }) {
		sort = nil
	}
	var extra []int // predicate and sort columns left out of the keys
	switch {
	case len(sort) > 0 && (len(rng) == 0 || slices.Contains(rng, sort[0].ColumnID)):
		c.Keys = append(c.Keys, sort...)
		rules[RuleOneRangeOrSort] = true
		for _, id := range rng {
			if id != sort[0].ColumnID {
				extra = append(extra, id)
			}
		}
	case len(rng) > 0:
		c.Keys = append(c.Keys, model.KeyCol{ColumnID: rng[0]})
		rules[RuleOneRangeOrSort] = true
		extra = append(extra, rng[1:]...)
		if len(sort) > 0 {
			rules[RuleSortAfterRangeDropped] = true
			for _, k := range sort {
				extra = append(extra, k.ColumnID)
			}
		}
	}
	if len(c.Keys) == 0 {
		return c, false
	}

	inKeys := func(id int) bool {
		return slices.ContainsFunc(c.Keys, func(k model.KeyCol) bool { return k.ColumnID == id })
	}
	ck := clusteredKey(t)
	for _, id := range appendNew(slices.Clone(n.out), extra...) {
		switch {
		case inKeys(id):
		case slices.Contains(ck, id):
			rules[RuleClusteredKeyImplicit] = true
		default:
			c.Include = appendNew(c.Include, id)
		}
	}
	slices.Sort(c.Include)
	if len(c.Include) > 0 {
		rules[RuleIncludeOutputs] = true
	}
	c.Table = t.ObjectID
	c.Rules = orderedRules(rules)
	return c, true
}

func orderedRules(set map[string]bool) []string {
	var out []string
	for _, r := range ruleOrder {
		if set[r] {
			out = append(out, r)
		}
	}
	return out
}

func compareKeys(a, b []model.KeyCol) int {
	return slices.CompareFunc(a, b, func(x, y model.KeyCol) int {
		if c := cmp.Compare(x.ColumnID, y.ColumnID); c != 0 {
			return c
		}
		switch {
		case x.Desc == y.Desc:
			return 0
		case !x.Desc:
			return -1
		}
		return 1
	})
}

// merge joins the candidates of the same table and keys: includes, queries
// and rules united, the smallest EqCount, the first origin in originOrder.
func merge(raw []model.Candidate) []model.Candidate {
	var out []model.Candidate
	count := []int{}
	for _, c := range raw {
		i := slices.IndexFunc(out, func(o model.Candidate) bool {
			return o.Table == c.Table && compareKeys(o.Keys, c.Keys) == 0
		})
		if i < 0 {
			c.Include = slices.Clone(c.Include)
			c.Queries = slices.Clone(c.Queries)
			out = append(out, c)
			count = append(count, 1)
			continue
		}
		o := &out[i]
		count[i]++
		o.Include = appendNew(o.Include, c.Include...)
		o.Queries = append(o.Queries, c.Queries...)
		o.EqCount = min(o.EqCount, c.EqCount)
		if slices.Index(originOrder, c.Origin) < slices.Index(originOrder, o.Origin) {
			o.Origin = c.Origin
		}
		o.Rules = append(o.Rules, c.Rules...)
	}
	for i := range out {
		c := &out[i]
		set := map[string]bool{}
		for _, r := range c.Rules {
			set[r] = true
		}
		if count[i] > 1 {
			set[RuleMerged] = true
		}
		c.Rules = orderedRules(set)
		slices.Sort(c.Include)
		slices.Sort(c.Queries)
		c.Queries = slices.Compact(c.Queries)
		c.ID = ID(*c)
	}
	slices.SortFunc(out, compareCandidates)
	return out
}

func compareCandidates(a, b model.Candidate) int {
	if c := cmp.Compare(a.Table, b.Table); c != 0 {
		return c
	}
	if c := compareKeys(a.Keys, b.Keys); c != 0 {
		return c
	}
	return slices.Compare(a.Include, b.Include)
}

// ID is the stable identifier of a candidate: a short hash of its table,
// keys and includes (the includes in column_id order).
func ID(c model.Candidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|", c.Table)
	for _, k := range c.Keys {
		fmt.Fprintf(&b, "%d", k.ColumnID)
		if k.Desc {
			b.WriteString("d")
		}
		b.WriteString(",")
	}
	inc := slices.Clone(c.Include)
	slices.Sort(inc)
	fmt.Fprintf(&b, "|%v", inc)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:6])
}

// BindQueries gives each candidate that has no query (those of the AI and of
// sqlferret, or a missing index group of the DMV) every query, read or
// write, whose plan reads its table, directly or through an indexed view.
// Every candidate, with queries or without, also receives every write whose
// plan writes its table: its maintenance is measured on the writes of the
// table (spec section 8.6), and a write such as INSERT ... VALUES writes the
// table without reading it. The input is not changed.
func BindQueries(cands []model.Candidate, w *model.Workload) []model.Candidate {
	r := newResolver(w)
	readers := map[int][]int64{}
	writers := map[int][]int64{}
	for _, list := range [][]model.Query{w.Queries, w.Writes} {
		for _, q := range list {
			if q.Plan == nil {
				continue
			}
			for _, st := range q.Plan.Statements {
				for _, a := range st.Accesses {
					for _, o := range append([]showplan.ObjRef{a.Obj}, a.ViewBase...) {
						if t := r.table(o); t != nil {
							readers[t.ObjectID] = append(readers[t.ObjectID], q.QueryID)
						}
					}
				}
				for _, wr := range st.Writes {
					for _, o := range wr.Objs {
						if t := r.table(o); t != nil {
							writers[t.ObjectID] = append(writers[t.ObjectID], q.QueryID)
						}
					}
				}
			}
		}
	}
	out := slices.Clone(cands)
	for i := range out {
		qs := slices.Clone(out[i].Queries)
		if len(qs) == 0 {
			qs = append(qs, readers[out[i].Table]...)
		}
		qs = append(qs, writers[out[i].Table]...)
		slices.Sort(qs)
		out[i].Queries = slices.Compact(qs)
	}
	return out
}
