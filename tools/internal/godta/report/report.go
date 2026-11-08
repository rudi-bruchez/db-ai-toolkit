// Package report writes what goDTA found for one database: rapport.md for a
// person, indexes.sql for the DBA who decides, and result.json, the
// machine-readable twin of rapport.md that Claude reads before writing its
// own suggestions (spec sections 8.7 and 9). Summary writes synthese.md, one
// line per database, at the root of the workspace.
//
// The package is pure: it reads a Workload and the scored recommendations,
// opens no connection, and writes the same files for the same input.
package report

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// File names written by Write and Summary.
const (
	ReportFile  = "rapport.md"
	ScriptFile  = "indexes.sql"
	ResultFile  = "result.json"
	SummaryFile = "synthese.md"
)

// Recommendation decisions, as score writes them.
const (
	Recommended = "recommended"
	Discuss     = "discuss"
	Rejected    = "rejected"
)

// statusMeasured is the Status of a measure that has numbers.
const statusMeasured = "measured"

// Statuses of a candidate without measures: before evaluate ran, and after it
// ran without measuring any of its queries.
const (
	StatusNotRun  = "not evaluated: evaluate has not run"
	statusNoQuery = "not evaluated: no query"
)

// invisibleCode is written next to every DROP of the script.
const invisibleCode = "-- Code that did not run during the collection window (month-end jobs, reports, ad hoc\n" +
	"-- queries) is invisible to goDTA: check that nothing else needs %s before dropping it."

// Result is result.json: everything rapport.md says, as data.
type Result struct {
	Server   ResultServer `json:"server"`
	Database string       `json:"database"`
	// Source is "query_store" or "plan_cache"; Period says what the
	// counters cover, in words; Window is the collection window, which is
	// the period only for the Query Store.
	Source      string       `json:"source"`
	CaptureMode string       `json:"capture_mode"`
	Window      model.Window `json:"window"`
	Period      string       `json:"period"`
	// Skipped and Warnings are collect's; EvaluationWarnings come from the
	// evaluation.
	Skipped            []string `json:"skipped"`
	Warnings           []string `json:"warnings"`
	EvaluationWarnings []string `json:"evaluation_warnings"`
	Formulas           []string `json:"formulas"`
	// Recommendations holds every scored candidate, each with its Section:
	// "recommended", "discuss", "rejected" or "not_evaluated".
	Recommendations []ResultRec `json:"recommendations"`
	// AutoCreatedStats are the _WA_Sys_* statistics the engine created while
	// goDTA evaluated; Hypotheticals the hypothetical indexes found by
	// collect, schema.table.index; Redundancies the observations on the
	// existing indexes, without gain and without script.
	AutoCreatedStats []string `json:"auto_created_stats"`
	Hypotheticals    []string `json:"hypotheticals"`
	Redundancies     []string `json:"redundancies"`
	Script           string   `json:"script"`
}

// ResultServer identifies the instance.
type ResultServer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Major   int    `json:"major"`
	Level   string `json:"level"`
	Edition string `json:"edition"`
}

// ResultRec is one scored candidate. Gain, Maintenance and SizeMB are nil
// for a candidate that was not evaluated: it has no numbers.
type ResultRec struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	// Decision is score's ("recommended", "discuss", "rejected"), Action
	// the confrontation's ("new", "widen", "replace", "discuss",
	// "covered").
	Decision   string   `json:"decision"`
	Action     string   `json:"action"`
	Table      string   `json:"table"`
	Definition string   `json:"definition"`
	IndexName  string   `json:"index_name,omitempty"`
	Statements []string `json:"statements,omitempty"`
	Origin     string   `json:"origin"`
	Reason     string   `json:"reason,omitempty"`
	Rules      []string `json:"rules,omitempty"`
	// Existing is the index widened or replaced, or the one that covers
	// the candidate.
	Existing    *ResultExisting `json:"existing,omitempty"`
	Gain        *float64        `json:"gain"`
	Maintenance *float64        `json:"maintenance"`
	// Status says why a candidate of section "not_evaluated" has no
	// numbers: the statuses of its measures, or StatusNotRun.
	Status string `json:"status,omitempty"`
	// RowsWritten is in rows, not cost units; see model.Recommendation.
	RowsWritten float64       `json:"rows_written,omitempty"`
	SizeMB      *float64      `json:"size_mb"`
	Formula     string        `json:"formula,omitempty"`
	Queries     []ResultQuery `json:"queries"`
	Notes       []string      `json:"notes,omitempty"`
}

// ResultExisting is an existing index a candidate is about. Usage counters
// are indicative (DMV counters, not over the window). Dropped says whether
// the script drops it; KeptReason why not, for a replaced index.
type ResultExisting struct {
	Name       string            `json:"name"`
	Definition string            `json:"definition"`
	Usage      *model.IndexUsage `json:"usage,omitempty"`
	Dropped    bool              `json:"dropped"`
	KeptReason string            `json:"kept_reason,omitempty"`
}

// ResultQuery is one query a candidate serves. Before and After are nil when
// the measure has no numbers.
type ResultQuery struct {
	// Text is the full query text; rapport.md shows an excerpt.
	QueryID    int64    `json:"query_id"`
	Text       string   `json:"text"`
	Executions int64    `json:"executions"`
	IsWrite    bool     `json:"is_write"`
	Status     string   `json:"status"`
	Before     *float64 `json:"before"`
	After      *float64 `json:"after"`
	// Maintenance is the estimated cost of maintaining the index in one
	// execution of a write.
	Maintenance *float64 `json:"maintenance"`
	Notes       []string `json:"notes,omitempty"`
}

// item is one recommendation with everything rapport.md, indexes.sql and
// result.json need.
type item struct {
	rec     model.Recommendation
	table   *model.Table
	section string
	// def describes the proposed index; name, stmts and drop are the
	// script, empty when nothing is scripted.
	def      string
	name     string
	stmts    []string
	existing *model.Index
	drop     string
	kept     string
	skip     string
	// status is what a not evaluated candidate says instead of numbers.
	status string
}

// sections in their order in the report.
var sectionOrder = []string{Recommended, Discuss, Rejected, "not_evaluated"}

// measured reports whether at least one measure of r has numbers.
func measured(r model.Recommendation) bool {
	return slices.ContainsFunc(r.Queries, func(m model.Measure) bool { return m.Status == statusMeasured })
}

func sectionOf(r model.Recommendation) string {
	if !measured(r) {
		return "not_evaluated"
	}
	if r.Decision == Recommended && r.Candidate.Decision == model.DecisionDiscuss {
		// The confrontation left it to discuss (spec section 8.3): no
		// script, so never among the recommended indexes.
		return Discuss
	}
	switch r.Decision {
	case Recommended, Discuss:
		return r.Decision
	}
	return Rejected
}

// Write writes rapport.md, indexes.sql and result.json for the database of
// w into dir. evaluated says whether an evaluation was read; autoStats and
// warnings come from it.
func Write(ws workspace.WS, dir string, w *model.Workload, recs []model.Recommendation, evaluated bool, autoStats []string, warnings []string) error {
	items, err := build(w, recs, evaluated)
	if err != nil {
		return err
	}
	var redund []string
	for _, id := range tableIDs(w) {
		redund = append(redund, Redundancies(w.Tables[id])...)
	}
	script := renderScript(w, items)
	rep := renderReport(w, items, autoStats, warnings, redund)
	res := result(w, items, autoStats, warnings, redund)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if err := writeFile(dir, ScriptFile, script); err != nil {
		return err
	}
	if err := writeFile(dir, ReportFile, rep); err != nil {
		return err
	}
	return ws.WriteJSON(dir, ResultFile, res)
}

// writeFile writes s to dir/name (mode 0600) through a temporary file and a
// rename, like workspace.WriteJSON.
func writeFile(dir, name, s string) error {
	tmp, err := os.CreateTemp(dir, name+".tmp*")
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(s); err != nil {
		tmp.Close()
		return fmt.Errorf("report: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("report: close %s: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	return nil
}

func tableIDs(w *model.Workload) []int {
	ids := make([]int, 0, len(w.Tables))
	for id := range w.Tables {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int) int {
		ta, tb := w.Tables[a], w.Tables[b]
		return cmp.Or(cmp.Compare(ta.Schema, tb.Schema), cmp.Compare(ta.Name, tb.Name), cmp.Compare(a, b))
	})
	return ids
}

// build resolves every recommendation against the workload, in report order:
// by section, then gain descending for the measured ones, then ID.
func build(w *model.Workload, recs []model.Recommendation, evaluated bool) ([]*item, error) {
	var items []*item
	for _, r := range recs {
		t := w.Tables[r.Candidate.Table]
		if t == nil {
			return nil, fmt.Errorf("report: candidate %s: table %d is not in the workload", r.Candidate.ID, r.Candidate.Table)
		}
		it := &item{rec: r, table: t, section: sectionOf(r)}
		if it.section == "not_evaluated" {
			it.status = notEvaluatedStatus(r, evaluated)
		}
		def, err := describe(t, r.Candidate.Keys, r.Candidate.Include, "")
		if err != nil {
			return nil, fmt.Errorf("report: candidate %s: %w", r.Candidate.ID, err)
		}
		it.def = def
		if e := r.Candidate.Existing; e != "" {
			if i := slices.IndexFunc(t.Indexes, func(ix model.Index) bool { return ix.Name == e }); i >= 0 {
				it.existing = &t.Indexes[i]
			}
		}
		if it.section == Recommended {
			if err := it.plan(); err != nil {
				return nil, fmt.Errorf("report: candidate %s: %w", r.Candidate.ID, err)
			}
		}
		items = append(items, it)
	}
	slices.SortStableFunc(items, func(a, b *item) int {
		return cmp.Or(
			cmp.Compare(slices.Index(sectionOrder, a.section), slices.Index(sectionOrder, b.section)),
			cmp.Compare(b.rec.Gain, a.rec.Gain),
			cmp.Compare(a.rec.Candidate.ID, b.rec.Candidate.ID))
	})
	return items, nil
}

// notEvaluatedStatus is the statuses of the measures of r, or, without
// measures, StatusNotRun before evaluate and statusNoQuery after it.
func notEvaluatedStatus(r model.Recommendation, evaluated bool) string {
	var st []string
	for _, m := range r.Queries {
		if !slices.Contains(st, m.Status) {
			st = append(st, m.Status)
		}
	}
	switch {
	case len(st) > 0:
		return strings.Join(st, "; ")
	case !evaluated:
		return StatusNotRun
	}
	return statusNoQuery
}

// plan writes the script of a recommended candidate: CREATE for a new one,
// CREATE ... DROP_EXISTING for a widened one, CREATE then a guarded DROP for
// a replacing one.
func (it *item) plan() error {
	c, t := it.rec.Candidate, it.table
	switch c.Decision {
	case model.DecisionNew:
		it.name = indexName(t, c)
		def, err := indexDef(t, it.name, c.Keys, c.Include)
		if err != nil {
			return err
		}
		it.stmts = []string{tsql.CreateIndex(def)}
	case model.DecisionWiden, model.DecisionReplace:
		e := it.existing
		if e == nil {
			it.skip = fmt.Sprintf("the existing index %s is not in the collected catalog: nothing scripted", c.Existing)
			return nil
		}
		name := e.Name
		if c.Decision == model.DecisionReplace {
			name = indexName(t, c)
		}
		it.name = name
		def, err := indexDef(t, name, c.Keys, c.Include)
		if err != nil {
			return err
		}
		def.DataCompression = e.Compression
		def.FillFactor = e.FillFactor
		if e.DataSpace != "" {
			def.OnTarget = tsql.QuoteName(e.DataSpace)
		}
		if c.Decision == model.DecisionWiden {
			def.Unique, def.Filter = e.Unique, e.Filter
			it.stmts = []string{tsql.CreateIndexDropExisting(def)}
			return nil
		}
		it.stmts = []string{tsql.CreateIndex(def)}
		if why := protected(*e); why != "" {
			it.kept = why
			return nil
		}
		it.drop = fmt.Sprintf("IF EXISTS (SELECT 1 FROM sys.indexes WHERE object_id = OBJECT_ID(%s) AND name = %s) %s",
			tsql.QuoteString(obj(t).Quoted()), tsql.QuoteString(name), tsql.DropIndex(obj(t), e.Name))
	default:
		it.skip = fmt.Sprintf("decision %q of the confrontation is not scripted", c.Decision)
	}
	return nil
}

// protected says why the existing index e must never be dropped, or "".
func protected(e model.Index) string {
	switch {
	case e.PrimaryKey:
		return "it is a primary key"
	case e.UniqueConstraint:
		return "it supports a unique constraint"
	case e.Unique:
		return "it is unique"
	case e.Kind != model.KindNonclustered:
		return "it is a " + e.Kind + " index"
	case len(e.NamedIn) > 0:
		return "it is named in " + strings.Join(e.NamedIn, ", ")
	}
	return ""
}

func obj(t *model.Table) tsql.Obj { return tsql.Obj{Schema: t.Schema, Name: t.Name} }

func colName(t *model.Table, id int) (string, error) {
	for _, c := range t.Columns {
		if c.ID == id {
			return c.Name, nil
		}
	}
	return "", fmt.Errorf("column %d is not a column of %s.%s", id, t.Schema, t.Name)
}

func indexDef(t *model.Table, name string, keys []model.KeyCol, include []int) (tsql.IndexDef, error) {
	def := tsql.IndexDef{Table: obj(t), Name: name}
	for _, k := range keys {
		n, err := colName(t, k.ColumnID)
		if err != nil {
			return def, err
		}
		def.Keys = append(def.Keys, tsql.KeyCol{Name: n, Desc: k.Desc})
	}
	for _, id := range include {
		n, err := colName(t, id)
		if err != nil {
			return def, err
		}
		def.Include = append(def.Include, n)
	}
	return def, nil
}

// describe writes [schema].[table] (a ASC, b DESC) INCLUDE (c) [WHERE f].
func describe(t *model.Table, keys []model.KeyCol, include []int, filter string) (string, error) {
	var b strings.Builder
	b.WriteString(obj(t).Quoted() + " (")
	for i, k := range keys {
		n, err := colName(t, k.ColumnID)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(tsql.QuoteName(n))
		if k.Desc {
			b.WriteString(" DESC")
		} else {
			b.WriteString(" ASC")
		}
	}
	b.WriteString(")")
	if len(include) > 0 {
		names := make([]string, len(include))
		for i, id := range include {
			n, err := colName(t, id)
			if err != nil {
				return "", err
			}
			names[i] = tsql.QuoteName(n)
		}
		b.WriteString(" INCLUDE (" + strings.Join(names, ", ") + ")")
	}
	if filter != "" {
		b.WriteString(" WHERE " + filter)
	}
	return b.String(), nil
}

func describeIndex(t *model.Table, e model.Index) string {
	s, err := describe(t, e.Keys, e.Include, e.Filter)
	if err != nil {
		s = obj(t).Quoted() + " (" + err.Error() + ")"
	}
	var attrs []string
	if e.PrimaryKey {
		attrs = append(attrs, "primary key")
	} else if e.UniqueConstraint {
		attrs = append(attrs, "unique constraint")
	} else if e.Unique {
		attrs = append(attrs, "unique")
	}
	attrs = append(attrs, e.Kind)
	s += ", " + strings.Join(attrs, " ")
	if e.Compression != "" && e.Compression != "NONE" {
		s += ", compression " + e.Compression
	}
	if e.FillFactor > 0 {
		s += fmt.Sprintf(", fill factor %d", e.FillFactor)
	}
	if e.DataSpace != "" {
		s += ", on " + e.DataSpace
	}
	return s
}

// maxName is the identifier limit of SQL Server.
const maxName = 128

// indexName names a new or replacing index: IX_<table>_<keys>_<id>, the
// first six characters of the candidate ID making it unique and stable from
// one run to the next. It never has the form of a run object's name
// (tsql.RunPattern), which cleanup would drop. Too long, the table and key
// part is cut.
func indexName(t *model.Table, c model.Candidate) string {
	parts := []string{"IX", t.Name}
	for _, k := range c.Keys {
		n, err := colName(t, k.ColumnID)
		if err != nil {
			n = fmt.Sprint(k.ColumnID)
		}
		parts = append(parts, n)
	}
	id := c.ID
	if len(id) > 6 {
		id = id[:6]
	}
	suffix := "_" + id
	head := []rune(strings.Join(parts, "_"))
	// Counted in runes, not UTF-16 units: a name of runes outside the BMP
	// is cut a little further than needed, never too little.
	for len(string(head)) > 0 && utf16Len(head)+len(suffix) > maxName {
		head = head[:len(head)-1]
	}
	return string(head) + suffix
}

func utf16Len(rs []rune) int {
	n := 0
	for _, r := range rs {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// Redundancies lists, as observations, the existing indexes of t that are
// exact duplicates of another (same keys in the same order and direction,
// same included columns, same filter), that have the same keys as another
// with other included columns, or whose keys are a strict prefix of another's
// with the same filter. Only real, enabled rowstore indexes count; the
// comparison is on declared keys, by column_id.
func Redundancies(t *model.Table) []string {
	if t == nil {
		return nil
	}
	var ixs []model.Index
	for _, ix := range t.Indexes {
		if ix.Hypothetical || ix.Disabled || len(ix.Keys) == 0 {
			continue
		}
		if ix.Kind != model.KindNonclustered && ix.Kind != model.KindClustered && ix.Kind != model.KindViewClustered {
			continue
		}
		ixs = append(ixs, ix)
	}
	slices.SortFunc(ixs, func(a, b model.Index) int { return a.IndexID - b.IndexID })
	var out []string
	q := func(name string) string { return obj(t).Quoted() + "." + tsql.QuoteName(name) }
	for i, a := range ixs {
		for j, b := range ixs {
			if i == j || a.Filter != b.Filter {
				continue
			}
			switch {
			case j > i && slices.Equal(a.Keys, b.Keys) && sameSet(a.Include, b.Include):
				out = append(out, fmt.Sprintf("%s is an exact duplicate of %s", q(b.Name), q(a.Name)))
			case j > i && slices.Equal(a.Keys, b.Keys):
				out = append(out, fmt.Sprintf("%s has the same keys as %s, with other included columns", q(b.Name), q(a.Name)))
			case len(a.Keys) < len(b.Keys) && slices.Equal(a.Keys, b.Keys[:len(a.Keys)]):
				out = append(out, fmt.Sprintf("the keys of %s are a prefix of the keys of %s", q(a.Name), q(b.Name)))
			}
		}
	}
	return out
}

func sameSet(a, b []int) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// period says what the counters cover. From the plan cache there is no
// window: the counters are cumulated since each plan was compiled.
func period(w *model.Workload) string {
	if w.Source == model.SourcePlanCache {
		return "plan cache: execution counters cumulated since each plan was compiled, over no fixed period; " +
			"the collection window does not bound them"
	}
	return fmt.Sprintf("Query Store, from %s to %s (UTC)", fmtTime(w.Window.From), fmtTime(w.Window.Until))
}

func fmtTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

func num(f float64) string { return fmt.Sprintf("%.2f", f) }

// excerpt returns the first 100 characters of a query text, white space
// collapsed, backticks replaced so that it fits in a Markdown code span.
func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "`", "'")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "..."
	}
	return s
}

// lookup returns the query of the workload with id, read or write.
func lookup(w *model.Workload, id int64) (model.Query, bool) {
	for _, list := range [][]model.Query{w.Queries, w.Writes} {
		for _, q := range list {
			if q.QueryID == id {
				return q, true
			}
		}
	}
	return model.Query{}, false
}

func formulas(items []*item) []string {
	var fs []string
	for _, it := range items {
		if f := it.rec.Formula; f != "" && !slices.Contains(fs, f) {
			fs = append(fs, f)
		}
	}
	slices.Sort(fs)
	return fs
}

func hypotheticals(w *model.Workload) []string {
	var out []string
	for _, h := range w.Hypotheticals {
		name := fmt.Sprintf("object %d", h.ObjectID)
		if t := w.Tables[h.ObjectID]; t != nil {
			name = obj(t).Quoted()
		}
		out = append(out, name+"."+tsql.QuoteName(h.Name))
	}
	slices.Sort(out)
	return out
}

func origin(c model.Candidate) string {
	switch c.Origin {
	case "ai":
		return "AI suggestion"
	case "missing_index":
		return "missing index DMV"
	case "":
		return "unknown"
	}
	return c.Origin
}

func usage(u *model.IndexUsage) string {
	if u == nil {
		return "no usage counters"
	}
	return fmt.Sprintf("seeks %d, scans %d, lookups %d, updates %d, leaf inserts %d, leaf updates %d, leaf deletes %d "+
		"(indicative: DMV counters since the last restart or rebuild, not over the window)",
		u.Seeks, u.Scans, u.Lookups, u.Updates, u.LeafInserts, u.LeafUpdates, u.LeafDeletes)
}

var sectionTitle = map[string]string{
	Recommended:     "Recommended indexes",
	Discuss:         "To discuss",
	Rejected:        "Rejected",
	"not_evaluated": "Not evaluated",
}

func renderReport(w *model.Workload, items []*item, autoStats, warnings, redund []string) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	p("# goDTA report: %s on %s\n\n", w.Database.Name, w.Server.ServerName)
	p("## Context\n\n")
	p("- Server: %s, SQL Server %s (major %d, %s), %s\n", w.Server.ServerName, w.Server.Version, w.Server.Major, w.Server.Level, w.Server.Edition)
	p("- Database: %s, compatibility level %d, Query Store %s\n", w.Database.Name, w.Database.Compat, orNone(w.Database.QueryStoreState))
	p("- Workload source: %s\n", period(w))
	p("- Capture mode: %s\n", orNone(w.CaptureMode))
	p("- Queries read: %d, writes: %d\n", len(w.Queries), len(w.Writes))
	if fs := formulas(items); len(fs) > 0 {
		p("- Score formula: %s\n", strings.Join(fs, "; "))
	}
	p("- Costs are the optimizer's estimated costs, in its own units; a gain is a cost difference times executions.\n")
	list(&b, "What is missing", w.Skipped, "nothing was skipped")
	list(&b, "Warnings", append(slices.Clone(w.Warnings), warnings...), "none")

	for _, sec := range sectionOrder {
		p("\n## %s\n", sectionTitle[sec])
		n := 0
		for _, it := range items {
			if it.section != sec {
				continue
			}
			n++
			block(&b, w, it, n)
		}
		if n == 0 {
			p("\nNone.\n")
		}
	}
	list(&b, "Statistics created by the engine during the evaluation", autoStats,
		"none (the engine created no _WA_Sys_* statistics while goDTA evaluated)")
	list(&b, "Hypothetical indexes found before the run", hypotheticals(w), "none")
	p("\n## Redundancies among the existing indexes\n\n")
	p("Observations only: no gain is computed for them and the script does nothing about them.\n\n")
	if len(redund) == 0 {
		p("None.\n")
	}
	for _, r := range redund {
		p("- %s\n", r)
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func list(b *strings.Builder, title string, items []string, none string) {
	fmt.Fprintf(b, "\n## %s\n\n", title)
	if len(items) == 0 {
		fmt.Fprintf(b, "%s.\n", strings.ToUpper(none[:1])+none[1:])
		return
	}
	for _, s := range items {
		fmt.Fprintf(b, "- %s\n", s)
	}
}

func actionText(it *item) string {
	c := it.rec.Candidate
	switch c.Decision {
	case model.DecisionNew:
		return "new index"
	case model.DecisionWiden:
		return "widen " + c.Existing
	case model.DecisionReplace:
		return "replace " + c.Existing
	case model.DecisionCovered:
		return "covered by " + c.Existing
	case model.DecisionDiscuss:
		return "to discuss with " + c.Existing
	}
	return string(c.Decision)
}

func block(b *strings.Builder, w *model.Workload, it *item, n int) {
	p := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	r, c := it.rec, it.rec.Candidate
	p("\n### %d. %s, candidate %s\n\n", n, actionText(it), c.ID)
	p("- Index: `%s`\n", it.def)
	p("- Origin: %s\n", origin(c))
	if c.Reason != "" {
		p("- Reason given: %s\n", c.Reason)
	}
	if len(c.Rules) > 0 {
		p("- Rules: %s\n", strings.Join(c.Rules, ", "))
	}
	if it.section == "not_evaluated" {
		p("- Status: %s\n", it.status)
	} else {
		p("- Gain: %s, maintenance: %s, estimated size: %s MB\n", num(r.Gain), num(r.Maintenance), num(r.SizeMB))
		if r.RowsWritten != 0 {
			p("- Rows written by the unmeasured writes: %s\n", num(r.RowsWritten))
		}
	}
	if e := it.existing; e != nil {
		p("- Existing index %s: `%s`; %s\n", e.Name, describeIndex(it.table, *e), usage(e.Usage))
	} else if c.Existing != "" {
		p("- Existing index %s: not in the collected catalog\n", c.Existing)
	}
	if len(r.Queries) > 0 {
		p("- Queries:\n")
		for _, m := range r.Queries {
			q, _ := lookup(w, m.QueryID)
			kind := "read"
			if q.IsWrite {
				kind = "write"
			}
			if it.section == "not_evaluated" || m.Status != statusMeasured {
				p("  - query_id %d (%s), %d executions, %s: `%s`\n", m.QueryID, kind, q.Executions, m.Status, excerpt(q.Text))
				continue
			}
			maint := ""
			if q.IsWrite || m.Maintenance != 0 {
				maint = ", index maintenance " + num(m.Maintenance) + " per execution"
			}
			p("  - query_id %d (%s), %d executions, cost %s before, %s after%s: `%s`\n",
				m.QueryID, kind, q.Executions, num(m.Before), num(m.After), maint, excerpt(q.Text))
		}
	}
	for _, note := range r.Notes {
		p("- Note: %s\n", note)
	}
	if it.skip != "" {
		p("- Script: %s\n", it.skip)
	}
	if len(it.stmts) > 0 {
		p("\n```sql\n")
		for _, s := range it.stmts {
			p("%s\n", s)
		}
		if it.drop != "" {
			p("%s\n", it.drop)
		}
		p("```\n")
		if it.kept != "" {
			p("\nThe replaced index %s is kept, %s: drop it by hand only after checking.\n", c.Existing, it.kept)
		}
	}
}

func renderScript(w *model.Workload, items []*item) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	p("-- goDTA index script for database %s on %s\n", w.Database.Name, w.Server.ServerName)
	p("-- Workload source: %s\n", period(w))
	p("-- Statements have no terminating semicolon; each batch ends with a GO line\n")
	p("-- (sqlcmd, SSMS). Read every block and run only what you decided to run.\n")
	p("-- A replaced index is dropped after its replacement is created, and only if it exists.\n")
	p("\nUSE %s\nGO\n", tsql.QuoteName(w.Database.Name))
	p("-- CREATE INDEX needs these options on a table with an indexed view or a computed column.\n")
	p("SET ANSI_NULLS ON\nSET ANSI_PADDING ON\nSET ANSI_WARNINGS ON\nSET ARITHABORT ON\n")
	p("SET CONCAT_NULL_YIELDS_NULL ON\nSET QUOTED_IDENTIFIER ON\nSET NUMERIC_ROUNDABORT OFF\nGO\n")
	n := 0
	for _, it := range items {
		if it.section != Recommended {
			continue
		}
		n++
		c := it.rec.Candidate
		p("\n-- %d. %s, candidate %s, origin %s\n", n, actionText(it), c.ID, origin(c))
		p("-- Gain %s, maintenance %s, estimated size %s MB\n", num(it.rec.Gain), num(it.rec.Maintenance), num(it.rec.SizeMB))
		if it.rec.RowsWritten != 0 {
			p("-- Rows written by the unmeasured writes: %s\n", num(it.rec.RowsWritten))
		}
		if it.skip != "" {
			p("-- Not scripted: %s\n", it.skip)
			continue
		}
		if it.existing != nil && it.existing.DataSpace != "" && it.existing.DataSpace != "PRIMARY" {
			p("-- ON %s is the data space of %s. If it is a partition scheme, remove the ON clause:\n", tsql.QuoteName(it.existing.DataSpace), it.existing.Name)
			p("-- the index is then aligned with the table (a scheme without its column fails, error 2726).\n")
		}
		for _, s := range it.stmts {
			p("%s\nGO\n", s)
		}
		if it.drop != "" {
			p(invisibleCode+"\n", c.Existing)
			p("%s\nGO\n", it.drop)
		}
		if it.kept != "" {
			p("-- %s is kept, not dropped: %s.\n", c.Existing, it.kept)
		}
	}
	if n == 0 {
		p("\n-- No recommended index.\n")
	}
	return b.String()
}

func ptr(f float64) *float64 { return &f }

func result(w *model.Workload, items []*item, autoStats, warnings, redund []string) Result {
	res := Result{
		Server: ResultServer{Name: w.Server.ServerName, Version: w.Server.Version, Major: w.Server.Major,
			Level: w.Server.Level, Edition: w.Server.Edition},
		Database: w.Database.Name, Source: w.Source, CaptureMode: w.CaptureMode,
		Window: w.Window, Period: period(w),
		Skipped: nonNil(w.Skipped), Warnings: nonNil(w.Warnings), EvaluationWarnings: nonNil(warnings),
		Formulas: nonNil(formulas(items)), AutoCreatedStats: nonNil(autoStats),
		Hypotheticals: nonNil(hypotheticals(w)), Redundancies: nonNil(redund),
		Recommendations: []ResultRec{}, Script: ScriptFile,
	}
	for _, it := range items {
		r, c := it.rec, it.rec.Candidate
		rr := ResultRec{
			ID: c.ID, Section: it.section, Decision: r.Decision, Action: string(c.Decision),
			Table: obj(it.table).Quoted(), Definition: it.def, IndexName: it.name,
			Origin: c.Origin, Reason: c.Reason, Rules: c.Rules, Formula: r.Formula, Notes: r.Notes,
			Status: it.status, Queries: []ResultQuery{},
		}
		if len(it.stmts) > 0 {
			rr.Statements = slices.Clone(it.stmts)
			if it.drop != "" {
				rr.Statements = append(rr.Statements, it.drop)
			}
		}
		if it.section != "not_evaluated" {
			rr.Gain, rr.Maintenance, rr.SizeMB = ptr(r.Gain), ptr(r.Maintenance), ptr(r.SizeMB)
			rr.RowsWritten = r.RowsWritten
		}
		if e := it.existing; e != nil {
			rr.Existing = &ResultExisting{Name: e.Name, Definition: describeIndex(it.table, *e), Usage: e.Usage,
				Dropped: it.drop != "", KeptReason: it.kept}
		} else if c.Existing != "" {
			rr.Existing = &ResultExisting{Name: c.Existing, Definition: "not in the collected catalog"}
		}
		for _, m := range r.Queries {
			q, _ := lookup(w, m.QueryID)
			rq := ResultQuery{QueryID: m.QueryID, Text: q.Text, Executions: q.Executions,
				IsWrite: q.IsWrite, Status: m.Status, Notes: m.Notes}
			if it.section != "not_evaluated" && m.Status == statusMeasured {
				rq.Before, rq.After, rq.Maintenance = ptr(m.Before), ptr(m.After), ptr(m.Maintenance)
			}
			rr.Queries = append(rr.Queries, rq)
		}
		res.Recommendations = append(res.Recommendations, rr)
	}
	return res
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Summary writes synthese.md at the root of the workspace: one line per
// database, sorted by server then database.
func Summary(ws workspace.WS, rows []model.SummaryRow) error {
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b model.SummaryRow) int {
		return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.Database, b.Database))
	})
	var b strings.Builder
	b.WriteString("# goDTA summary\n\n")
	if len(rows) == 0 {
		b.WriteString("No database.\n")
	} else {
		b.WriteString("| Server | Database | Status | Recommended | Total gain | To discuss | Rejected | Not evaluated | Warnings |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
		cell := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
		for _, r := range rows {
			warn := "none"
			if len(r.Warnings) > 0 {
				warn = strings.Join(r.Warnings, "; ")
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %d | %d | %d | %s |\n", cell(r.Server), cell(r.Database),
				cell(r.Status), r.Recommended, num(r.Gain), r.Discuss, r.Rejected, r.NotEvaluated, cell(warn))
		}
	}
	if err := os.MkdirAll(ws.Root, 0o700); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	return writeFile(ws.Root, SummaryFile, b.String())
}
