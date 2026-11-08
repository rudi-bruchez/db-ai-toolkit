// Package runplan builds the execution plan a --dry-run writes and a write
// phase executes (spec section 7.1): the run ID, the ordered write statements
// with their final object names, the catalog query that resolves each value
// known only at execution, the side effects, the schema fingerprint of the
// tables concerned, and a checksum over all of it.
//
// It opens no connection except in ReadFingerprints, the live catalog read.
package runplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// Phases, as plan files name them (workspace.PlanFile).
const (
	PhaseEvaluate = "evaluate"
	PhaseCleanup  = "cleanup"
	PhaseClone    = "clone"
)

// IndexIDMarker stands in a step's SQL for the index_id its Resolve query
// returns at execution.
const IndexIDMarker = "{{index_id}}"

// DropLockTimeoutMS is the short, fixed lock timeout of every DROP INDEX of
// evaluate (spec section 8.5): never a growing one, which would lengthen the
// queue of queries waiting behind the Sch-M request as much.
const DropLockTimeoutMS = 1000

// Errors Verify wraps, so that a caller can tell them apart.
var (
	ErrChecksum      = errors.New("checksum mismatch")
	ErrWrongServer   = errors.New("wrong server")
	ErrWrongDatabase = errors.New("wrong database")
	ErrSchemaDrift   = errors.New("schema changed")
)

// Plan is an execution plan. Checksum is the SHA-256 of the plan's JSON
// without that field (Seal).
type Plan struct {
	RunID       string    `json:"run_id"`
	Phase       string    `json:"phase"`
	Server      string    `json:"server"`
	MachineName string    `json:"machine_name"`
	Database    string    `json:"database"`
	CreatedAt   time.Time `json:"created_at"`
	// MaxCombined and LockTimeoutMS are the values chosen at --dry-run for
	// evaluate; the lock timeout is also the SQL of its SET LOCK_TIMEOUT step.
	MaxCombined   int                `json:"max_combined,omitempty"`
	LockTimeoutMS int                `json:"lock_timeout_ms,omitempty"`
	Steps         []Step             `json:"steps"`
	Schema        []TableFingerprint `json:"schema"`
	SideEffects   []string           `json:"side_effects"`
	// Skipped says which candidates or journal entries got no step, and why.
	Skipped  []string `json:"skipped,omitempty"`
	Checksum string   `json:"checksum,omitempty"`
}

// Step is one write statement. Resolve, when set, is the catalog query the
// runtime sends after SQL to learn a value known only at execution (the
// index_id of a hypothetical index); later statements that need it carry
// IndexIDMarker. Journal, when set, is the journal entry of the object the
// step creates or drops: for a creation, the "planned" entry the runtime
// records before sending SQL (then the same entry as "created" after); for a
// drop, the "removed" entry it records after success. Candidate is the ID of
// the candidate a step creates or drops.
type Step struct {
	N         int              `json:"n"`
	SQL       string           `json:"sql"`
	Resolve   string           `json:"resolve,omitempty"`
	Journal   *workspace.Entry `json:"journal,omitempty"`
	Candidate string           `json:"candidate,omitempty"`
}

// TableFingerprint is the live state of a table at --dry-run: its
// modify_date and the hash of the sorted definition of all its real indexes.
type TableFingerprint struct {
	ObjectID      int       `json:"object_id"`
	Schema        string    `json:"schema"`
	Name          string    `json:"name"`
	ModifyDate    time.Time `json:"modify_date"`
	IndexesSHA256 string    `json:"indexes_sha256"`
}

func (f TableFingerprint) label() string {
	if f.Name == "" {
		return "object_id " + strconv.Itoa(f.ObjectID)
	}
	return tsql.Obj{Schema: f.Schema, Name: f.Name}.Quoted()
}

// IndexName is the name of the n-th hypothetical index of a run, n from 1 in
// candidate order.
func IndexName(runID string, n int) string {
	return "godta_" + runID + "_" + strconv.Itoa(n)
}

// Tables returns the object_ids of every table and indexed view of the
// workload, sorted: those whose fingerprint an evaluate plan records, since a
// real index declared to AUTOPILOT on any table a query reads changes the
// measure.
func Tables(w *model.Workload) []int {
	ids := make([]int, 0, len(w.Tables))
	for id := range w.Tables {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Definition is the index definition of candidate c on table t under name.
// ok is false when a column of c is not a column of t.
func Definition(t *model.Table, c model.Candidate, name string) (tsql.IndexDef, bool) {
	col := func(id int) (string, bool) {
		for _, cl := range t.Columns {
			if cl.ID == id {
				return cl.Name, true
			}
		}
		return "", false
	}
	ix := tsql.IndexDef{Table: tsql.Obj{Schema: t.Schema, Name: t.Name}, Name: name}
	for _, k := range c.Keys {
		n, ok := col(k.ColumnID)
		if !ok {
			return tsql.IndexDef{}, false
		}
		ix.Keys = append(ix.Keys, tsql.KeyCol{Name: n, Desc: k.Desc})
	}
	for _, id := range c.Include {
		n, ok := col(id)
		if !ok {
			return tsql.IndexDef{}, false
		}
		ix.Include = append(ix.Include, n)
	}
	return ix, true
}

// ResolveIndexID is the catalog query that gives the index_id of index name
// on object objectID.
func ResolveIndexID(objectID int, name string) string {
	return fmt.Sprintf("SELECT index_id FROM sys.indexes WHERE object_id = %d AND name = %s",
		objectID, tsql.QuoteString(name))
}

// Side effects of evaluate on the server, from spec sections 7.1 and 8.5.
var evaluateSideEffects = []string{
	"Creating a hypothetical index takes a shared (S) lock on the table, and Sch-S: a writer in progress blocks it, a reader does not; it reads the table by sampling.",
	"The creation holds this S lock during the sampled read: it blocks writers for that time.",
	"Dropping takes Sch-M: a long reader blocks it, and while it waits every new query on the table waits behind it. A drop that fails on the lock timeout would leave the index behind: goDTA retries it with a short, fixed lock timeout and a pause between attempts (never a growing timeout, which would lengthen the queue as much), then, after the last attempt, journals it for cleanup.",
	"Cached plans of the tables touched are not invalidated by the hypothetical index (measured: same plan_handle, plan_generation_num unchanged); CREATE STATISTICS does not invalidate them either, DROP STATISTICS makes them recompile.",
	"The table's missing index suggestions are cleared by the creation: collect must therefore run first, which the order of the phases guarantees.",
	"Compilation may auto-create _WA_Sys_* statistics on columns without statistics. They are the ones the engine would have created on the first query on these columns; goDTA lists them in the report but does not drop them (their name lacks goDTA's prefix and they may serve other queries).",
	"The replay creates temporary procedures (#godta_...) in tempdb, under the query's SET options; they disappear with the session.",
}

// ForEvaluate builds the plan of evaluate --target server: per table, in the
// order tables first appear among the candidates, SET LOCK_TIMEOUT
// lockTimeoutMS, the creation of each candidate's hypothetical index with its
// journal entry and the query resolving its index_id, then SET LOCK_TIMEOUT
// DropLockTimeoutMS and the drop of each. Every creation of a table comes
// before its first drop: the combined measure needs all of them. Candidates
// covered by an existing index and candidates on a table with a columnstore
// index get no step (spec sections 8.3 and 8.5).
func ForEvaluate(w *model.Workload, cands []model.Candidate, runID string, fp []TableFingerprint, maxCombined, lockTimeoutMS int) Plan {
	p := Plan{
		RunID:         runID,
		Phase:         PhaseEvaluate,
		Server:        w.Server.ServerName,
		MachineName:   w.Server.MachineName,
		Database:      w.Database.Name,
		CreatedAt:     time.Now().UTC(),
		MaxCombined:   maxCombined,
		LockTimeoutMS: lockTimeoutMS,
		Schema:        sortedFingerprints(fp),
		SideEffects:   slices.Clone(evaluateSideEffects),
	}
	type planned struct {
		c    model.Candidate
		id   string
		name string
		ix   tsql.IndexDef
	}
	byTable := map[int][]planned{}
	var order []int
	n := 0
	for _, c := range cands {
		id := c.ID
		if id == "" {
			id = candidate.ID(c)
		}
		t := w.Tables[c.Table]
		switch {
		case c.Decision == model.DecisionCovered:
			p.Skipped = append(p.Skipped, fmt.Sprintf("candidate %s: covered by %s", id, c.Existing))
			continue
		case t == nil:
			p.Skipped = append(p.Skipped, fmt.Sprintf("candidate %s: table %d not in the workload", id, c.Table))
			continue
		case t.HasColumnstore:
			p.Skipped = append(p.Skipped, fmt.Sprintf("candidate %s: table %s has a columnstore index", id, tsql.Obj{Schema: t.Schema, Name: t.Name}.Quoted()))
			continue
		}
		n++
		name := IndexName(runID, n)
		ix, ok := Definition(t, c, name)
		if !ok {
			n--
			p.Skipped = append(p.Skipped, fmt.Sprintf("candidate %s: a column is not in table %s", id, tsql.Obj{Schema: t.Schema, Name: t.Name}.Quoted()))
			continue
		}
		if _, seen := byTable[c.Table]; !seen {
			order = append(order, c.Table)
		}
		byTable[c.Table] = append(byTable[c.Table], planned{c, id, name, ix})
	}
	add := func(s Step) {
		s.N = len(p.Steps) + 1
		p.Steps = append(p.Steps, s)
	}
	for _, tid := range order {
		t := w.Tables[tid]
		add(Step{SQL: tsql.SetLockTimeout(lockTimeoutMS)})
		for _, pc := range byTable[tid] {
			add(Step{
				SQL:       tsql.CreateHypothetical(pc.ix),
				Resolve:   ResolveIndexID(tid, pc.name),
				Journal:   entry(p, t, pc.name, workspace.StatePlanned),
				Candidate: pc.id,
			})
		}
		add(Step{SQL: tsql.SetLockTimeout(DropLockTimeoutMS)})
		for _, pc := range byTable[tid] {
			add(Step{
				SQL:       tsql.DropIndex(pc.ix.Table, pc.name),
				Journal:   entry(p, t, pc.name, workspace.StateRemoved),
				Candidate: pc.id,
			})
		}
	}
	return p
}

func entry(p Plan, t *model.Table, name, state string) *workspace.Entry {
	return &workspace.Entry{
		RunID:    p.RunID,
		Server:   p.Server,
		Database: p.Database,
		Kind:     workspace.KindHypotheticalIndex,
		Schema:   t.Schema,
		Table:    t.Name,
		Name:     name,
		State:    state,
	}
}

func sortedFingerprints(fp []TableFingerprint) []TableFingerprint {
	out := slices.Clone(fp)
	slices.SortFunc(out, func(a, b TableFingerprint) int { return a.ObjectID - b.ObjectID })
	return out
}

// ForCleanup builds the plan of cleanup from the journal's pending entries:
// one drop per object, in the journal's order, each step carrying the entry
// it turns "removed" (whose Database says where the step runs). The entries
// must come from one server: Server is the first entry's, and an entry of
// another server is skipped. MachineName is left empty: the journal does not
// record it, so the caller sets it from the instance identity before Seal,
// and Verify refuses the plan until it does. Temporary procedures get no step:
// they disappear with their session.
func ForCleanup(entries []workspace.Entry) Plan {
	p := Plan{
		RunID:     workspace.NewRunID(),
		Phase:     PhaseCleanup,
		CreatedAt: time.Now().UTC(),
	}
	if len(entries) > 0 {
		p.Server = entries[0].Server
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		if e.Server != p.Server {
			p.Skipped = append(p.Skipped, fmt.Sprintf("%s %s: server %s, not %s", e.Kind, e.Name, e.Server, p.Server))
			continue
		}
		t := tsql.Obj{Schema: e.Schema, Name: e.Table}
		var stmt string
		switch e.Kind {
		case workspace.KindHypotheticalIndex:
			stmt = tsql.DropIndex(t, e.Name)
		case workspace.KindStatistics:
			stmt = tsql.DropStatistics(t, e.Name)
		case workspace.KindCloneDatabase:
			stmt = tsql.DropDatabase(e.Name)
		case workspace.KindBackupFile:
			stmt = tsql.DeleteBackupFile(e.Path)
		case workspace.KindTempProcedure:
			p.Skipped = append(p.Skipped, fmt.Sprintf("%s %s: disappears with its session", e.Kind, e.Name))
			continue
		default:
			p.Skipped = append(p.Skipped, fmt.Sprintf("%s %s: unknown kind", e.Kind, e.Name))
			continue
		}
		kinds[e.Kind] = true
		removed := e
		removed.RunID = p.RunID
		removed.At = time.Time{}
		removed.State = workspace.StateRemoved
		p.Steps = append(p.Steps, Step{N: len(p.Steps) + 1, SQL: stmt, Journal: &removed})
	}
	if kinds[workspace.KindHypotheticalIndex] {
		p.SideEffects = append(p.SideEffects, evaluateSideEffects[2])
	}
	if kinds[workspace.KindStatistics] {
		p.SideEffects = append(p.SideEffects, "DROP STATISTICS makes the cached plans of the table recompile.")
	}
	if kinds[workspace.KindCloneDatabase] {
		p.SideEffects = append(p.SideEffects, "DROP DATABASE removes the clone database goDTA created.")
	}
	if kinds[workspace.KindBackupFile] {
		p.SideEffects = append(p.SideEffects, "master.dbo.xp_delete_file deletes the backup file goDTA wrote, through the server.")
	}
	return p
}

// checksum is the SHA-256, in lower-case hex, of p's JSON without Checksum.
func checksum(p Plan) string {
	p.Checksum = ""
	data, err := json.Marshal(p)
	if err != nil {
		panic("runplan: encode: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Seal returns p with its Checksum set.
func (p Plan) Seal() Plan {
	p.Checksum = checksum(p)
	return p
}

// Verify refuses a plan whose checksum is wrong, that was made for another
// server (ServerName or MachineName) or another database, or whose schema
// fingerprint differs from live, the fingerprints read again now. Every
// difference is named. The caller checks Phase.
func Verify(p Plan, inst conn.Instance, db string, live []TableFingerprint) error {
	if p.Checksum == "" || p.Checksum != checksum(p) {
		return fmt.Errorf("%w: the plan was changed after it was sealed, or never sealed", ErrChecksum)
	}
	if p.Server != inst.ServerName || p.MachineName != inst.MachineName {
		return fmt.Errorf("%w: plan for server %q (machine %q), connected to %q (machine %q)",
			ErrWrongServer, p.Server, p.MachineName, inst.ServerName, inst.MachineName)
	}
	if p.Database != db {
		return fmt.Errorf("%w: plan for database %q, not %q", ErrWrongDatabase, p.Database, db)
	}
	now := map[int]TableFingerprint{}
	for _, f := range live {
		now[f.ObjectID] = f
	}
	var diffs []string
	for _, f := range p.Schema {
		l, ok := now[f.ObjectID]
		switch {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("table %s: no longer found", f.label()))
			continue
		case l.Schema != f.Schema || l.Name != f.Name:
			diffs = append(diffs, fmt.Sprintf("table %s: now named %s", f.label(), l.label()))
		}
		if !l.ModifyDate.Equal(f.ModifyDate) {
			diffs = append(diffs, fmt.Sprintf("table %s: modify_date changed from %s to %s",
				f.label(), f.ModifyDate.Format(time.RFC3339Nano), l.ModifyDate.Format(time.RFC3339Nano)))
		}
		if l.IndexesSHA256 != f.IndexesSHA256 {
			diffs = append(diffs, fmt.Sprintf("table %s: index definitions changed", f.label()))
		}
	}
	if len(diffs) > 0 {
		return fmt.Errorf("%w since the plan was made: %s", ErrSchemaDrift, strings.Join(diffs, "; "))
	}
	return nil
}

// ReadFingerprints reads, live, the fingerprint of each table of tables (an
// object_id that no longer exists is left out). The hash covers every real
// index, the heap included, hypothetical ones excluded: per index, its id,
// name, type, uniqueness, constraint, disabled state, filter and columns in
// key order then includes.
func ReadFingerprints(ctx context.Context, q conn.Querier, tables []int) ([]TableFingerprint, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	ids := make([]string, len(tables))
	for i, id := range tables {
		ids[i] = strconv.Itoa(id)
	}
	in := strings.Join(ids, ", ")

	var out []*TableFingerprint
	rows, err := q.QueryContext(ctx, `SELECT o.object_id, SCHEMA_NAME(o.schema_id), o.name, o.modify_date
FROM sys.objects AS o WHERE o.object_id IN (`+in+`) ORDER BY o.object_id`)
	if err != nil {
		return nil, fmt.Errorf("fingerprints: %w", err)
	}
	for rows.Next() {
		f := &TableFingerprint{}
		if err := rows.Scan(&f.ObjectID, &f.Schema, &f.Name, &f.ModifyDate); err != nil {
			rows.Close()
			return nil, fmt.Errorf("fingerprints: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("fingerprints: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fingerprints: %w", err)
	}

	type column struct {
		ID       int  `json:"c"`
		Desc     bool `json:"d,omitempty"`
		Included bool `json:"i,omitempty"`
	}
	type index struct {
		ID               int      `json:"id"`
		Name             string   `json:"name"`
		Type             int      `json:"type"`
		Unique           bool     `json:"unique"`
		PrimaryKey       bool     `json:"pk"`
		UniqueConstraint bool     `json:"uc"`
		Disabled         bool     `json:"disabled"`
		Filter           string   `json:"filter"`
		Columns          []column `json:"columns"`
	}
	defs := map[int][]*index{}
	rows, err = q.QueryContext(ctx, `SELECT i.object_id, i.index_id, ISNULL(i.name, N''), i.type,
  i.is_unique, i.is_primary_key, i.is_unique_constraint, i.is_disabled, ISNULL(i.filter_definition, N''),
  ic.column_id, ic.is_descending_key, ic.is_included_column
FROM sys.indexes AS i
LEFT JOIN sys.index_columns AS ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
WHERE i.object_id IN (`+in+`) AND i.is_hypothetical = 0
ORDER BY i.object_id, i.index_id, ic.is_included_column, ic.key_ordinal, ic.column_id`)
	if err != nil {
		return nil, fmt.Errorf("fingerprints: %w", err)
	}
	for rows.Next() {
		var (
			oid int
			ix  index
			cid *int
			cd  *bool
			inc *bool
		)
		if err := rows.Scan(&oid, &ix.ID, &ix.Name, &ix.Type, &ix.Unique, &ix.PrimaryKey,
			&ix.UniqueConstraint, &ix.Disabled, &ix.Filter, &cid, &cd, &inc); err != nil {
			rows.Close()
			return nil, fmt.Errorf("fingerprints: %w", err)
		}
		list := defs[oid]
		if len(list) == 0 || list[len(list)-1].ID != ix.ID {
			cp := ix
			list = append(list, &cp)
			defs[oid] = list
		}
		if cid != nil {
			last := list[len(list)-1]
			last.Columns = append(last.Columns, column{ID: *cid, Desc: cd != nil && *cd, Included: inc != nil && *inc})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("fingerprints: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fingerprints: %w", err)
	}

	result := make([]TableFingerprint, 0, len(out))
	for _, f := range out {
		data, err := json.Marshal(defs[f.ObjectID])
		if err != nil {
			return nil, fmt.Errorf("fingerprints: %w", err)
		}
		sum := sha256.Sum256(data)
		f.IndexesSHA256 = hex.EncodeToString(sum[:])
		result = append(result, *f)
	}
	return result, nil
}
