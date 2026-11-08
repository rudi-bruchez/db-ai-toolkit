package evaluate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/replay"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/score"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// Statuses of a measure.
const (
	StatusMeasured          = score.StatusMeasured
	StatusColumnstore       = "not evaluated: columnstore"
	StatusReferenceMismatch = "not evaluated: reference mismatch"
	StatusTimeout           = "not evaluated: timeout"
	StatusLockTimeout       = "not evaluated: lock timeout"
)

// StatusUnreplayable is the status of a query that cannot be replayed.
func StatusUnreplayable(reason string) string {
	return "not evaluated: unreplayable (" + reason + ")"
}

// StatusError is the status of a SQL Server error n (0 when the error has no
// number).
func StatusError(n int) string { return "not evaluated: error " + strconv.Itoa(n) }

// DefaultMaxCombined is the largest group of candidates of a table measured
// together when the plan does not say.
const DefaultMaxCombined = 4

// Drops of hypothetical indexes: a short fixed lock timeout (the plan's
// step), three attempts, a fixed pause between two (spec section 8.5).
const dropAttempts = 3

var dropPause = 2 * time.Second

// Hooks lets the lab mode act on each hypothetical index once it exists. The
// server mode leaves it empty.
type Hooks struct {
	// AfterCreateHypothetical runs after the creation of the index of c,
	// named indexName; its notes go to every Measure of c. An error makes
	// c not evaluated (its index is still dropped).
	AfterCreateHypothetical func(ctx context.Context, s *Session, c model.Candidate, indexName string) (notes []string, err error)
}

// Result is what Run measured.
type Result struct {
	// RunID is the run ID of the plan: the one given, or, in the lab mode,
	// the one Run made. The journal entries of the run carry it.
	RunID    string
	Measures []model.Measure
	// AutoCreatedStats are the automatic statistics the compilations
	// created, [schema].[table].[name].
	AutoCreatedStats []string
	Warnings         []string
}

// errSessionLost is a connection that could not be replaced: the run stops.
var errSessionLost = errors.New("evaluate: session lost")

// planned is one candidate of the run and its plan steps.
type planned struct {
	c       model.Candidate
	id      string
	t       *model.Table
	name    string
	create  runplan.Step
	drop    runplan.Step
	hide    map[int]bool // index_ids of t hidden (widen, replace)
	indexID int
	created bool
	// uncertain is a creation interrupted by the end of ctx: the index may
	// exist, so the drops look it up.
	uncertain bool
	status    string // set when the candidate cannot be measured
	errText   string
	notes     []string
}

func (pc *planned) pages() int64 {
	kb, cb := score.IndexBytes(pc.t, pc.c)
	return HypotheticalPages(pc.t.Rows, kb, cb)
}

// tableRun is the candidates of one table, in plan order.
type tableRun struct {
	t     *model.Table
	cands []*planned
}

// ref is the reference of one query: its replay, the tables it touches, the
// SHOWPLAN_XML compilation and the one under AUTOPILOT with every real index
// declared.
type ref struct {
	q             model.Query
	write         bool
	rp            replay.Replay
	tables        []*model.Table
	showplanCost  float64
	showplanShape string
	before        float64
	beforeRows    float64
	shape         string
	status        string
	errText       string
}

type run struct {
	s           *Session
	ws          workspace.WS
	plan        *runplan.Plan
	w           *model.Workload
	h           Hooks
	maxCombined int
	refs        map[int64]*ref
	res         Result
}

// Run evaluates the candidates (spec section 8.5) and returns one Measure
// per query of each candidate, one per query of each combined group, the
// automatic statistics the compilations created and the warnings. p is the
// execution plan: on the source it is mandatory, and every write sent is one
// of its steps (anything else is ErrNotInPlan, before anything is sent). nil
// is the lab mode, which builds its plan itself and does not write on the
// source. The candidates are those the plan was made from, after
// candidate.ConfrontAll.
func Run(ctx context.Context, s *Session, ws workspace.WS, p *runplan.Plan, w *model.Workload, cands []model.Candidate, h Hooks) (Result, error) {
	plan := p
	if plan == nil {
		pl := runplan.ForEvaluate(w, cands, workspace.NewRunID(), nil, DefaultMaxCombined, s.lockTimeout)
		plan = &pl
	} else {
		s.usePlan(p)
		defer s.usePlan(nil)
	}
	r := &run{s: s, ws: ws, plan: plan, w: w, h: h, maxCombined: plan.MaxCombined, refs: map[int64]*ref{}}
	r.res.RunID = plan.RunID
	if r.maxCombined <= 0 {
		r.maxCombined = DefaultMaxCombined
	}
	groups, err := r.match(cands)
	if err != nil {
		return Result{}, err
	}
	ids := runplan.Tables(w)
	before, statsErr := r.autoStats(ctx, ids)
	if statsErr != nil {
		r.warn("reading sys.stats before the evaluation: %v", statsErr)
	}
	for _, g := range groups {
		if err := r.table(ctx, g); err != nil {
			return r.res, err
		}
	}
	if statsErr == nil {
		after, err := r.autoStats(ctx, ids)
		if err != nil {
			r.warn("reading sys.stats after the evaluation: %v", err)
		} else {
			for _, n := range after {
				if !slices.Contains(before, n) {
					r.res.AutoCreatedStats = append(r.res.AutoCreatedStats, n)
				}
			}
		}
	}
	return r.res, nil
}

func (r *run) warn(format string, args ...any) {
	r.res.Warnings = append(r.res.Warnings, fmt.Sprintf(format, args...))
}

// match pairs every candidate with its plan steps, by the filter of
// runplan.ForEvaluate: a covered candidate gets no step and no measure, a
// candidate on a columnstore table no step and a "columnstore" measure per
// query. Any other candidate without its creation and drop steps, or whose
// creation step is not its definition, is ErrNotInPlan. Nothing is sent.
func (r *run) match(cands []model.Candidate) ([]*tableRun, error) {
	steps := map[string][]runplan.Step{}
	for _, st := range r.plan.Steps {
		if st.Candidate != "" {
			steps[st.Candidate] = append(steps[st.Candidate], st)
		}
	}
	take := func(id string, create bool) (runplan.Step, bool) {
		list := steps[id]
		for i, st := range list {
			if (st.Resolve != "") == create {
				steps[id] = slices.Delete(list, i, i+1)
				return st, true
			}
		}
		return runplan.Step{}, false
	}
	var out []*tableRun
	byTable := map[int]*tableRun{}
	var columnstore []model.Measure
	for _, c := range cands {
		id := c.ID
		if id == "" {
			id = candidate.ID(c)
		}
		c.ID = id
		t := r.w.Tables[c.Table]
		switch {
		case c.Decision == model.DecisionCovered:
			continue
		case t == nil:
			r.warn("candidate %s: table %d not in the workload", id, c.Table)
			continue
		case t.HasColumnstore:
			for _, q := range c.Queries {
				columnstore = append(columnstore, model.Measure{QueryID: q, Candidate: id, Status: StatusColumnstore})
			}
			continue
		}
		create, ok1 := take(id, true)
		drop, ok2 := take(id, false)
		if !ok1 || !ok2 || create.Journal == nil {
			return nil, fmt.Errorf("%w: candidate %s has no creation and drop steps", ErrNotInPlan, id)
		}
		name := create.Journal.Name
		def, ok := runplan.Definition(t, c, name)
		if !ok {
			r.warn("candidate %s: a column is not in table %s", id, quoted(t))
			continue
		}
		if create.SQL != tsql.CreateHypothetical(def) || drop.SQL != tsql.DropIndex(def.Table, name) {
			return nil, fmt.Errorf("%w: the steps of candidate %s are not its definition", ErrNotInPlan, id)
		}
		pc := &planned{c: c, id: id, t: t, name: name, create: create, drop: drop, hide: map[int]bool{}}
		if c.Decision == model.DecisionWiden || c.Decision == model.DecisionReplace {
			for _, ix := range t.Indexes {
				if ix.Name == c.Existing {
					pc.hide[ix.IndexID] = true
				}
			}
		}
		g := byTable[c.Table]
		if g == nil {
			g = &tableRun{t: t}
			byTable[c.Table] = g
			out = append(out, g)
		}
		g.cands = append(g.cands, pc)
	}
	r.res.Measures = append(r.res.Measures, columnstore...)
	return out, nil
}

func quoted(t *model.Table) string { return tsql.Obj{Schema: t.Schema, Name: t.Name}.Quoted() }

// autoStats lists the automatic statistics of the tables ids.
func (r *run) autoStats(ctx context.Context, ids []int) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = strconv.Itoa(id)
	}
	rows, err := r.s.query(ctx, `SELECT /* godta:internal */ OBJECT_SCHEMA_NAME(s.object_id), OBJECT_NAME(s.object_id), s.name
FROM sys.stats AS s WHERE s.auto_created = 1 AND s.object_id IN (`+strings.Join(list, ", ")+`)
ORDER BY s.object_id, s.stats_id`)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, row := range rows {
		if len(row) != 3 {
			continue
		}
		sc, _ := row[0].(string)
		tb, _ := row[1].(string)
		n, _ := row[2].(string)
		out = append(out, tsql.Obj{Schema: sc, Name: tb}.Quoted()+"."+tsql.QuoteName(n))
	}
	return out, nil
}

// classify turns a compilation or statement error into a status. fatal is
// true when the run must stop: AUTOPILOT could not be set, the connection
// could not be replaced, or ctx is done.
func classify(ctx context.Context, err error) (status string, fatal bool) {
	switch {
	case errors.Is(err, ErrAutopilotOff), errors.Is(err, errSessionLost), ctx.Err() != nil:
		return "", true
	case errors.Is(err, ErrCompileTimeout):
		return StatusTimeout, false
	}
	n := errorNumber(err)
	if n == 1222 {
		return StatusLockTimeout, false
	}
	return StatusError(n), false
}

// lastStatement is the last statement of p that has an operator tree.
func lastStatement(p *showplan.Plan) *showplan.Statement {
	if p == nil {
		return nil
	}
	for i := len(p.Statements) - 1; i >= 0; i-- {
		if p.Statements[i].Root != nil {
			return &p.Statements[i]
		}
	}
	return nil
}

// tablesOf lists the tables of the workload a statement reads or writes:
// those of its accesses, the base tables of a substituted indexed view
// (which appear nowhere else in the plan), and those of its writes, sorted
// by object_id.
//
// A table qualified by a database is the workload's when the database is the
// workload's or current, the database the session compiles in: the plans of
// a lab clone name the clone, not the source database the workload was
// collected from, and a table of theirs left out would leave its real
// indexes undeclared, hence hidden under AUTOPILOT.
func tablesOf(w *model.Workload, st *showplan.Statement, current string) []*model.Table {
	if st == nil {
		return nil
	}
	var objs []showplan.ObjRef
	for _, a := range st.Accesses {
		objs = append(objs, a.Obj)
		objs = append(objs, a.ViewBase...)
	}
	for _, wr := range st.Writes {
		objs = append(objs, wr.Objs...)
	}
	var out []*model.Table
	for _, o := range objs {
		if o.Table == "" || (o.Database != "" && !w.Database.Matches(o.Database) &&
			(current == "" || !strings.EqualFold(o.Database, current))) {
			continue
		}
		if t := candidate.TableByName(w, o.Schema, o.Table); t != nil && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b *model.Table) int { return a.ObjectID - b.ObjectID })
	return out
}

func hasColumnstore(ts []*model.Table) bool {
	return slices.ContainsFunc(ts, func(t *model.Table) bool { return t.HasColumnstore })
}

func (r *run) query(id int64) (model.Query, bool, bool) {
	for _, q := range r.w.Queries {
		if q.QueryID == id {
			return q, q.IsWrite, true
		}
	}
	for _, q := range r.w.Writes {
		if q.QueryID == id {
			return q, true, true
		}
	}
	return model.Query{}, false, false
}

// reference computes, once per query, step 3 of spec section 8.5.
func (r *run) reference(ctx context.Context, id int64) (*ref, error) {
	if rf := r.refs[id]; rf != nil {
		return rf, nil
	}
	q, write, ok := r.query(id)
	rf := &ref{q: q, write: write}
	r.refs[id] = rf
	if !ok {
		rf.status = StatusUnreplayable("not in the workload")
		return rf, nil
	}
	err := measureReference(ctx, r.s, r.w, rf, r.plan.RunID)
	return rf, err
}

// measureReference fills rf for the query rf.q: SHOWPLAN_XML first, which
// creates the missing automatic statistics and refreshes a stale one, then
// AUTOPILOT with every real index of every table of the query declared. The
// two must have the same shape and cost. The order matters: a compilation
// under AUTOPILOT does neither (measured), so in the other order a query on
// a column without statistics comes out as a false mismatch. Only a fatal
// error is returned; the others go to rf.status.
func measureReference(ctx context.Context, s *Session, w *model.Workload, rf *ref, runID string) error {
	st := lastStatement(rf.q.Plan)
	if hasColumnstore(tablesOf(w, st, s.dbName)) {
		rf.status = StatusColumnstore
		return nil
	}
	rf.rp = replay.Build(rf.q, st, w, s.tempProc(runID))
	if rf.rp.Kind == replay.KindUnreplayable {
		rf.status = StatusUnreplayable(rf.rp.Reason)
		return nil
	}
	fail := func(err error) error {
		status, fatal := classify(ctx, err)
		if fatal {
			return err
		}
		rf.status, rf.errText = status, err.Error()
		return nil
	}
	sp, err := s.Compile(ctx, rf.rp, false)
	if err != nil {
		return fail(err)
	}
	rf.showplanCost, rf.showplanShape = sp.Cost, showplan.Shape(*sp)
	rf.tables = tablesOf(w, sp, s.dbName)
	if hasColumnstore(rf.tables) {
		rf.status = StatusColumnstore
		return nil
	}
	ap, err := compileDeclared(ctx, s, rf.rp, rf.tables, nil, nil)
	if err != nil {
		return fail(err)
	}
	rf.before, rf.shape, rf.beforeRows = ap.Cost, showplan.Shape(*ap), rootRows(ap)
	if rf.shape != rf.showplanShape || !showplan.SameCost(rf.before, rf.showplanCost) {
		rf.status = StatusReferenceMismatch
		rf.errText = fmt.Sprintf("SHOWPLAN_XML cost %g, AUTOPILOT cost %g; shape %s under SHOWPLAN_XML, %s under AUTOPILOT",
			rf.showplanCost, rf.before, rf.showplanShape, rf.shape)
		return nil
	}
	rf.status = StatusMeasured
	return nil
}

// compileDeclared resets the AUTOPILOT list, declares the real indexes of
// tables (hide gives, per table, the index_ids left undeclared) and the
// hypothetical indexes hyps, then compiles rp under AUTOPILOT.
func compileDeclared(ctx context.Context, s *Session, rp replay.Replay, tables []*model.Table, hide map[int]map[int]bool, hyps []*planned) (*showplan.Statement, error) {
	if err := s.Reset(ctx); err != nil {
		return nil, err
	}
	for _, t := range tables {
		if err := s.DeclareReal(ctx, t, hide[t.ObjectID]); err != nil {
			return nil, err
		}
	}
	for _, pc := range hyps {
		if err := s.DeclareHypothetical(ctx, pc.t.ObjectID, pc.indexID, pc.pages()); err != nil {
			return nil, err
		}
	}
	return s.Compile(ctx, rp, true)
}

// table runs steps 3 to 8 of spec section 8.5 for the candidates of one
// table. The drops run whatever happens once a creation was sent.
func (r *run) table(ctx context.Context, g *tableRun) (err error) {
	// Step 3: the references, before any hypothetical index of the table
	// exists.
	var qids []int64
	for _, pc := range g.cands {
		qids = append(qids, pc.c.Queries...)
	}
	slices.Sort(qids)
	for _, id := range slices.Compact(qids) {
		if _, err := r.reference(ctx, id); err != nil {
			return err
		}
	}

	// Step 4: the creations.
	if err := r.s.Exec(ctx, tsql.SetLockTimeout(r.plan.LockTimeoutMS)); err != nil {
		return fmt.Errorf("evaluate: %w", err)
	}
	defer func() {
		if derr := r.drops(ctx, g); err == nil {
			err = derr
		}
	}()
	for _, pc := range g.cands {
		if err := r.createOne(ctx, pc); err != nil {
			return err
		}
	}

	// Steps 5 and 6: each candidate alone.
	for _, pc := range g.cands {
		for _, id := range pc.c.Queries {
			m, err := r.measureOne(ctx, pc, id)
			if err != nil {
				return err
			}
			r.res.Measures = append(r.res.Measures, m)
		}
	}

	// Step 7: the candidates together, by groups of at most maxCombined.
	var created []*planned
	for _, pc := range g.cands {
		if pc.created && pc.status == "" {
			created = append(created, pc)
		}
	}
	for start := 0; start < len(created); start += r.maxCombined {
		group := created[start:min(start+r.maxCombined, len(created))]
		if len(group) < 2 {
			continue
		}
		if err := r.measureCombined(ctx, group); err != nil {
			return err
		}
	}
	return nil
}

// createOne journals and creates the hypothetical index of pc, reads its
// index_id and runs the hook.
func (r *run) createOne(ctx context.Context, pc *planned) error {
	if err := r.ws.Record(stamp(*pc.create.Journal, workspace.StatePlanned)); err != nil {
		return err
	}
	if err := r.s.Exec(ctx, pc.create.SQL); err != nil {
		if errors.Is(err, ErrNotInPlan) {
			return err
		}
		status, fatal := classify(ctx, err)
		if fatal {
			pc.uncertain = ctx.Err() != nil
			return err
		}
		pc.status, pc.errText = status, err.Error()
		return nil
	}
	pc.created = true
	if err := r.ws.Record(stamp(*pc.create.Journal, workspace.StateCreated)); err != nil {
		return err
	}
	rows, err := r.s.query(ctx, pc.create.Resolve)
	id, ok := intOf(rows)
	if err != nil || !ok {
		pc.status = StatusError(errorNumber(err))
		pc.errText = fmt.Sprintf("index_id of %s not found: %v", pc.name, err)
		return nil
	}
	pc.indexID = id
	if r.h.AfterCreateHypothetical != nil {
		notes, err := r.h.AfterCreateHypothetical(ctx, r.s, pc.c, pc.name)
		if err != nil {
			if errors.Is(err, ErrNotInPlan) || ctx.Err() != nil {
				return err
			}
			pc.status, pc.errText = StatusError(errorNumber(err)), err.Error()
		}
		pc.notes = notes
	}
	return nil
}

func stamp(e workspace.Entry, state string) workspace.Entry {
	e.State = state
	e.At = time.Now().UTC()
	return e
}

// measureOne measures query id with the candidate pc alone (step 5) and, for
// a write, its maintenance (step 6).
func (r *run) measureOne(ctx context.Context, pc *planned, id int64) (model.Measure, error) {
	rf := r.refs[id]
	m := model.Measure{QueryID: id, Candidate: pc.id, Before: rf.before, BeforeRows: rf.beforeRows, Shape: rf.shape, Notes: slices.Clone(pc.notes)}
	switch {
	case pc.status != "":
		m.Status, m.Error = pc.status, pc.errText
		return m, nil
	case rf.status != StatusMeasured:
		m.Status, m.Error = rf.status, rf.errText
		return m, nil
	}
	tables := withTable(rf.tables, pc.t)
	hide := map[int]map[int]bool{pc.t.ObjectID: pc.hide}
	after, err := compileDeclared(ctx, r.s, rf.rp, tables, hide, []*planned{pc})
	if err != nil {
		status, fatal := classify(ctx, err)
		if fatal {
			return m, err
		}
		m.Status, m.Error = status, err.Error()
		return m, nil
	}
	m.After, m.AfterRows, m.Shape = after.Cost, rootRows(after), showplan.Shape(*after)
	m.UsedHypothetical = used(after, []*planned{pc})
	m.Status = StatusMeasured
	if !rf.write {
		return m, nil
	}
	without, err := compileDeclared(ctx, r.s, rf.rp, tables, hide, nil)
	if err != nil {
		status, fatal := classify(ctx, err)
		if fatal {
			return m, err
		}
		m.Status, m.Error = status, "write reference: "+err.Error()
		return m, nil
	}
	m.Maintenance = maintenance(without, after, pc.name)
	return m, nil
}

// measureCombined measures every query of the group with all its
// candidates declared together.
func (r *run) measureCombined(ctx context.Context, group []*planned) error {
	var ids, notes []string
	var qids []int64
	hide := map[int]map[int]bool{}
	for _, pc := range group {
		ids = append(ids, pc.id)
		qids = append(qids, pc.c.Queries...)
		for _, n := range pc.notes {
			if !slices.Contains(notes, n) {
				notes = append(notes, n)
			}
		}
		h := hide[pc.t.ObjectID]
		if h == nil {
			h = map[int]bool{}
			hide[pc.t.ObjectID] = h
		}
		for ix := range pc.hide {
			h[ix] = true
		}
	}
	slices.Sort(qids)
	for _, id := range slices.Compact(qids) {
		rf := r.refs[id]
		if rf == nil || rf.status != StatusMeasured {
			continue
		}
		tables := rf.tables
		for _, pc := range group {
			tables = withTable(tables, pc.t)
		}
		m := model.Measure{QueryID: id, Combined: slices.Clone(ids), Before: rf.before, BeforeRows: rf.beforeRows, Notes: slices.Clone(notes)}
		after, err := compileDeclared(ctx, r.s, rf.rp, tables, hide, group)
		if err != nil {
			status, fatal := classify(ctx, err)
			if fatal {
				return err
			}
			m.Status, m.Error = status, err.Error()
		} else {
			m.After, m.AfterRows, m.Shape = after.Cost, rootRows(after), showplan.Shape(*after)
			m.UsedHypothetical = used(after, group)
			m.Status = StatusMeasured
		}
		r.res.Measures = append(r.res.Measures, m)
	}
	return nil
}

// rootRows is the estimated rows of the root operator of st, 0 without one.
func rootRows(st *showplan.Statement) float64 {
	if st == nil || st.Root == nil {
		return 0
	}
	return st.Root.EstimateRows
}

func withTable(ts []*model.Table, t *model.Table) []*model.Table {
	if slices.Contains(ts, t) {
		return ts
	}
	out := append(slices.Clone(ts), t)
	slices.SortFunc(out, func(a, b *model.Table) int { return a.ObjectID - b.ObjectID })
	return out
}

// used lists the IDs of the candidates whose hypothetical index the plan
// reads, sorted.
func used(st *showplan.Statement, cands []*planned) []string {
	var out []string
	for _, a := range st.Accesses {
		for _, pc := range cands {
			if a.Obj.Index == pc.name && strings.EqualFold(a.Obj.Table, pc.t.Name) && !slices.Contains(out, pc.id) {
				out = append(out, pc.id)
			}
		}
	}
	slices.Sort(out)
	return out
}

// maintenance is the cost of maintaining the hypothetical index name in the
// write plan with (compiled with the index declared), without the plan
// without (the same declarations, the index left out). An operator that
// maintains the index alone (a wide plan, Index Update) counts for its own
// cost; one that maintains several (a narrow plan: one Clustered Index
// Update lists the clustered key and every nonclustered index) counts for
// the difference of its own cost between the two plans, the same operator
// found in without by its Op and its other objects.
func maintenance(without, with *showplan.Statement, name string) float64 {
	var total float64
	for _, w := range with.Writes {
		if !slices.ContainsFunc(w.Objs, func(o showplan.ObjRef) bool { return o.Index == name }) {
			continue
		}
		others := slices.DeleteFunc(slices.Clone(w.Objs), func(o showplan.ObjRef) bool { return o.Index == name })
		if len(others) == 0 {
			total += w.OwnCost
			continue
		}
		base, found := 0.0, false
		if without != nil {
			for _, o := range without.Writes {
				if o.Op == w.Op && sameObjs(o.Objs, others) {
					base, found = o.OwnCost, true
					break
				}
			}
		}
		if !found {
			total += w.OwnCost
			continue
		}
		total += max(w.OwnCost-base, 0)
	}
	return total
}

func sameObjs(a, b []showplan.ObjRef) bool {
	key := func(o showplan.ObjRef) string { return o.Schema + "." + o.Table + "." + o.Index }
	ka := make([]string, len(a))
	for i, o := range a {
		ka[i] = key(o)
	}
	kb := make([]string, len(b))
	for i, o := range b {
		kb[i] = key(o)
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}

// drops runs step 8 for the created candidates of g: the plan's short lock
// timeout, then each drop, three attempts two seconds apart. A drop that
// succeeds is journaled "removed"; one that fails three times stays
// "created" for cleanup, with a warning. They run even when ctx is done, on
// a context of their own with a deadline. A creation that the end of ctx
// interrupted is looked up first: found, it is journaled "created" and
// dropped; not found, its "planned" entry is closed as "removed".
func (r *run) drops(ctx context.Context, g *tableRun) error {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	var created, uncertain []*planned
	for _, pc := range g.cands {
		switch {
		case pc.created:
			created = append(created, pc)
		case pc.uncertain:
			uncertain = append(uncertain, pc)
		}
	}
	if len(created)+len(uncertain) == 0 {
		return nil
	}
	// mayExist journals an interrupted creation "created": cleanup checks
	// whether the index is there before it drops it.
	mayExist := func(pc *planned) error {
		return r.ws.Record(stamp(*pc.create.Journal, workspace.StateCreated))
	}
	if err := r.s.Exec(dctx, tsql.SetLockTimeout(runplan.DropLockTimeoutMS)); err != nil {
		if errors.Is(err, ErrNotInPlan) {
			return err
		}
		if rerr := r.s.replace(dctx); rerr != nil {
			for _, pc := range uncertain {
				if jerr := mayExist(pc); jerr != nil {
					rerr = errors.Join(rerr, jerr)
				}
			}
			r.warn("table %s: the session is lost, %d hypothetical index(es) left for cleanup", quoted(g.t), len(created)+len(uncertain))
			return errors.Join(errSessionLost, err, rerr)
		}
		if err := r.s.Exec(dctx, tsql.SetLockTimeout(runplan.DropLockTimeoutMS)); err != nil {
			return fmt.Errorf("evaluate: %w", err)
		}
	}
	for _, pc := range uncertain {
		rows, err := r.s.query(dctx, pc.create.Resolve)
		if _, found := intOf(rows); err == nil && !found {
			if err := r.ws.Record(stamp(*pc.drop.Journal, workspace.StateRemoved)); err != nil {
				return err
			}
			continue
		}
		if jerr := mayExist(pc); jerr != nil {
			return jerr
		}
		if err != nil {
			r.warn("hypothetical index %s on %s: its creation was interrupted and it could not be looked up (%v): left in the journal for cleanup",
				pc.name, quoted(pc.t), err)
			continue
		}
		created = append(created, pc)
	}
	for _, pc := range created {
		var err error
		for attempt := 1; attempt <= dropAttempts; attempt++ {
			if err = r.s.Exec(dctx, pc.drop.SQL); err == nil {
				break
			}
			if errors.Is(err, ErrNotInPlan) {
				return err
			}
			if errorNumber(err) == 0 {
				// Not an engine error: the connection itself may be gone.
				if rerr := r.s.replace(dctx); rerr != nil {
					err = errors.Join(err, rerr)
				}
			}
			if attempt < dropAttempts {
				select {
				case <-dctx.Done():
				case <-time.After(dropPause):
				}
			}
		}
		if err != nil {
			r.warn("hypothetical index %s on %s not dropped after %d attempts (%v): left in the journal for cleanup",
				pc.name, quoted(pc.t), dropAttempts, err)
			continue
		}
		if err := r.ws.Record(stamp(*pc.drop.Journal, workspace.StateRemoved)); err != nil {
			return err
		}
	}
	return nil
}
