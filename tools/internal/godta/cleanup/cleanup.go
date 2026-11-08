// Package cleanup finds and removes what goDTA left on a server: first from
// the journal, then, as a safety net, by the name pattern of a run
// (spec section 7.2).
//
// Find only reads. Run executes a sealed cleanup plan (runplan.ForCleanup)
// and nothing else: every statement it sends that writes is a step of the
// plan; the reads it adds check that the object still exists and is goDTA's.
package cleanup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/runplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// ServerDatabase is the Database of a pattern entry for a clone database: the
// step runs on the server connection, whose database is master.
const ServerDatabase = "master"

// Found is what Find returns. Journal holds the pending journal entries of
// the connected server; ByPattern the objects found by tsql.RunPattern that
// the journal does not list; Unknown, one line each, what looks like a
// leftover but is not goDTA's (another tool's hypothetical index, a name
// LIKE matches only through the collation) and journal entries of another
// server. Unknown is never removed.
type Found struct {
	Journal   []workspace.Entry `json:"journal"`
	ByPattern []workspace.Entry `json:"by_pattern"`
	Unknown   []string          `json:"unknown"`
}

// Entries returns Journal then ByPattern, the input of runplan.ForCleanup.
func (f Found) Entries() []workspace.Entry {
	out := make([]workspace.Entry, 0, len(f.Journal)+len(f.ByPattern))
	out = append(out, f.Journal...)
	return append(out, f.ByPattern...)
}

// Find lists what cleanup would remove. server is a connection on master,
// for sys.databases; open opens a pool on one database, which Find closes:
// each database is read through a pool opened on it, never through USE.
func Find(ctx context.Context, server conn.Querier, open func(db string) (*sql.DB, error), ws workspace.WS) (Found, error) {
	var f Found
	inst, err := conn.Identify(ctx, server)
	if err != nil {
		return Found{}, err
	}
	pending, err := ws.Pending()
	if err != nil {
		return Found{}, err
	}
	journaled := map[string]bool{}
	for _, e := range pending {
		if e.Server != inst.ServerName {
			f.Unknown = append(f.Unknown, fmt.Sprintf("journal: %s %s of server %s, not %s: connect to that server to clean it up",
				e.Kind, describe(e), e.Server, inst.ServerName))
			continue
		}
		f.Journal = append(f.Journal, e)
		journaled[identity(e)] = true
	}
	add := func(e workspace.Entry) {
		if journaled[identity(e)] {
			return
		}
		e.Server = inst.ServerName
		e.State = workspace.StateCreated
		e.RunID = runIDOf(e.Name)
		f.ByPattern = append(f.ByPattern, e)
	}

	dbs, clones, err := databases(ctx, server)
	if err != nil {
		return Found{}, err
	}
	for _, db := range dbs {
		entries, unknown, err := scanDatabase(ctx, server, open, db)
		if err != nil {
			return Found{}, err
		}
		for _, e := range entries {
			add(e)
		}
		f.Unknown = append(f.Unknown, unknown...)
	}
	for _, name := range clones {
		if !tsql.IsRunName(name) {
			f.Unknown = append(f.Unknown, fmt.Sprintf("database %s: a clone whose name matches the pattern only through the collation, not goDTA's", tsql.QuoteName(name)))
			continue
		}
		add(workspace.Entry{Database: ServerDatabase, Kind: workspace.KindCloneDatabase, Name: name})
	}
	return f, nil
}

// identity is what makes a pattern entry the same object as a journal entry.
// A clone database is known by its name alone: the journal records it under
// the source database.
func identity(e workspace.Entry) string {
	if e.Kind == workspace.KindCloneDatabase {
		return e.Kind + "\x00" + e.Name
	}
	return strings.Join([]string{e.Database, e.Kind, e.Schema, e.Table, e.Name, e.Path}, "\x00")
}

func describe(e workspace.Entry) string {
	switch e.Kind {
	case workspace.KindBackupFile:
		return e.Path
	case workspace.KindCloneDatabase:
		return tsql.QuoteName(e.Name)
	}
	return tsql.QuoteName(e.Database) + "." + tsql.Obj{Schema: e.Schema, Name: e.Table}.Quoted() + "." + tsql.QuoteName(e.Name)
}

// runIDOf returns the run ID inside a name of the form tsql.IsRunName checks.
func runIDOf(name string) string {
	if !tsql.IsRunName(name) {
		return ""
	}
	return name[len("godta_") : len("godta_")+8]
}

// databases lists the user databases to scan, and the clones that match the
// run pattern. A clone of goDTA is not scanned: it is dropped whole, and the
// statistics it copied from its source would be found there too.
func databases(ctx context.Context, server conn.Querier) (scan, clones []string, err error) {
	rows, err := server.QueryContext(ctx, `SELECT d.name,
  CASE WHEN d.name LIKE @p1 AND CAST(DATABASEPROPERTYEX(d.name, 'IsClone') AS int) = 1 THEN 1 ELSE 0 END
FROM sys.databases AS d
WHERE d.database_id > 4
ORDER BY d.name`, tsql.RunPattern())
	if err != nil {
		return nil, nil, fmt.Errorf("cleanup: listing databases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var clone bool
		if err := rows.Scan(&name, &clone); err != nil {
			return nil, nil, fmt.Errorf("cleanup: listing databases: %w", err)
		}
		if clone {
			clones = append(clones, name)
		} else {
			scan = append(scan, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("cleanup: listing databases: %w", err)
	}
	// The accessibility filter is a second query: HAS_DBACCESS and the state
	// are read after the list, so a database dropped in between is skipped
	// by readable rather than failing the scan.
	return scan, clones, rows.Close()
}

// readable reports whether db still exists, is online, multi-user and open
// to this login: a database another session is dropping, or one in
// SINGLE_USER, cannot be opened and holds nothing goDTA can remove now.
func readable(ctx context.Context, server conn.Querier, db string) (bool, error) {
	var ok int
	err := server.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys.databases
WHERE name = @p1 AND state = 0 AND user_access = 0 AND HAS_DBACCESS(name) = 1`, db).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("cleanup: database %s: %w", tsql.QuoteName(db), err)
	}
	return ok == 1, nil
}

// hypotheticalQuery lists every hypothetical index of the database: those of
// goDTA go to ByPattern, the others (the DTA's _dta_index_...) to Unknown.
const hypotheticalQuery = `SELECT SCHEMA_NAME(o.schema_id), o.name, i.name
FROM sys.indexes AS i
JOIN sys.objects AS o ON o.object_id = i.object_id
WHERE i.is_hypothetical = 1
ORDER BY SCHEMA_NAME(o.schema_id), o.name, i.name`

// statsQuery lists the statistics of the run pattern that are not an index's
// own: every index has statistics of its name, which DROP STATISTICS refuses
// (error 3739, measured).
const statsQuery = `SELECT SCHEMA_NAME(o.schema_id), o.name, s.name
FROM sys.stats AS s
JOIN sys.objects AS o ON o.object_id = s.object_id
WHERE s.name LIKE @p1
  AND o.is_ms_shipped = 0
  AND NOT EXISTS (SELECT 1 FROM sys.indexes AS i WHERE i.object_id = s.object_id AND i.index_id = s.stats_id)
ORDER BY SCHEMA_NAME(o.schema_id), o.name, s.name`

func scanDatabase(ctx context.Context, server conn.Querier, open func(string) (*sql.DB, error), db string) ([]workspace.Entry, []string, error) {
	ok, err := readable(ctx, server, db)
	if err != nil || !ok {
		return nil, nil, err
	}
	pool, err := open(db)
	if err != nil {
		if again, aerr := readable(ctx, server, db); aerr == nil && !again {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("cleanup: opening %s: %w", tsql.QuoteName(db), err)
	}
	defer pool.Close()

	var entries []workspace.Entry
	var unknown []string
	err = eachRow(ctx, pool, hypotheticalQuery, nil, func(schema, table, name string) {
		e := workspace.Entry{Database: db, Kind: workspace.KindHypotheticalIndex, Schema: schema, Table: table, Name: name}
		if tsql.IsRunName(name) {
			entries = append(entries, e)
			return
		}
		unknown = append(unknown, fmt.Sprintf("hypothetical index %s: not goDTA's, left in place", describe(e)))
	})
	if err != nil {
		return nil, nil, fmt.Errorf("cleanup: %s: hypothetical indexes: %w", tsql.QuoteName(db), err)
	}
	err = eachRow(ctx, pool, statsQuery, []any{tsql.RunPattern()}, func(schema, table, name string) {
		e := workspace.Entry{Database: db, Kind: workspace.KindStatistics, Schema: schema, Table: table, Name: name}
		if tsql.IsRunName(name) {
			entries = append(entries, e)
			return
		}
		unknown = append(unknown, fmt.Sprintf("statistics %s: matches the pattern only through the collation, not goDTA's", describe(e)))
	})
	if err != nil {
		return nil, nil, fmt.Errorf("cleanup: %s: statistics: %w", tsql.QuoteName(db), err)
	}
	return entries, unknown, nil
}

func eachRow(ctx context.Context, q conn.Querier, query string, args []any, f func(a, b, c string)) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			return err
		}
		f(a, b, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}

// ErrNotCleanupPlan is returned for a plan of another phase.
var ErrNotCleanupPlan = errors.New("not a cleanup plan")

// Run executes the sealed cleanup plan p, step by step, and records each
// object it removes as "removed" in the journal. It refuses a plan whose
// checksum is wrong or that was made for another server (runplan.Verify).
//
// Before each step it checks, by a read, that the object is still there and
// still goDTA's kind of object: one already gone (an entry left "planned" by
// a crash before the creation, a backup file deleted by hand) is recorded
// "removed" without sending the step, since xp_delete_file on a missing file
// and DROP on a missing object raise an error. A real index, a database that
// is not a clone, under a journaled name, is refused, never dropped.
//
// Each drop runs on its own connection under SET LOCK_TIMEOUT
// runplan.DropLockTimeoutMS: a Sch-M request that waits makes every new
// query on the table wait behind it. A failed step leaves its entry pending
// and Run goes on with the next; the failures come back joined.
func Run(ctx context.Context, server conn.Querier, open func(db string) (*sql.DB, error), ws workspace.WS, p runplan.Plan) ([]workspace.Entry, error) {
	if p.Phase != runplan.PhaseCleanup {
		return nil, fmt.Errorf("cleanup: %w: phase %q", ErrNotCleanupPlan, p.Phase)
	}
	if len(p.Steps) == 0 {
		// Nothing to send: ForCleanup of no entries names no server.
		return nil, nil
	}
	inst, err := conn.Identify(ctx, server)
	if err != nil {
		return nil, err
	}
	// A cleanup plan covers the whole server: its Database is empty.
	if err := runplan.Verify(p, inst, p.Database, nil); err != nil {
		return nil, fmt.Errorf("cleanup: %w", err)
	}
	pools := map[string]*sql.DB{}
	defer func() {
		for _, db := range pools {
			db.Close()
		}
	}()
	target := func(e workspace.Entry) (conn.Querier, error) {
		switch e.Kind {
		case workspace.KindCloneDatabase, workspace.KindBackupFile:
			return server, nil
		}
		if db, ok := pools[e.Database]; ok {
			return db, nil
		}
		db, err := open(e.Database)
		if err != nil {
			return nil, err
		}
		pools[e.Database] = db
		return db, nil
	}

	var removed []workspace.Entry
	var errs []error
	for _, s := range p.Steps {
		if s.Journal == nil {
			errs = append(errs, fmt.Errorf("step %d: no journal entry", s.N))
			continue
		}
		e := *s.Journal
		fail := func(err error) {
			errs = append(errs, fmt.Errorf("step %d, %s %s: %w", s.N, e.Kind, describe(e), err))
		}
		q, err := target(e)
		if err != nil {
			fail(err)
			continue
		}
		present, err := exists(ctx, q, e)
		if err != nil {
			fail(err)
			continue
		}
		if present {
			if err := execStep(ctx, q, s.SQL); err != nil {
				fail(err)
				continue
			}
		}
		e.At = time.Now().UTC()
		e.State = workspace.StateRemoved
		if err := ws.Record(e); err != nil {
			// The object is gone but the journal does not say so: stop, a
			// journal that cannot be written is not one to go on with.
			return removed, errors.Join(append(errs, fmt.Errorf("step %d: %w", s.N, err))...)
		}
		removed = append(removed, e)
	}
	return removed, errors.Join(errs...)
}

// errNotGoDTAs refuses to drop an object of the journaled name that is not of
// the journaled kind.
var errNotGoDTAs = errors.New("an object of this name exists but is not of the journaled kind; left in place")

// exists reports whether the object of e is still there, of e's kind.
func exists(ctx context.Context, q conn.Querier, e workspace.Entry) (bool, error) {
	table := tsql.Obj{Schema: e.Schema, Name: e.Table}.Quoted()
	switch e.Kind {
	case workspace.KindHypotheticalIndex:
		var n, hypo int
		err := q.QueryRowContext(ctx, `SELECT COUNT(*), ISNULL(SUM(CASE WHEN is_hypothetical = 1 THEN 1 ELSE 0 END), 0)
FROM sys.indexes WHERE object_id = OBJECT_ID(@p1) AND name = @p2`, table, e.Name).Scan(&n, &hypo)
		if err != nil {
			return false, err
		}
		if n > hypo {
			return false, errNotGoDTAs
		}
		return n > 0, nil
	case workspace.KindStatistics:
		var n, own int
		err := q.QueryRowContext(ctx, `SELECT COUNT(*), ISNULL(SUM(CASE WHEN i.index_id IS NULL THEN 1 ELSE 0 END), 0)
FROM sys.stats AS s
LEFT JOIN sys.indexes AS i ON i.object_id = s.object_id AND i.index_id = s.stats_id
WHERE s.object_id = OBJECT_ID(@p1) AND s.name = @p2`, table, e.Name).Scan(&n, &own)
		if err != nil {
			return false, err
		}
		if n > own {
			return false, errNotGoDTAs
		}
		return n > 0, nil
	case workspace.KindCloneDatabase:
		var id, clone sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT DB_ID(@p1), CAST(DATABASEPROPERTYEX(@p1, 'IsClone') AS int)`, e.Name).Scan(&id, &clone)
		if err != nil {
			return false, err
		}
		if !id.Valid {
			return false, nil
		}
		if clone.Int64 != 1 {
			return false, errNotGoDTAs
		}
		return true, nil
	case workspace.KindBackupFile:
		// First column of xp_fileexist: the file exists (and is not a
		// directory, which the second column says).
		var file, dir, parent int
		err := q.QueryRowContext(ctx, "EXEC master.dbo.xp_fileexist "+tsql.QuoteString(e.Path)).Scan(&file, &dir, &parent)
		if err != nil {
			return false, err
		}
		return file == 1, nil
	}
	return false, fmt.Errorf("unknown kind %q", e.Kind)
}

// execStep sends one plan step on a connection of its own, after a short,
// fixed lock timeout.
func execStep(ctx context.Context, q conn.Querier, stmt string) error {
	type conner interface {
		Conn(ctx context.Context) (*sql.Conn, error)
	}
	if db, ok := q.(conner); ok {
		c, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		q = c
	}
	if _, err := q.ExecContext(ctx, tsql.SetLockTimeout(runplan.DropLockTimeoutMS)); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, stmt)
	return err
}
