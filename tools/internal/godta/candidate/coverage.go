package candidate

import (
	"fmt"
	"slices"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
)

// The coverage model and the confrontation with the existing indexes (spec
// section 8.3). Columns are compared by column_id, never by name.

// clusteredKeyCols returns the keys of the clustered index of t, nil for a
// heap.
func clusteredKeyCols(t *model.Table) []model.KeyCol {
	for _, ix := range t.Indexes {
		if ix.Kind == model.KindClustered || ix.Kind == model.KindViewClustered {
			return ix.Keys
		}
	}
	return nil
}

func isClustered(ix model.Index) bool {
	return ix.Kind == model.KindClustered || ix.Kind == model.KindViewClustered
}

func hasKey(keys []model.KeyCol, id int) bool {
	return slices.ContainsFunc(keys, func(k model.KeyCol) bool { return k.ColumnID == id })
}

// EffectiveKeys returns the keys an index is sorted on: for a non unique
// nonclustered index on a clustered table, its declared keys followed by the
// columns of the clustered key it does not already hold; for a unique
// nonclustered index, a clustered index and any index of a heap, its declared
// keys.
func EffectiveKeys(ix model.Index, t *model.Table) []model.KeyCol {
	keys := slices.Clone(ix.Keys)
	if isClustered(ix) || ix.Unique {
		return keys
	}
	for _, k := range clusteredKeyCols(t) {
		if !hasKey(keys, k.ColumnID) {
			keys = append(keys, k)
		}
	}
	return keys
}

// LeafCols returns the columns present at the leaf level of an index: every
// column of the table for a clustered index; otherwise its effective keys,
// its included columns and the clustered key.
func LeafCols(ix model.Index, t *model.Table) map[int]bool {
	out := map[int]bool{}
	if isClustered(ix) {
		for _, c := range t.Columns {
			out[c.ID] = true
		}
		return out
	}
	for _, k := range EffectiveKeys(ix, t) {
		out[k.ColumnID] = true
	}
	for _, id := range ix.Include {
		out[id] = true
	}
	for _, k := range clusteredKeyCols(t) {
		out[k.ColumnID] = true
	}
	return out
}

// usable is condition 1, restricted to the rowstore indexes that have keys.
func usable(e model.Index) bool {
	return !e.Disabled && !e.Hypothetical &&
		(e.Kind == model.KindClustered || e.Kind == model.KindNonclustered)
}

// keysMatch is condition 3: the equality columns of c, in any order and
// direction, lead the effective keys of e, followed in order by the other
// declared keys of c, with the same direction on all of them or the reverse
// on all of them. The clustered key that EffectiveKeys appends to c is not a
// demand of its queries (a column a query sorts on is declared): it enters
// neither the order nor the direction rule.
func keysMatch(e model.Index, c model.Candidate, t *model.Table) bool {
	ek := EffectiveKeys(e, t)
	ck := c.Keys
	if len(ck) == 0 || len(ek) < len(ck) {
		return false
	}
	eq := min(max(c.EqCount, 0), len(ck))
	for _, k := range ck[:eq] {
		if !hasKey(ek[:eq], k.ColumnID) {
			return false
		}
	}
	inverted := -1
	for i := eq; i < len(ck); i++ {
		if ek[i].ColumnID != ck[i].ColumnID {
			return false
		}
		inv := 0
		if ek[i].Desc != ck[i].Desc {
			inv = 1
		}
		if inverted >= 0 && inv != inverted {
			return false
		}
		inverted = inv
	}
	return true
}

// includesMatch is condition 4.
func includesMatch(e model.Index, c model.Candidate, t *model.Table) bool {
	leaf := LeafCols(e, t)
	for _, id := range c.Include {
		if !leaf[id] {
			return false
		}
	}
	return true
}

// Covers says whether the existing index e covers the candidate c on its
// table t: the four conditions of spec section 8.3. The first c.EqCount keys
// of c are its equality columns. A filtered index never covers: goDTA does
// not try to prove that the filter is implied by the predicates.
func Covers(e model.Index, c model.Candidate, t *model.Table) bool {
	return usable(e) && e.Filter == "" && keysMatch(e, c, t) && includesMatch(e, c, t)
}

// candidateIndex is c as the non unique nonclustered index it would be.
func candidateIndex(c model.Candidate) model.Index {
	return model.Index{ObjectID: c.Table, Name: c.ID, Kind: model.KindNonclustered, Keys: c.Keys, Include: c.Include}
}

func isStrictPrefix(p, of []model.KeyCol) bool {
	return len(p) < len(of) && slices.Equal(p, of[:len(p)])
}

func constraint(e model.Index) bool { return e.PrimaryKey || e.UniqueConstraint }

// replaceable says whether e may be dropped for a candidate whose declared
// keys extend its own.
func replaceable(e model.Index) bool {
	return usable(e) && e.Kind == model.KindNonclustered && !e.Unique && e.Filter == "" &&
		!constraint(e) && len(e.NamedIn) == 0
}

// existing returns the indexes of t in index_id order.
func existing(t *model.Table) []model.Index {
	ixs := slices.Clone(t.Indexes)
	slices.SortStableFunc(ixs, func(a, b model.Index) int { return a.IndexID - b.IndexID })
	return ixs
}

// Confront decides what to do with the candidate c on its table t, in the
// order of spec section 8.3: covered by an existing index; widen an existing
// nonclustered index that has the keys but not the included columns, or
// discuss when that index supports a constraint (errors 1907 and 3723);
// replace an existing index whose declared keys are a strict prefix of the
// candidate's; otherwise new, beside whatever exists. It returns the
// decision, the name of the existing index it is about, and the reasons.
// Two indexes that cover the candidate together do not cover it.
func Confront(c model.Candidate, t *model.Table) (model.Decision, string, []string) {
	ixs := existing(t)
	for _, e := range ixs {
		if Covers(e, c, t) {
			return model.DecisionCovered, e.Name, []string{fmt.Sprintf("covered by %s", e.Name)}
		}
	}
	var held *model.Index
	for _, e := range ixs {
		if !usable(e) || e.Kind != model.KindNonclustered || e.Filter != "" || !keysMatch(e, c, t) {
			continue
		}
		if !constraint(e) {
			return model.DecisionWiden, e.Name, []string{fmt.Sprintf("%s has the keys but not every included column: widen it, includes united, physical options kept", e.Name)}
		}
		if held == nil {
			held = &e
		}
	}
	if held != nil {
		return model.DecisionDiscuss, held.Name, []string{fmt.Sprintf(
			"%s has the keys but not every included column, and supports a constraint: SQL Server refuses DROP_EXISTING (error 1907) and DROP INDEX (error 3723)", held.Name)}
	}
	// The prefix is read on the declared keys: on a clustered table the
	// implicit clustered key closes the effective keys of a non unique index,
	// so (a, pk) is never a prefix of (a, b, pk) and replace would never apply
	// (decision of 8 November 2026, spec section 8.3). An identical index is
	// not recreated: same effective keys are covered or widened above.
	for _, e := range ixs {
		if replaceable(e) && isStrictPrefix(e.Keys, c.Keys) {
			return model.DecisionReplace, e.Name, []string{fmt.Sprintf(
				"the keys of %s are a strict prefix of the candidate's: replace it, includes united, physical options kept", e.Name)}
		}
	}
	return model.DecisionNew, "", []string{"no existing index covers, widens to or is replaced by the candidate"}
}

// united returns the includes of c and e together, less the keys and the
// clustered key, in column_id order.
func united(keys []model.KeyCol, c model.Candidate, e model.Index, t *model.Table) []int {
	ck := clusteredKeyCols(t)
	var out []int
	for _, id := range appendNew(slices.Clone(c.Include), e.Include...) {
		if !hasKey(keys, id) && !hasKey(ck, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// ConfrontAll confronts every candidate with the existing indexes of its
// table and writes Decision and Existing on it; no candidate is dropped (a
// covered one stays, with the name of the index that covers it, for the
// report). A widened candidate takes the keys of the existing index and the
// includes of both, a replacing one the includes of both, and its ID is
// computed again. For widen and replace, every query or write whose plan
// reads the existing index is added to Queries (spec section 8.6: the
// measure includes the queries that used the index, evaluated without it).
// The input is not changed.
func ConfrontAll(cands []model.Candidate, w *model.Workload) []model.Candidate {
	r := newResolver(w)
	out := slices.Clone(cands)
	for i := range out {
		c := &out[i]
		t := w.Tables[c.Table]
		if t == nil {
			c.Decision, c.Existing = model.DecisionNew, ""
			continue
		}
		c.Decision, c.Existing, _ = Confront(*c, t)
		if c.Decision != model.DecisionWiden && c.Decision != model.DecisionReplace {
			continue
		}
		e := t.Indexes[slices.IndexFunc(t.Indexes, func(ix model.Index) bool { return ix.Name == c.Existing })]
		if c.Decision == model.DecisionWiden {
			c.Keys = slices.Clone(e.Keys)
		}
		c.Include = united(c.Keys, *c, e, t)
		c.ID = ID(*c)
		qs := slices.Clone(c.Queries)
		for _, list := range [][]model.Query{w.Queries, w.Writes} {
			for _, q := range list {
				if q.Plan != nil && readsIndex(r, q, t, e.Name) {
					qs = append(qs, q.QueryID)
				}
			}
		}
		slices.Sort(qs)
		c.Queries = slices.Compact(qs)
	}
	return out
}

// readsIndex says whether a statement with a plan of q reads the index name
// of t.
func readsIndex(r resolver, q model.Query, t *model.Table, name string) bool {
	for _, st := range q.Plan.Statements {
		if st.Root == nil {
			continue
		}
		for _, a := range st.Accesses {
			if r.same(a.Obj.Index, name) && r.table(a.Obj) == t {
				return true
			}
		}
	}
	return false
}
