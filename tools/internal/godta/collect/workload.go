package collect

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// Warnings.
const (
	WarnPlanCache       = "plan cache counters are cumulative since compilation"
	WarnQueryStoreEmpty = "query store has no query in the window"
)

// marker is in the text of every query collect sends to a user database:
// the Query Store captures them too, and the workload leaves them out. The
// queries that filter on it carry it in a string literal, which matches.
const marker = "godta:collect"

// Database collects one database. db is a pool opened on that database
// (conn.Open with info.Name): every catalog query is in one- or two-part
// names and reads the connection's database.
func Database(ctx context.Context, db *sql.DB, inst conn.Instance, info model.DBInfo, opt Options) (*model.Workload, error) {
	opt = opt.withDefaults()
	w := &model.Workload{Server: inst, Window: opt.Window, Tables: map[int]*model.Table{}}
	if err := readDBInfo(ctx, db, info, w); err != nil {
		return nil, err
	}
	switch w.Database.QueryStoreState {
	case "READ_WRITE", "READ_ONLY":
		all, err := queryStoreQueries(ctx, db, opt)
		if err != nil {
			return nil, err
		}
		if len(all) > 0 {
			w.Source = model.SourceQueryStore
			if err := splitAndDetail(ctx, db, w, all, opt.TopN, queryStoreDetail); err != nil {
				return nil, err
			}
			break
		}
		w.Warnings = append(w.Warnings, WarnQueryStoreEmpty)
		fallthrough
	default:
		w.Source = model.SourcePlanCache
		w.Warnings = append(w.Warnings, WarnPlanCache)
		all, handles, err := planCacheQueries(ctx, db)
		if err != nil {
			return nil, err
		}
		detail := func(ctx context.Context, db *sql.DB, qs []*model.Query) error {
			return planCacheDetail(ctx, db, qs, handles)
		}
		if err := splitAndDetail(ctx, db, w, all, opt.TopN, detail); err != nil {
			return nil, err
		}
	}
	parsePlans(w)
	if err := readCatalog(ctx, db, w); err != nil {
		return nil, err
	}
	return w, nil
}

// readDBInfo reads the database the pool reached and checks it is the one
// asked for, then its Query Store state and capture mode.
func readDBInfo(ctx context.Context, db *sql.DB, info model.DBInfo, w *model.Workload) error {
	var d model.DBInfo
	var clone int
	err := db.QueryRowContext(ctx, `SELECT /* godta:collect */ DB_ID(), DB_NAME(),
  ISNULL(CAST(DATABASEPROPERTYEX(DB_NAME(), 'Collation') AS nvarchar(128)), N''),
  (SELECT compatibility_level FROM sys.databases WHERE database_id = DB_ID()),
  ISNULL(CAST(DATABASEPROPERTYEX(DB_NAME(), 'IsClone') AS int), 0);`).
		Scan(&d.ID, &d.Name, &d.Collation, &d.Compat, &clone)
	if err != nil {
		return fmt.Errorf("reading the database: %w", err)
	}
	if d.Name != info.Name {
		return fmt.Errorf("the connection reached database %q, not %q", d.Name, info.Name)
	}
	d.IsClone = clone == 1
	var state, mode sql.NullString
	err = db.QueryRowContext(ctx, `SELECT /* godta:collect */ actual_state_desc, query_capture_mode_desc
FROM sys.database_query_store_options;`).Scan(&state, &mode)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("reading the query store options: %w", err)
	}
	d.QueryStoreState = cmp.Or(state.String, "OFF")
	if d.QueryStoreState != "OFF" {
		w.CaptureMode = mode.String
	}
	w.Database = d
	return nil
}

// queryStoreAggregate totals the runtime statistics of each query over the
// intervals that overlap the window. Only columns present since 2016 are
// read. set_options is varbinary(8), converted to bigint here. The text is
// cut to 4000 characters, enough to tell a write from a read; the full text
// comes with the detail of the queries kept.
const queryStoreAggregate = `WITH /* godta:collect */ a AS (
  SELECT p.query_id,
    SUM(rs.count_executions) AS execs,
    SUM(rs.avg_cpu_time * rs.count_executions) / 1000.0 AS cpu_ms,
    SUM(rs.avg_logical_io_reads * rs.count_executions) AS reads,
    SUM(rs.avg_duration * rs.count_executions) / 1000.0 AS duration_ms,
    SUM(rs.avg_rowcount * rs.count_executions) / NULLIF(SUM(rs.count_executions), 0) AS avg_rows
  FROM sys.query_store_plan p
  JOIN sys.query_store_runtime_stats rs ON rs.plan_id = p.plan_id
  JOIN sys.query_store_runtime_stats_interval i ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
  WHERE i.end_time > @p1 AND i.start_time < @p2
  GROUP BY p.query_id)
SELECT q.query_id, q.query_hash, ISNULL(q.object_id, 0), q.context_settings_id,
  CONVERT(bigint, cs.set_options), cs.date_first, cs.date_format,
  LEFT(qt.query_sql_text, 4000),
  a.execs, a.cpu_ms, a.reads, a.duration_ms, ISNULL(a.avg_rows, 0)
FROM a
JOIN sys.query_store_query q ON q.query_id = a.query_id
JOIN sys.query_store_query_text qt ON qt.query_text_id = q.query_text_id
JOIN sys.query_context_settings cs ON cs.context_settings_id = q.context_settings_id
WHERE qt.query_sql_text NOT LIKE N'%godta:collect%'
ORDER BY q.query_id;`

func queryStoreQueries(ctx context.Context, db *sql.DB, opt Options) ([]model.Query, error) {
	rows, err := db.QueryContext(ctx, queryStoreAggregate, opt.Window.From.UTC(), opt.Window.Until.UTC())
	if err != nil {
		return nil, fmt.Errorf("reading the query store: %w", err)
	}
	defer rows.Close()
	var out []model.Query
	for rows.Next() {
		var q model.Query
		var hash []byte
		if err := rows.Scan(&q.QueryID, &hash, &q.ObjectID, &q.ContextSettingsID,
			&q.SetOptions, &q.DateFirst, &q.DateFormat, &q.Text,
			&q.Executions, &q.CPUms, &q.Reads, &q.Duration, &q.AvgRowcount); err != nil {
			return nil, fmt.Errorf("reading the query store: %w", err)
		}
		q.Hash = hexHash(hash)
		if statementKind(q.Text) == "" {
			continue
		}
		q.IsWrite = isWriteText(q.Text)
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the query store: %w", err)
	}
	return out, nil
}

// detailFunc fills the full text and the plan of the queries kept.
type detailFunc func(ctx context.Context, db *sql.DB, qs []*model.Query) error

// splitAndDetail keeps the top N reads and every write, then reads their
// text and plan.
func splitAndDetail(ctx context.Context, db *sql.DB, w *model.Workload, all []model.Query, topN int, detail detailFunc) error {
	var reads []model.Query
	for _, q := range all {
		if q.IsWrite {
			w.Writes = append(w.Writes, q)
		} else {
			reads = append(reads, q)
		}
	}
	w.Queries = dedupTopN(reads, topN)
	var ptrs []*model.Query
	for i := range w.Queries {
		ptrs = append(ptrs, &w.Queries[i])
	}
	for i := range w.Writes {
		ptrs = append(ptrs, &w.Writes[i])
	}
	return detail(ctx, db, ptrs)
}

// dedupTopN keeps the top n by executions, CPU, reads and duration, the
// union of the four, each query once, sorted by QueryID. Ties go to the
// lower QueryID.
func dedupTopN(qs []model.Query, n int) []model.Query {
	keys := []func(model.Query) float64{
		func(q model.Query) float64 { return float64(q.Executions) },
		func(q model.Query) float64 { return q.CPUms },
		func(q model.Query) float64 { return q.Reads },
		func(q model.Query) float64 { return q.Duration },
	}
	seen := map[int64]bool{}
	var out []model.Query
	for _, key := range keys {
		ranked := slices.Clone(qs)
		slices.SortStableFunc(ranked, func(a, b model.Query) int {
			if c := cmp.Compare(key(b), key(a)); c != 0 {
				return c
			}
			return cmp.Compare(a.QueryID, b.QueryID)
		})
		for _, q := range ranked[:min(n, len(ranked))] {
			if !seen[q.QueryID] {
				seen[q.QueryID] = true
				out = append(out, q)
			}
		}
	}
	slices.SortFunc(out, func(a, b model.Query) int { return cmp.Compare(a.QueryID, b.QueryID) })
	return out
}

// idList writes integers as a comma-separated list for an IN clause. They
// are formatted from Go integers, never from text.
func idList[T int | int64](ids []T) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(int64(id), 10)
	}
	return strings.Join(parts, ",")
}

// queryStoreDetail reads the full text and the last plan of each query.
func queryStoreDetail(ctx context.Context, db *sql.DB, qs []*model.Query) error {
	if len(qs) == 0 {
		return nil
	}
	byID := map[int64]*model.Query{}
	var ids []int64
	for _, q := range qs {
		byID[q.QueryID] = q
		ids = append(ids, q.QueryID)
	}
	rows, err := db.QueryContext(ctx, `SELECT /* godta:collect */ q.query_id, qt.query_sql_text, ISNULL(lp.query_plan, N'')
FROM sys.query_store_query q
JOIN sys.query_store_query_text qt ON qt.query_text_id = q.query_text_id
OUTER APPLY (SELECT TOP (1) p.query_plan FROM sys.query_store_plan p
  WHERE p.query_id = q.query_id ORDER BY p.last_execution_time DESC, p.plan_id DESC) lp
WHERE q.query_id IN (`+idList(ids)+`);`)
	if err != nil {
		return fmt.Errorf("reading the query store plans: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var text, plan string
		if err := rows.Scan(&id, &text, &plan); err != nil {
			return fmt.Errorf("reading the query store plans: %w", err)
		}
		if q := byID[id]; q != nil {
			q.Text, q.PlanXML = text, plan
		}
	}
	return rows.Err()
}

// planCacheAggregate groups the cached statements of the connection's
// database by query_hash. The database of a plan is its dbid attribute:
// sys.dm_exec_sql_text gives NULL for an ad hoc batch. One statement per
// hash, the last executed, gives the text and the plan.
const planCacheAggregate = `WITH /* godta:collect */ s AS (
  SELECT qs.query_hash, qs.sql_handle, qs.plan_handle, qs.statement_start_offset, qs.statement_end_offset,
    qs.execution_count, qs.total_worker_time, qs.total_logical_reads, qs.total_elapsed_time, qs.total_rows,
    pa.set_options, pa.date_first, pa.date_format,
    ROW_NUMBER() OVER (PARTITION BY qs.query_hash ORDER BY qs.last_execution_time DESC, qs.plan_handle, qs.statement_start_offset) AS rn
  FROM sys.dm_exec_query_stats qs
  CROSS APPLY (SELECT
      MAX(CASE WHEN attribute = N'dbid' THEN CONVERT(int, value) END) AS dbid,
      MAX(CASE WHEN attribute = N'set_options' THEN CONVERT(bigint, value) END) AS set_options,
      MAX(CASE WHEN attribute = N'date_first' THEN CONVERT(int, value) END) AS date_first,
      MAX(CASE WHEN attribute = N'date_format' THEN CONVERT(int, value) END) AS date_format
    FROM sys.dm_exec_plan_attributes(qs.plan_handle)) pa
  WHERE pa.dbid = DB_ID())
SELECT s.query_hash,
  SUM(s.execution_count), SUM(s.total_worker_time) / 1000.0, SUM(s.total_logical_reads) * 1.0,
  SUM(s.total_elapsed_time) / 1000.0, SUM(s.total_rows) * 1.0 / NULLIF(SUM(s.execution_count), 0),
  MAX(CASE WHEN s.rn = 1 THEN s.sql_handle END), MAX(CASE WHEN s.rn = 1 THEN s.plan_handle END),
  MAX(CASE WHEN s.rn = 1 THEN s.statement_start_offset END), MAX(CASE WHEN s.rn = 1 THEN s.statement_end_offset END),
  ISNULL(MAX(CASE WHEN s.rn = 1 THEN s.set_options END), 0),
  ISNULL(MAX(CASE WHEN s.rn = 1 THEN s.date_first END), 0),
  ISNULL(MAX(CASE WHEN s.rn = 1 THEN s.date_format END), 0)
FROM s
GROUP BY s.query_hash;`

// cached is what planCacheDetail needs to read a statement back.
type cached struct {
	sqlHandle, planHandle []byte
	start, end            int
}

func planCacheQueries(ctx context.Context, db *sql.DB) ([]model.Query, map[int64]cached, error) {
	rows, err := db.QueryContext(ctx, planCacheAggregate)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the plan cache: %w", err)
	}
	var out []model.Query
	var handles []cached
	for rows.Next() {
		var q model.Query
		var hash []byte
		var c cached
		var avg sql.NullFloat64
		if err := rows.Scan(&hash, &q.Executions, &q.CPUms, &q.Reads, &q.Duration, &avg,
			&c.sqlHandle, &c.planHandle, &c.start, &c.end, &q.SetOptions, &q.DateFirst, &q.DateFormat); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("reading the plan cache: %w", err)
		}
		q.AvgRowcount = avg.Float64
		q.Hash = hexHash(hash)
		if len(hash) == 8 {
			q.QueryID = int64(binary.BigEndian.Uint64(hash))
		}
		out = append(out, q)
		handles = append(handles, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, fmt.Errorf("reading the plan cache: %w", err)
	}
	rows.Close()
	kept := map[int64]cached{}
	// The text tells a write from a read; it is read for every hash, the
	// plan only for the queries kept.
	keep := out[:0]
	for i := range out {
		var batch sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT /* godta:collect */ (SELECT text FROM sys.dm_exec_sql_text(@p1));`,
			handles[i].sqlHandle).Scan(&batch); err != nil {
			return nil, nil, fmt.Errorf("reading a cached statement: %w", err)
		}
		out[i].Text = statementText(batch.String, handles[i].start, handles[i].end)
		if statementKind(out[i].Text) == "" || strings.Contains(batch.String, marker) {
			continue
		}
		out[i].IsWrite = isWriteText(out[i].Text)
		kept[out[i].QueryID] = handles[i]
		keep = append(keep, out[i])
	}
	return keep, kept, nil
}

// planCacheDetail reads the statement plan of each query kept, and the
// Query Store query_ids of the same hash when the Query Store still has
// them.
func planCacheDetail(ctx context.Context, db *sql.DB, qs []*model.Query, handles map[int64]cached) error {
	for _, q := range qs {
		c := handles[q.QueryID]
		var plan sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT /* godta:collect */ (SELECT query_plan FROM sys.dm_exec_text_query_plan(@p1, @p2, @p3));`,
			c.planHandle, c.start, c.end).Scan(&plan); err != nil {
			return fmt.Errorf("reading a cached plan: %w", err)
		}
		q.PlanXML = plan.String
		q.QueryIDs = queryIDsOfHash(ctx, db, q.QueryID)
	}
	return nil
}

// queryIDsOfHash lists the Query Store query_ids of a hash; none when the
// Query Store cannot be read.
func queryIDsOfHash(ctx context.Context, db *sql.DB, hash int64) []int64 {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(hash))
	rows, err := db.QueryContext(ctx, `SELECT /* godta:collect */ query_id FROM sys.query_store_query
WHERE query_hash = CONVERT(binary(8), @p1) ORDER BY query_id;`, b)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func hexHash(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return fmt.Sprintf("0x%X", b)
}

// statementText cuts the statement out of its batch. The offsets count
// bytes of UTF-16, the end is -1 for the end of the batch. A parameterized
// batch starts with its declaration, (@a int,@b int): it is kept in front of
// the statement, as the Query Store writes it.
func statementText(batch string, start, end int) string {
	u := utf16.Encode([]rune(batch))
	from, to := start/2, len(u)
	if end >= 0 {
		to = end/2 + 1
	}
	if from < 0 || from > len(u) || to > len(u) || to < from {
		return ""
	}
	stmt := string(utf16.Decode(u[from:to]))
	if decl := paramDecl(batch); decl != "" && !strings.HasPrefix(stmt, decl) {
		stmt = decl + stmt
	}
	return stmt
}

// paramDecl returns the leading parameter declaration of a batch, (@...),
// or "".
func paramDecl(batch string) string {
	if !strings.HasPrefix(batch, "(@") {
		return ""
	}
	toks := sqlq.Lex(sqlq.Sanitize(batch))
	rs := []rune(batch)
	for _, t := range toks {
		if t.Text == ")" && t.Depth == 0 {
			return string(rs[:t.End])
		}
	}
	return ""
}

var dmlKeywords = map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true}

// statementKind returns SELECT, INSERT, UPDATE, DELETE or MERGE for a query
// statement, "" for anything else (the CREATE INDEX of an index build, which
// the Query Store captures too). The statement starts, after a leading
// parameter declaration, with one of these keywords or with the WITH of its
// CTEs; its kind is the first of them outside parentheses. Comments,
// strings and quoted names are ignored.
func statementKind(text string) string {
	toks := sqlq.Lex(sqlq.Sanitize(text))
	i := 0
	if len(toks) > 1 && toks[0].Text == "(" && strings.HasPrefix(toks[1].Text, "@") {
		for i < len(toks) && !(toks[i].Text == ")" && toks[i].Depth == 0) {
			i++
		}
		i++
	}
	// A statement may open with parentheses: (SELECT ...) UNION ...
	for i < len(toks) && toks[i].Text == "(" {
		i++
	}
	if i >= len(toks) {
		return ""
	}
	first := strings.ToUpper(toks[i].Text)
	if dmlKeywords[first] {
		return first
	}
	if first != "WITH" {
		return ""
	}
	for ; i < len(toks); i++ {
		if kw := strings.ToUpper(toks[i].Text); toks[i].Depth == 0 && dmlKeywords[kw] {
			return kw
		}
	}
	return ""
}

// isWriteText tells whether a statement is an INSERT, UPDATE, DELETE or
// MERGE.
func isWriteText(text string) bool {
	k := statementKind(text)
	return k != "" && k != "SELECT"
}

// ParsePlans parses again the plans of a workload read back from
// collect.json, where Plan is not written. A plan that does not parse leaves
// Plan nil, with a warning.
func ParsePlans(w *model.Workload) { parsePlans(w) }

// parsePlans parses the plan of every query; a plan that does not parse is
// a warning and leaves Plan nil.
func parsePlans(w *model.Workload) {
	for _, list := range [][]model.Query{w.Queries, w.Writes} {
		for i := range list {
			q := &list[i]
			if q.PlanXML == "" {
				continue
			}
			p, err := showplan.Parse([]byte(q.PlanXML))
			if err != nil {
				w.Warnings = append(w.Warnings, fmt.Sprintf("query %d: plan not read: %v", q.QueryID, err))
				continue
			}
			q.Plan = p
		}
	}
}
