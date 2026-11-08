// Package score turns the measures of evaluate into one recommendation per
// candidate: gain, maintenance, estimated size and decision (spec section
// 8.6). It is pure: no connection, the same input gives the same output.
package score

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
)

// Decisions of a recommendation.
const (
	Recommended = "recommended"
	Discuss     = "discuss"
	Rejected    = "rejected"
)

// StatusMeasured is the status of a measure that has a cost before and after.
const StatusMeasured = "measured"

// Notes written on the recommendations.
const (
	NoteSampled       = "estimates use sampled statistics; a real index has full-scan statistics"
	NotePlanCache     = "plan cache: no reliable maintenance source"
	NoteRowsWritten   = "maintenance estimated from rows written"
	NoteUnreliable    = "unreliable estimate"
	noteRedundantBy   = "made redundant by "
	noteGainLow       = "gain below threshold"
	noteMaintenanceHi = "maintenance above threshold"
	noteNoMeasure     = "not enough measured queries"
)

// Thresholds are the configurable limits of the decision.
type Thresholds struct {
	// MinGainRatio is the smallest gain, as a fraction of the cumulated cost
	// before of the measured queries, that can be recommended.
	MinGainRatio float64
	// MaxMaintenanceRatio is the largest maintenance, as a fraction of the
	// gain, that can be recommended.
	MaxMaintenanceRatio float64
	// MinQueries is the smallest number of measured queries.
	MinQueries int
}

// DefaultThresholds: gain at least 10 % of the cost of the queries concerned,
// maintenance at most half the gain, at least one measured query.
func DefaultThresholds() Thresholds {
	return Thresholds{MinGainRatio: 0.10, MaxMaintenanceRatio: 0.5, MinQueries: 1}
}

// Formula writes the rule in plain words. It depends only on th, so it is
// the same from one run to the next.
func Formula(th Thresholds) string {
	pct := func(f float64) string { return strconv.FormatFloat(f*100, 'f', -1, 64) + "%" }
	return "gain = sum over the measured queries of (estimated cost before - estimated cost after) x executions; " +
		"maintenance = sum over the measured writes of the cost of the operator maintaining the index x executions " +
		"(rows written x executions for a write that could not be measured); " +
		"recommended when at least " + strconv.Itoa(th.MinQueries) + " query is measured, " +
		"gain > 0 and gain >= " + pct(th.MinGainRatio) + " of the summed cost before x executions, " +
		"and maintenance <= " + pct(th.MaxMaintenanceRatio) + " of gain; otherwise rejected; " +
		"a recommendation becomes discuss when the source is the plan cache, " +
		"when a combined measure uses other candidates instead of this one, " +
		"or when an estimate is unreliable"
}

// EstimatedPages is the number of data pages of an index of rows rows, the
// same estimate as the pages declared to DBCC AUTOPILOT (spec section 8.5):
// ceil(rows x (key and include bytes + clustered key bytes + 8 + 2) / 8096),
// at least 1.
func EstimatedPages(rows int64, keyAndIncludeBytes, clusteredKeyBytes float64) int64 {
	p := int64(math.Ceil(float64(rows) * (keyAndIncludeBytes + clusteredKeyBytes + 8 + 2) / 8096))
	return max(p, 1)
}

// IndexBytes gives the average bytes of the keys and includes of c, and of
// the row locator it carries: the clustered keys not already in c, or the
// 8-byte RID of a heap. Widths are the columns' AvgWidth.
func IndexBytes(t *model.Table, c model.Candidate) (keyAndInclude, clustered float64) {
	width := map[int]float64{}
	for _, col := range t.Columns {
		width[col.ID] = col.AvgWidth
	}
	in := map[int]bool{}
	for _, k := range c.Keys {
		in[k.ColumnID] = true
		keyAndInclude += width[k.ColumnID]
	}
	for _, id := range c.Include {
		if !in[id] {
			in[id] = true
			keyAndInclude += width[id]
		}
	}
	for _, ix := range t.Indexes {
		if ix.Kind != model.KindClustered || ix.Disabled || ix.Hypothetical {
			continue
		}
		for _, k := range ix.Keys {
			if !in[k.ColumnID] {
				clustered += width[k.ColumnID]
			}
		}
		return keyAndInclude, clustered
	}
	return keyAndInclude, 8
}

// Score gives one recommendation per candidate, in the order of cands.
//
// The measures of a candidate are those whose Candidate is its ID; the
// combined measures are those whose Combined lists its ID. Combined and
// UsedHypothetical hold candidate IDs. A measure whose Status is not
// "measured" brings neither gain nor maintenance and keeps its status in
// Queries.
func Score(w *model.Workload, cands []model.Candidate, ms []model.Measure, th Thresholds) []model.Recommendation {
	queries := map[int64]model.Query{}
	writes := map[int64]bool{}
	for _, q := range w.Queries {
		queries[q.QueryID] = q
	}
	for _, q := range w.Writes {
		queries[q.QueryID] = q
		writes[q.QueryID] = true
	}
	formula := Formula(th)

	out := make([]model.Recommendation, 0, len(cands))
	for _, c := range cands {
		r := model.Recommendation{Candidate: c, Formula: formula, Decision: Rejected}
		if t := w.Tables[c.Table]; t != nil {
			kb, cb := IndexBytes(t, c)
			r.SizeMB = float64(EstimatedPages(t.Rows, kb, cb)) * 8 / 1024
		}

		var before float64
		measured := map[int64]bool{}
		concerned := map[int64]bool{}
		for _, q := range c.Queries {
			concerned[q] = true
		}
		unreliable := false
		for _, m := range ms {
			if m.Candidate != c.ID {
				continue
			}
			r.Queries = append(r.Queries, m)
			concerned[m.QueryID] = true
			if hasUnreliable(m.Notes) {
				unreliable = true
			}
			if m.Status != StatusMeasured {
				continue
			}
			measured[m.QueryID] = true
			exec := float64(queries[m.QueryID].Executions)
			before += m.Before * exec
			r.Gain += (m.Before - m.After) * exec
			if writes[m.QueryID] {
				r.Maintenance += m.Maintenance * exec
			}
		}

		// Writes of the candidate without a measure: rows written.
		rowsWritten := false
		for _, id := range sortedKeys(concerned) {
			if !writes[id] || measured[id] {
				continue
			}
			q := queries[id]
			r.Maintenance += float64(q.Executions) * q.AvgRowcount
			rowsWritten = true
		}
		if rowsWritten {
			r.Notes = append(r.Notes, NoteRowsWritten)
		}

		if len(measured) == 0 {
			r.Gain = 0
		} else {
			r.Notes = append(r.Notes, NoteSampled)
		}

		switch {
		case len(measured) == 0 || len(measured) < th.MinQueries:
			r.Notes = append(r.Notes, noteNoMeasure)
		case r.Gain <= 0 || r.Gain < th.MinGainRatio*before:
			r.Notes = append(r.Notes, noteGainLow)
		case r.Maintenance > th.MaxMaintenanceRatio*r.Gain:
			r.Notes = append(r.Notes, noteMaintenanceHi)
		default:
			r.Decision = Recommended
		}

		// Caps: at best discuss.
		if w.Source == model.SourcePlanCache {
			r.Notes = append(r.Notes, NotePlanCache)
			r.Decision = capDiscuss(r.Decision)
		}
		for _, m := range ms {
			if slices.Contains(m.Combined, c.ID) && concerned[m.QueryID] && hasUnreliable(m.Notes) {
				unreliable = true
			}
		}
		if unreliable {
			r.Notes = append(r.Notes, NoteUnreliable)
			r.Decision = capDiscuss(r.Decision)
		}
		if by := redundantBy(c, concerned, ms); len(by) > 0 {
			r.Notes = append(r.Notes, noteRedundantBy+strings.Join(by, ", "))
			r.Decision = capDiscuss(r.Decision)
		}
		out = append(out, r)
	}
	return out
}

// redundantBy gives the other candidates that the measured combined
// measures of c's queries use, when none of them uses c.
func redundantBy(c model.Candidate, concerned map[int64]bool, ms []model.Measure) []string {
	seen := false
	others := map[string]bool{}
	for _, m := range ms {
		if m.Status != StatusMeasured || !concerned[m.QueryID] || !slices.Contains(m.Combined, c.ID) {
			continue
		}
		seen = true
		for _, u := range m.UsedHypothetical {
			if u == c.ID {
				return nil
			}
			others[u] = true
		}
	}
	if !seen {
		return nil
	}
	return sortedKeys(others)
}

func capDiscuss(d string) string {
	if d == Recommended {
		return Discuss
	}
	return d
}

func hasUnreliable(notes []string) bool {
	return slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, NoteUnreliable) })
}

func sortedKeys[K int64 | string](m map[K]bool) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}
