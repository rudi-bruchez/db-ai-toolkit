// Package evaluate measures the candidates with hypothetical indexes under
// AUTOPILOT (spec section 8.5): a session on one pinned connection, the
// reference check against a plain SHOWPLAN_XML, the measure of each candidate
// alone and of the candidates of a table together, and the maintenance cost
// read in the write plans.
package evaluate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/replay"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/score"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
)

// Errors of the session.
var (
	// ErrNotInPlan refuses a write statement that is not a step of the
	// execution plan the run was given.
	ErrNotInPlan = errors.New("statement not in the execution plan")
	// ErrAutopilotOff stops the evaluation: SET AUTOPILOT ON failed, and
	// the next statement would have run for real.
	ErrAutopilotOff = errors.New("SET AUTOPILOT ON failed")
	// ErrNotAPlan is a compilation that did not return exactly one
	// ShowPlanXML document; the connection is abandoned.
	ErrNotAPlan = errors.New("the compilation did not return one ShowPlanXML document")
	// ErrCompileTimeout is a compilation that exceeded the compile timeout;
	// the connection is abandoned.
	ErrCompileTimeout = errors.New("compilation timed out")
)

// execer sends one statement in its own batch. Query returns the rows of
// every result set, one []any per row.
type execer interface {
	Exec(ctx context.Context, sql string) error
	Query(ctx context.Context, sql string) ([][]any, error)
}

// Session is an AUTOPILOT session on one pinned connection of the pool of
// the evaluated database.
type Session struct {
	db             *sql.DB
	c              *sql.Conn
	ex             execer
	reopen         func(ctx context.Context) (execer, error)
	abandon        func()
	dbID           int
	enterprise     bool
	compileTimeout time.Duration
	// lockTimeout is the session's current SET LOCK_TIMEOUT, replayed on a
	// new connection.
	lockTimeout int
	// declared are the DBCC AUTOPILOT declarations sent since the last
	// reset, in order, replayed on a new connection.
	declared []string
	// allowed are the statements of the execution plan; nil allows any
	// statement (lab mode).
	allowed map[string]bool
	procN   int
}

// connExecer runs statements on a pinned *sql.Conn.
type connExecer struct{ c *sql.Conn }

func (e connExecer) Exec(ctx context.Context, s string) error {
	_, err := e.c.ExecContext(ctx, s)
	return err
}

func (e connExecer) Query(ctx context.Context, s string) ([][]any, error) {
	rows, err := e.c.QueryContext(ctx, s)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for {
		cols, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			out = append(out, vals)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, rows.Close()
}

// NewSession pins a connection of db, the pool opened on the evaluated
// database, reads DB_ID(), resets the AUTOPILOT list at once (without it a
// compilation under AUTOPILOT fails with error 8601, measured) and sets the
// lock timeout. A connection the session abandons (a compile timeout, a
// result that is not a plan) is replaced by a new one of the same pool, the
// session state replayed.
func NewSession(ctx context.Context, db *sql.DB, inst conn.Instance, compileTimeout time.Duration, lockTimeoutMS int) (*Session, error) {
	s := &Session{
		db:             db,
		enterprise:     inst.IsEnterpriseFeatures,
		compileTimeout: compileTimeout,
		lockTimeout:    lockTimeoutMS,
	}
	s.reopen = func(ctx context.Context) (execer, error) {
		c, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		s.c = c
		return connExecer{c}, nil
	}
	s.abandon = func() {
		if s.c == nil {
			return
		}
		// Returning driver.ErrBadConn from Raw makes database/sql close
		// the connection instead of handing it back to the pool.
		_ = s.c.Raw(func(any) error { return driver.ErrBadConn })
		_ = s.c.Close()
		s.c = nil
	}
	ex, err := s.reopen(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluate: pinning a connection: %w", err)
	}
	s.ex = ex
	rows, err := s.ex.Query(ctx, "SELECT DB_ID()")
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("evaluate: DB_ID(): %w", err)
	}
	id, ok := intOf(rows)
	if !ok {
		s.Close()
		return nil, fmt.Errorf("evaluate: DB_ID() returned %v", rows)
	}
	s.dbID = id
	if err := s.start(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// start sends what a new connection needs: the reset of the AUTOPILOT list,
// the lock timeout and the declarations since the last reset.
func (s *Session) start(ctx context.Context) error {
	if err := s.ex.Exec(ctx, tsql.AutopilotReset(s.dbID)); err != nil {
		return fmt.Errorf("evaluate: %s: %w", tsql.AutopilotReset(s.dbID), err)
	}
	if s.lockTimeout > 0 {
		if err := s.ex.Exec(ctx, tsql.SetLockTimeout(s.lockTimeout)); err != nil {
			return fmt.Errorf("evaluate: %s: %w", tsql.SetLockTimeout(s.lockTimeout), err)
		}
	}
	for _, d := range s.declared {
		if err := s.ex.Exec(ctx, d); err != nil {
			return fmt.Errorf("evaluate: %s: %w", d, err)
		}
	}
	return nil
}

// replace abandons the connection and pins a new one, its state replayed.
func (s *Session) replace(ctx context.Context) error {
	s.abandon()
	ex, err := s.reopen(ctx)
	if err != nil {
		return fmt.Errorf("%w: replacing the connection: %w", errSessionLost, err)
	}
	s.ex = ex
	if err := s.start(ctx); err != nil {
		return fmt.Errorf("%w: %w", errSessionLost, err)
	}
	return nil
}

// Close returns the pinned connection to the pool.
func (s *Session) Close() error {
	if s.c == nil {
		return nil
	}
	err := s.c.Close()
	s.c = nil
	return err
}

// DBID is the database_id the session works in.
func (s *Session) DBID() int { return s.dbID }

// usePlan restricts Exec to the statements of p's steps; nil lifts it.
func (s *Session) usePlan(p *runplan.Plan) {
	if p == nil {
		s.allowed = nil
		return
	}
	s.allowed = map[string]bool{}
	for _, st := range p.Steps {
		s.allowed[st.SQL] = true
	}
}

// Exec sends one statement. Under an execution plan, a statement that is
// not one of its steps is refused with ErrNotInPlan and never sent. A hook
// writes through it.
func (s *Session) Exec(ctx context.Context, stmt string) error {
	if s.allowed != nil && !s.allowed[stmt] {
		return fmt.Errorf("%w: %s", ErrNotInPlan, stmt)
	}
	if err := s.ex.Exec(ctx, stmt); err != nil {
		return err
	}
	var ms int
	if _, err := fmt.Sscanf(stmt, "SET LOCK_TIMEOUT %d", &ms); err == nil {
		s.lockTimeout = ms
	}
	return nil
}

// query sends a catalog query (never a write).
func (s *Session) query(ctx context.Context, q string) ([][]any, error) {
	return s.ex.Query(ctx, q)
}

// Reset empties the session's AUTOPILOT list.
func (s *Session) Reset(ctx context.Context) error {
	s.declared = nil
	if err := s.ex.Exec(ctx, tsql.AutopilotReset(s.dbID)); err != nil {
		return fmt.Errorf("evaluate: %s: %w", tsql.AutopilotReset(s.dbID), err)
	}
	return nil
}

func (s *Session) declare(ctx context.Context, stmt string) error {
	for _, d := range s.declared {
		if d == stmt {
			return nil
		}
	}
	if err := s.ex.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("evaluate: %s: %w", stmt, err)
	}
	s.declared = append(s.declared, stmt)
	return nil
}

// DeclareReal declares to AUTOPILOT, with pages 0 (the real size), every
// enabled real nonclustered rowstore index of t not in hide (by index_id),
// and, when the instance substitutes indexed views by itself (Enterprise and
// Developer), every indexed view over t, and t itself when it is one, by
// type 6. An undeclared real index is hidden from the optimizer under
// AUTOPILOT; the clustered index and the heap are visible without
// declaration (spec section 8.5).
func (s *Session) DeclareReal(ctx context.Context, t *model.Table, hide map[int]bool) error {
	isView := false
	for _, ix := range t.Indexes {
		if ix.Kind == model.KindViewClustered {
			isView = true
		}
		if ix.Kind != model.KindNonclustered || ix.Disabled || ix.Hypothetical || hide[ix.IndexID] {
			continue
		}
		if err := s.declare(ctx, tsql.AutopilotIndex(s.dbID, t.ObjectID, ix.IndexID, 0)); err != nil {
			return err
		}
	}
	if !s.enterprise {
		return nil
	}
	views := t.Views
	if isView {
		views = append([]int{t.ObjectID}, views...)
	}
	for _, v := range views {
		if err := s.declare(ctx, tsql.AutopilotView(s.dbID, v)); err != nil {
			return err
		}
	}
	return nil
}

// DeclareHypothetical declares the hypothetical index indexID of objectID
// with its estimated data pages: undeclared it is invisible, declared with 0
// pages it counts as nearly empty.
func (s *Session) DeclareHypothetical(ctx context.Context, objectID, indexID int, pages int64) error {
	return s.declare(ctx, tsql.AutopilotIndex(s.dbID, objectID, indexID, int(pages)))
}

// HypotheticalPages is the number of data pages declared for a hypothetical
// index (spec section 8.5): ceil(rows x (keyAndIncludeBytes +
// clusteredKeyBytes + 8 + 2) / 8096), at least 1. It is score's estimate,
// so that the size the report shows is the size the measure used.
func HypotheticalPages(rows int64, keyAndIncludeBytes, clusteredKeyBytes float64) int64 {
	return score.EstimatedPages(rows, keyAndIncludeBytes, clusteredKeyBytes)
}

const (
	showplanOn   = "SET SHOWPLAN_XML ON"
	showplanOff  = "SET SHOWPLAN_XML OFF"
	autopilotOn  = "SET AUTOPILOT ON"
	autopilotOff = "SET AUTOPILOT OFF"
)

// tempProc returns a new temporary procedure name for this session.
func (s *Session) tempProc(runID string) string {
	s.procN++
	return fmt.Sprintf("#godta_%s_r%d", runID, s.procN)
}

// Compile compiles r without running it: the SET options, the Setup, then
// SET SHOWPLAN_XML ON or, with autopilot, SET AUTOPILOT ON alone in its
// batch (its failure is ErrAutopilotOff), the Call under the compile
// timeout, OFF, the Teardown. The Call must return one ShowPlanXML document;
// otherwise, and on a timeout, the connection is abandoned, never reused,
// and replaced. It returns the last statement of the plan that has an
// operator tree.
func (s *Session) Compile(ctx context.Context, r replay.Replay, autopilot bool) (*showplan.Statement, error) {
	if r.Kind == replay.KindUnreplayable {
		return nil, fmt.Errorf("evaluate: unreplayable query: %s", r.Reason)
	}
	for _, o := range r.SetOptions {
		if err := s.ex.Exec(ctx, o); err != nil {
			return nil, fmt.Errorf("evaluate: %s: %w", o, err)
		}
	}
	for i, st := range r.Setup {
		if err := s.ex.Exec(ctx, st); err != nil {
			s.teardown(ctx, r.Teardown[:min(i, len(r.Teardown))])
			return nil, fmt.Errorf("evaluate: setup: %w", err)
		}
	}
	on, off := showplanOn, showplanOff
	if autopilot {
		on, off = autopilotOn, autopilotOff
	}
	if err := s.ex.Exec(ctx, on); err != nil {
		if rerr := s.replace(ctx); rerr != nil {
			return nil, errors.Join(fmt.Errorf("evaluate: %s: %w", on, err), rerr)
		}
		if autopilot {
			return nil, fmt.Errorf("%w: %w", ErrAutopilotOff, err)
		}
		return nil, fmt.Errorf("evaluate: %s: %w", on, err)
	}
	cctx, cancel := context.WithTimeout(ctx, s.compileTimeout)
	rows, err := s.ex.Query(cctx, r.Call)
	timedOut := cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil
	cancel()
	if err != nil || timedOut {
		if rerr := s.replace(ctx); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		if timedOut {
			return nil, fmt.Errorf("%w after %s: %v", ErrCompileTimeout, s.compileTimeout, err)
		}
		return nil, err
	}
	doc, ok := planDoc(rows)
	if !ok {
		if rerr := s.replace(ctx); rerr != nil {
			return nil, errors.Join(ErrNotAPlan, rerr)
		}
		return nil, ErrNotAPlan
	}
	if err := s.ex.Exec(ctx, off); err != nil {
		if rerr := s.replace(ctx); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		return nil, fmt.Errorf("evaluate: %s: %w", off, err)
	}
	if err := s.teardown(ctx, r.Teardown); err != nil {
		return nil, err
	}
	p, err := showplan.Parse([]byte(doc))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotAPlan, err)
	}
	for i := len(p.Statements) - 1; i >= 0; i-- {
		if p.Statements[i].Root != nil {
			st := p.Statements[i]
			return &st, nil
		}
	}
	return nil, fmt.Errorf("%w: no statement with a plan", ErrNotAPlan)
}

func (s *Session) teardown(ctx context.Context, stmts []string) error {
	for _, st := range stmts {
		if err := s.ex.Exec(ctx, st); err != nil {
			return fmt.Errorf("evaluate: teardown: %w", err)
		}
	}
	return nil
}

// planDoc returns the document of a result made of one row of one column
// that holds a ShowPlanXML document.
func planDoc(rows [][]any) (string, bool) {
	if len(rows) != 1 || len(rows[0]) != 1 {
		return "", false
	}
	var doc string
	switch v := rows[0][0].(type) {
	case string:
		doc = v
	case []byte:
		doc = string(v)
	default:
		return "", false
	}
	if !strings.Contains(doc[:min(len(doc), 512)], "<ShowPlanXML") {
		return "", false
	}
	return doc, true
}

// intOf reads a result of one row whose first column is an integer.
func intOf(rows [][]any) (int, bool) {
	if len(rows) != 1 || len(rows[0]) == 0 {
		return 0, false
	}
	switch v := rows[0][0].(type) {
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case int:
		return v, true
	case int16:
		return int(v), true
	case uint8:
		return int(v), true
	}
	return 0, false
}

// errorNumber is the SQL Server error number of err, 0 when it has none.
func errorNumber(err error) int {
	var e mssql.Error
	if errors.As(err, &e) {
		return int(e.Number)
	}
	return 0
}
