// restore.go has the preparation of the lab (spec section 8.4, steps 7 and
// 8): the restore of the clone's backup on the lab, the switch to
// READ_WRITE, the automatic statistics turned off, the database scoped
// configurations of the source, and the multicolumn statistics read as
// streams then dropped from the lab. StatsHook puts each stream back on the
// hypothetical index of the same keys at evaluation time (section 8.5,
// step 2).
package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/candidate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/evaluate"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/workspace"
)

// NoteStatsNotTransferred is the note of every Measure of a candidate of
// several keys whose statistics are not on the lab: a suggestion that came
// after the clone (spec section 8.4).
const NoteStatsNotTransferred = "unreliable estimate: statistics not transferred; evaluate on the server or run clone again"

// StreamKey is the key of a multicolumn statistics stream in
// Prepared.Streams: the table's object_id on the source and the column_ids
// of the keys in order. The sort direction is ignored, since statistics have
// none. It is what StatsHook finds from a candidate, which does not know the
// name of the godta_* statistics.
func StreamKey(table int, keys []model.KeyCol) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(table))
	for i, k := range keys {
		if i == 0 {
			b.WriteByte(':')
		} else {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(k.ColumnID))
	}
	return b.String()
}

// Restore restores the clone's backup f on the lab as database db and
// prepares it, in the order of spec section 8.4, steps 7 and 8:
//
//  1. CheckDistinct and CheckSameVersion of src and labi, labi being checked
//     against what server identifies itself as;
//  2. RESTORE FILELISTONLY of the file as the lab sees it (containerPath, the
//     directory where the lab container mounts the host directory of
//     f.HostPath), then RESTORE ... WITH MOVE of each logical file to dataDir
//     (the lab's InstanceDefaultDataPath when empty);
//  3. CheckCloneDB on the restored database;
//  4. SET READ_WRITE (without it, creating a hypothetical index fails with
//     error 3906);
//  5. AUTO_CREATE_STATISTICS OFF and AUTO_UPDATE_STATISTICS OFF, before any
//     compilation: the clone carries the modification counters, and an
//     automatic update would recompute a copied statistics from the empty
//     table;
//  6. the database scoped configurations of w, MAXDOP replaced by the
//     source's effective MAXDOP (EffectiveMaxDOP), with a warning when the
//     lab has fewer processors;
//  7. for each multicolumn godta_* statistics of the clone, its stream is
//     read into Prepared.Streams, State{f, Prepared} is saved in dir, then
//     the statistics is dropped from the lab.
//
// server is the lab's connection on master; open opens a pool on the
// restored database, for steps 6 and 7, which Restore closes before it
// returns. Nothing is sent before the guards of step 1 pass, and db must not
// exist on the lab. Any failure after the RESTORE drops the restored
// database (and saves State{f} again if Prepared was already saved).
func Restore(ctx context.Context, ws workspace.WS, dir string, server *sql.DB, open func(db string) (*sql.DB, error),
	src, labi conn.Instance, w *model.Workload, f File, containerPath, dataDir, db string) (Prepared, error) {
	p := Prepared{Database: db}
	if !tsql.IsRunName(db) {
		return Prepared{}, fmt.Errorf("restore: database %q is not named like a goDTA object", db)
	}
	if err := CheckDistinct(src, labi); err != nil {
		return Prepared{}, fmt.Errorf("restore: %w", err)
	}
	warning, err := CheckSameVersion(src, labi)
	if err != nil {
		return Prepared{}, fmt.Errorf("restore: %w", err)
	}
	if warning != "" {
		p.Warnings = append(p.Warnings, warning)
	}
	// labi is what the guards compared: it must be the instance server
	// reaches, or the guards prove nothing.
	reached, err := conn.Identify(ctx, server)
	if err != nil {
		return Prepared{}, fmt.Errorf("restore: %w", err)
	}
	if !strings.EqualFold(reached.ServerName, labi.ServerName) || !strings.EqualFold(reached.MachineName, labi.MachineName) {
		return Prepared{}, fmt.Errorf("restore: the lab connection reaches %s on %s, not the lab instance %s on %s",
			reached.ServerName, reached.MachineName, labi.ServerName, labi.MachineName)
	}
	var exists int
	if err := server.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.databases WHERE name = @p1", db).Scan(&exists); err != nil {
		return Prepared{}, fmt.Errorf("restore: %w", err)
	}
	if exists != 0 {
		return Prepared{}, fmt.Errorf("restore: database %q already exists on the lab", db)
	}

	// Step 2.
	path := joinServerPath(containerPath, filepath.Base(f.HostPath))
	files, err := fileList(ctx, server, path)
	if err != nil {
		return Prepared{}, fmt.Errorf("restore: %s: %w", tsql.RestoreFileList(path), err)
	}
	if dataDir == "" {
		if err := server.QueryRowContext(ctx, "SELECT CAST(SERVERPROPERTY('InstanceDefaultDataPath') AS nvarchar(4000))").Scan(&dataDir); err != nil {
			return Prepared{}, fmt.Errorf("restore: reading the lab's data directory: %w", err)
		}
	}
	moves := map[string]string{}
	for _, fl := range files {
		moves[fl.logical] = joinServerPath(dataDir, fmt.Sprintf("%s_f%d%s", db, fl.id, fl.ext()))
	}
	if _, err := server.ExecContext(ctx, tsql.RestoreDatabase(db, path, moves)); err != nil {
		// A failed RESTORE can leave the database in RESTORING.
		return Prepared{}, errors.Join(fmt.Errorf("restore: RESTORE of %s: %w", path, err), dropIfThere(server, db))
	}

	var pool *sql.DB
	saved := false
	fail := func(err error) (Prepared, error) {
		if pool != nil {
			pool.Close()
		}
		errs := []error{fmt.Errorf("restore: %w", err), dropIfThere(server, db)}
		if saved {
			errs = append(errs, SaveState(ws, dir, State{File: f}))
		}
		return Prepared{}, errors.Join(errs...)
	}

	// Step 3.
	if err := CheckCloneDB(ctx, server, db); err != nil {
		return fail(err)
	}
	// Steps 4 and 5, before anything compiles in the database.
	for _, stmt := range append([]string{tsql.SetReadWrite(db)}, tsql.SetAutoStatsOff(db)...) {
		if _, err := server.ExecContext(ctx, stmt); err != nil {
			return fail(fmt.Errorf("%s: %w", stmt, err))
		}
	}
	if pool, err = open(db); err != nil {
		return fail(fmt.Errorf("opening %s on the lab: %w", db, err))
	}
	defer func() {
		if pool != nil {
			pool.Close()
		}
	}()

	// Step 6.
	maxdop := EffectiveMaxDOP(w.ScopedConfigs, src)
	for _, c := range scopedConfigs(w.ScopedConfigs, maxdop) {
		stmt, err := setScopedConfig(c)
		if err != nil {
			return fail(err)
		}
		if _, err := pool.ExecContext(ctx, stmt); err != nil {
			return fail(fmt.Errorf("%s: %w", stmt, err))
		}
	}
	if labi.CPUCount < maxdop {
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"the lab has %d processors, fewer than the source's effective MAXDOP %d: parallel plans may differ", labi.CPUCount, maxdop))
	}

	// Step 7.
	stats, err := multiStats(ctx, pool)
	if err != nil {
		return fail(err)
	}
	p.Streams = map[string][]byte{}
	for _, s := range stats {
		var stream []byte
		var rows, pages sql.NullInt64
		if err := pool.QueryRowContext(ctx, tsql.ShowStatisticsStream(s.obj, s.name)).Scan(&stream, &rows, &pages); err != nil {
			return fail(fmt.Errorf("%s: %w", tsql.ShowStatisticsStream(s.obj, s.name), err))
		}
		key, ok := sourceKey(w, s)
		if !ok {
			p.Warnings = append(p.Warnings, fmt.Sprintf("statistics %s.%s: table or column not in the workload, stream not kept",
				s.obj.Quoted(), tsql.QuoteName(s.name)))
			continue
		}
		p.Streams[key] = stream
	}
	if err := SaveState(ws, dir, State{File: f, Prepared: &p}); err != nil {
		return fail(err)
	}
	saved = true
	for _, s := range stats {
		stmt := tsql.DropStatistics(s.obj, s.name)
		if _, err := pool.ExecContext(ctx, stmt); err != nil {
			return fail(fmt.Errorf("%s: %w", stmt, err))
		}
	}
	return p, nil
}

// EffectiveMaxDOP is the source's effective MAXDOP (spec section 8.4, step
// 7): the database's MAXDOP when not 0, else the instance's when not 0, else
// every processor; bounded by the source's processor count.
func EffectiveMaxDOP(configs []tsql.ScopedConfig, src conn.Instance) int {
	v := 0
	for _, c := range configs {
		if strings.EqualFold(c.Name, "MAXDOP") {
			v, _ = strconv.Atoi(c.Value)
		}
	}
	if v <= 0 {
		v = src.MaxDOP
	}
	if v <= 0 || (src.CPUCount > 0 && v > src.CPUCount) {
		v = src.CPUCount
	}
	return v
}

// scopedConfigs is configs with MAXDOP set to maxdop, added when absent.
func scopedConfigs(configs []tsql.ScopedConfig, maxdop int) []tsql.ScopedConfig {
	out := make([]tsql.ScopedConfig, 0, len(configs)+1)
	set := false
	for _, c := range configs {
		if strings.EqualFold(c.Name, "MAXDOP") {
			c = tsql.ScopedConfig{Name: "MAXDOP", Value: strconv.Itoa(maxdop), BaseType: "int"}
			set = true
		}
		out = append(out, c)
	}
	if !set && maxdop > 0 {
		out = append(out, tsql.ScopedConfig{Name: "MAXDOP", Value: strconv.Itoa(maxdop), BaseType: "int"})
	}
	return out
}

// setScopedConfig is tsql.SetScopedConfig, its panic on a value it cannot
// write turned into an error.
func setScopedConfig(c tsql.ScopedConfig) (stmt string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("scoped configuration %s: %v", c.Name, r)
		}
	}()
	return tsql.SetScopedConfig(c), nil
}

// joinServerPath joins a directory of the server and a file name with the
// directory's own separator (a backslash for a Windows path, a slash
// otherwise).
func joinServerPath(dir, name string) string {
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	return strings.TrimRight(dir, `/\`) + sep + name
}

// backupFile is one row of RESTORE FILELISTONLY.
type backupFile struct {
	logical string
	typ     string
	id      int64
}

// ext is the extension of the file once restored: .mdf for the primary data
// file, .ndf for another data file, .ldf for a log, none otherwise (a
// FILESTREAM container is a directory).
func (b backupFile) ext() string {
	switch {
	case b.typ == "L":
		return ".ldf"
	case b.typ == "D" && b.id == 1:
		return ".mdf"
	case b.typ == "D":
		return ".ndf"
	}
	return ""
}

// fileList reads the logical files of the backup at path. The columns of
// RESTORE FILELISTONLY change between versions: they are found by name.
func fileList(ctx context.Context, server *sql.DB, path string) ([]backupFile, error) {
	rows, err := server.QueryContext(ctx, tsql.RestoreFileList(path))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, c := range cols {
		idx[strings.ToLower(c)] = i
	}
	for _, c := range []string{"logicalname", "type", "fileid"} {
		if _, ok := idx[c]; !ok {
			return nil, fmt.Errorf("no column %s in the result", c)
		}
	}
	var out []backupFile
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		var b backupFile
		b.logical = text(vals[idx["logicalname"]])
		b.typ = strings.TrimSpace(text(vals[idx["type"]]))
		b.id, _ = strconv.ParseInt(text(vals[idx["fileid"]]), 10, 64)
		if b.logical == "" {
			return nil, errors.New("a file without a logical name")
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no file in the backup")
	}
	return out, nil
}

func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// dropIfThere drops db on the lab when it exists, whatever its state. It
// runs even when the caller's context is done.
func dropIfThere(server *sql.DB, db string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var state sql.NullString
	err := server.QueryRowContext(ctx, "SELECT state_desc FROM sys.databases WHERE name = @p1", db).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restore: looking for %s to drop it: %w", db, err)
	}
	q := tsql.QuoteName(db)
	stmt := tsql.DropDatabase(db)
	if state.String == "ONLINE" {
		stmt = "ALTER DATABASE " + q + " SET SINGLE_USER WITH ROLLBACK IMMEDIATE; " + stmt
	}
	if _, err := server.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("restore: dropping %s from the lab: %w", db, err)
	}
	return nil
}

// labStat is one multicolumn godta_* statistics of the restored clone.
type labStat struct {
	obj     tsql.Obj
	name    string
	columns []string // in order
}

// multiStatsQuery lists the user-created statistics of goDTA's run form with
// more than one column, their columns in order. An index's statistics is
// never user-created, so a hypothetical index the clone copied is left out.
const multiStatsQuery = `SELECT OBJECT_SCHEMA_NAME(s.object_id), OBJECT_NAME(s.object_id), s.name, c.name
FROM sys.stats AS s
JOIN sys.stats_columns AS sc ON sc.object_id = s.object_id AND sc.stats_id = s.stats_id
JOIN sys.columns AS c ON c.object_id = sc.object_id AND c.column_id = sc.column_id
WHERE s.user_created = 1 AND s.name LIKE @p1
  AND (SELECT COUNT(*) FROM sys.stats_columns AS n WHERE n.object_id = s.object_id AND n.stats_id = s.stats_id) > 1
ORDER BY s.object_id, s.stats_id, sc.stats_column_id`

func multiStats(ctx context.Context, pool *sql.DB) ([]labStat, error) {
	rows, err := pool.QueryContext(ctx, multiStatsQuery, tsql.RunPattern())
	if err != nil {
		return nil, fmt.Errorf("listing the multicolumn statistics: %w", err)
	}
	defer rows.Close()
	var out []labStat
	for rows.Next() {
		var schema, table, name, col string
		if err := rows.Scan(&schema, &table, &name, &col); err != nil {
			return nil, err
		}
		obj := tsql.Obj{Schema: schema, Name: table}
		if n := len(out); n == 0 || out[n-1].obj != obj || out[n-1].name != name {
			out = append(out, labStat{obj: obj, name: name})
		}
		last := &out[len(out)-1]
		last.columns = append(last.columns, col)
	}
	return out, rows.Err()
}

// sourceKey is the StreamKey of s in the source's terms: its table and
// columns found by name in w, so that the key does not depend on the clone
// keeping the object and column IDs.
func sourceKey(w *model.Workload, s labStat) (string, bool) {
	t := candidate.TableByName(w, s.obj.Schema, s.obj.Name)
	if t == nil {
		return "", false
	}
	keys := make([]model.KeyCol, len(s.columns))
	for i, c := range s.columns {
		id := candidate.ColumnByName(w, t, c)
		if id == 0 {
			return "", false
		}
		keys[i] = model.KeyCol{ColumnID: id}
	}
	return StreamKey(t.ObjectID, keys), true
}

// StatsHook is the lab's evaluate.Hooks (spec section 8.5, step 2): once the
// hypothetical index of candidate c exists, the stream of its keys is set
// on it with UPDATE STATISTICS ... WITH STATS_STREAM, sent through s.Exec.
// A candidate of one key has no stream and needs none (its single-column
// godta_* statistics stayed on the lab). A candidate of several keys without
// a stream (a suggestion that came after the clone) gets nothing sent and
// the note NoteStatsNotTransferred.
func StatsHook(p Prepared, w *model.Workload) evaluate.Hooks {
	return evaluate.Hooks{
		AfterCreateHypothetical: func(ctx context.Context, s *evaluate.Session, c model.Candidate, indexName string) ([]string, error) {
			stream, ok := p.Streams[StreamKey(c.Table, c.Keys)]
			if !ok {
				if len(c.Keys) > 1 {
					return []string{NoteStatsNotTransferred}, nil
				}
				return nil, nil
			}
			t := w.Tables[c.Table]
			if t == nil {
				return nil, fmt.Errorf("lab: table %d of candidate %s not in the workload", c.Table, c.ID)
			}
			if err := s.Exec(ctx, tsql.UpdateStatisticsStream(tsql.Obj{Schema: t.Schema, Name: t.Name}, indexName, stream)); err != nil {
				return nil, fmt.Errorf("lab: statistics stream on %s: %w", indexName, err)
			}
			return nil, nil
		},
	}
}
