package collect

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/showplan"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/tsql"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// The optimizer-related scoped configurations (spec section 8.2).
const scopedConfigsQuery = `SELECT /* godta:internal */ name, ISNULL(CAST(value AS nvarchar(4000)), N''),
  ISNULL(CAST(SQL_VARIANT_PROPERTY(value, 'BaseType') AS nvarchar(128)), N'')
FROM sys.database_scoped_configurations
WHERE name IN (N'MAXDOP', N'LEGACY_CARDINALITY_ESTIMATION', N'PARAMETER_SNIFFING', N'QUERY_OPTIMIZER_HOTFIXES')
ORDER BY name;`

// readCatalog reads everything about the tables the workload touches, the
// hypothetical indexes, the missing index groups, the scoped configurations
// and the named references to indexes.
func readCatalog(ctx context.Context, db *sql.DB, w *model.Workload, objects map[tableName]int) error {
	if err := readScopedConfigs(ctx, db, w); err != nil {
		return err
	}
	if err := readMissingIndexes(ctx, db, w); err != nil {
		return err
	}
	ids := touchedTables(w, objects)
	views, err := indexedViews(ctx, db, ids)
	if err != nil {
		return err
	}
	all := slices.Clone(ids)
	for _, vs := range views {
		all = append(all, vs...)
	}
	slices.Sort(all)
	all = slices.Compact(all)
	if err := readTables(ctx, db, w, all); err != nil {
		return err
	}
	for t, vs := range views {
		if tab := w.Tables[t]; tab != nil {
			tab.Views = vs
		}
	}
	if len(all) > 0 {
		cond := "i.object_id IN (" + idList(all) + ") AND i.is_hypothetical = 0"
		idx, err := readIndexes(ctx, db, cond)
		if err != nil {
			return err
		}
		vset := viewSet(views)
		for _, ix := range idx {
			if tab := w.Tables[ix.ObjectID]; tab != nil {
				if _, isView := vset[ix.ObjectID]; isView && ix.Kind == model.KindClustered {
					ix.Kind = model.KindViewClustered
				}
				tab.Indexes = append(tab.Indexes, ix)
			}
		}
		if err := readUsage(ctx, db, w, all); err != nil {
			return err
		}
		if err := readStats(ctx, db, w, all); err != nil {
			return err
		}
		for _, tab := range w.Tables {
			for _, ix := range tab.Indexes {
				switch ix.Kind {
				case model.KindClusteredColumnstore, model.KindNonclusteredColumnstore:
					tab.HasColumnstore = true
				case model.KindHeap:
					tab.IsHeap = true
				}
			}
		}
	}
	hyp, err := readIndexes(ctx, db, "i.is_hypothetical = 1")
	if err != nil {
		return err
	}
	w.Hypotheticals = hyp
	return readNamedRefs(ctx, db, w)
}

// ReadCatalog is the catalog half of Database, for a workload whose queries
// and database the caller filled: the tables their plans read or write, with
// their indexed views, indexes, statistics and columns. The integration tests
// of evaluate use it to describe a fresh database without compiling anything
// on it, which would create the statistics they need missing.
func ReadCatalog(ctx context.Context, db *sql.DB, w *model.Workload) error {
	if w.Tables == nil {
		w.Tables = map[int]*model.Table{}
	}
	objects, err := readObjects(ctx, db)
	if err != nil {
		return err
	}
	return readCatalog(ctx, db, w, objects)
}

func viewSet(views map[int][]int) map[int]struct{} {
	s := map[int]struct{}{}
	for _, vs := range views {
		for _, v := range vs {
			s[v] = struct{}{}
		}
	}
	return s
}

func readScopedConfigs(ctx context.Context, db *sql.DB, w *model.Workload) error {
	rows, err := db.QueryContext(ctx, scopedConfigsQuery)
	if err != nil {
		return fmt.Errorf("reading the scoped configurations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c tsql.ScopedConfig
		if err := rows.Scan(&c.Name, &c.Value, &c.BaseType); err != nil {
			return fmt.Errorf("reading the scoped configurations: %w", err)
		}
		w.ScopedConfigs = append(w.ScopedConfigs, c)
	}
	return rows.Err()
}

func readMissingIndexes(ctx context.Context, db *sql.DB, w *model.Workload) error {
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ d.index_handle, d.object_id, gs.avg_user_impact, gs.user_seeks,
  c.column_id, c.column_usage
FROM sys.dm_db_missing_index_details d
JOIN sys.dm_db_missing_index_groups g ON g.index_handle = d.index_handle
JOIN sys.dm_db_missing_index_group_stats gs ON gs.group_handle = g.index_group_handle
CROSS APPLY sys.dm_db_missing_index_columns(d.index_handle) c
WHERE d.database_id = DB_ID()
ORDER BY d.index_handle, c.column_id;`)
	if err != nil {
		return fmt.Errorf("reading the missing indexes: %w", err)
	}
	defer rows.Close()
	last := -1
	for rows.Next() {
		var handle, obj, col int
		var impact float64
		var seeks int64
		var usage string
		if err := rows.Scan(&handle, &obj, &impact, &seeks, &col, &usage); err != nil {
			return fmt.Errorf("reading the missing indexes: %w", err)
		}
		if handle != last {
			w.MissingIndexes = append(w.MissingIndexes, model.MissingIndexDMV{Table: obj, AvgImpact: impact, UserSeeks: seeks})
			last = handle
		}
		m := &w.MissingIndexes[len(w.MissingIndexes)-1]
		switch usage {
		case "EQUALITY":
			m.Equality = append(m.Equality, col)
		case "INEQUALITY":
			m.Inequality = append(m.Inequality, col)
		case "INCLUDE":
			m.Include = append(m.Include, col)
		}
	}
	return rows.Err()
}

// readObjects reads the tables and views of the database, by name. Schema
// and table names of a plan are the catalog's own spelling, so they are
// compared exactly, whatever the collation.
func readObjects(ctx context.Context, db *sql.DB) (map[tableName]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ o.object_id, s.name, o.name FROM sys.objects o
JOIN sys.schemas s ON s.schema_id = o.schema_id
WHERE o.type IN ('U', 'V');`)
	if err != nil {
		return nil, fmt.Errorf("reading the tables: %w", err)
	}
	defer rows.Close()
	out := map[tableName]int{}
	for rows.Next() {
		var id int
		var n tableName
		if err := rows.Scan(&id, &n.schema, &n.table); err != nil {
			return nil, fmt.Errorf("reading the tables: %w", err)
		}
		out[n] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the tables: %w", err)
	}
	return out, nil
}

// touchedTables resolves, in objects, the tables the plans read or write in
// this database, and adds those of the missing index groups. The database is
// this one by its current name or by a name its plans were compiled under
// (model.DBInfo.Matches).
func touchedTables(w *model.Workload, objects map[tableName]int) []int {
	set := map[int]bool{}
	for _, m := range w.MissingIndexes {
		set[m.Table] = true
	}
	for n := range planTables(w) {
		if id, ok := objects[n]; ok {
			set[id] = true
		}
	}
	ids := make([]int, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// tableName is a table as a plan names it.
type tableName struct{ schema, table string }

// planTables lists the tables of this database the plans of w read or
// write, by name.
func planTables(w *model.Workload) map[tableName]bool {
	wanted := map[tableName]bool{}
	add := func(o showplan.ObjRef) {
		if o.Table == "" || (o.Database != "" && !w.Database.Matches(o.Database)) {
			return
		}
		wanted[tableName{o.Schema, o.Table}] = true
	}
	for _, list := range [][]model.Query{w.Queries, w.Writes} {
		for _, q := range list {
			if q.Plan == nil {
				continue
			}
			for _, st := range q.Plan.Statements {
				for _, a := range st.Accesses {
					add(a.Obj)
					for _, b := range a.ViewBase {
						add(b)
					}
				}
				for _, wr := range st.Writes {
					for _, o := range wr.Objs {
						add(o)
					}
				}
			}
		}
	}
	return wanted
}

// indexedViews maps each table to the indexed views that reference it.
func indexedViews(ctx context.Context, db *sql.DB, ids []int) (map[int][]int, error) {
	out := map[int][]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ DISTINCT d.referenced_id, d.referencing_id
FROM sys.sql_expression_dependencies d
JOIN sys.views v ON v.object_id = d.referencing_id
JOIN sys.indexes i ON i.object_id = v.object_id AND i.index_id = 1
WHERE d.referenced_id IN (`+idList(ids)+`)
ORDER BY d.referenced_id, d.referencing_id;`)
	if err != nil {
		return nil, fmt.Errorf("reading the indexed views: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t, v int
		if err := rows.Scan(&t, &v); err != nil {
			return nil, fmt.Errorf("reading the indexed views: %w", err)
		}
		out[t] = append(out[t], v)
	}
	return out, rows.Err()
}

// readTables reads the tables (and indexed views) ids: names, rows, data
// pages and columns.
func readTables(ctx context.Context, db *sql.DB, w *model.Workload, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	in := idList(ids)
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ o.object_id, s.name, o.name, o.modify_date,
  ISNULL((SELECT SUM(ps.row_count) FROM sys.dm_db_partition_stats ps
          WHERE ps.object_id = o.object_id AND ps.index_id IN (0, 1)), 0),
  ISNULL((SELECT SUM(ps.in_row_data_page_count) FROM sys.dm_db_partition_stats ps
          WHERE ps.object_id = o.object_id AND ps.index_id IN (0, 1)), 0)
FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id
WHERE o.object_id IN (`+in+`);`)
	if err != nil {
		return fmt.Errorf("reading the tables: %w", err)
	}
	for rows.Next() {
		t := &model.Table{}
		if err := rows.Scan(&t.ObjectID, &t.Schema, &t.Name, &t.ModifyDate, &t.Rows, &t.DataPages); err != nil {
			rows.Close()
			return fmt.Errorf("reading the tables: %w", err)
		}
		w.Tables[t.ObjectID] = t
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("reading the tables: %w", err)
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT /* godta:internal */ c.object_id, c.column_id, c.name, ISNULL(TYPE_NAME(c.user_type_id), N''),
  c.max_length, c.is_nullable, c.is_computed
FROM sys.columns c WHERE c.object_id IN (`+in+`)
ORDER BY c.object_id, c.column_id;`)
	if err != nil {
		return fmt.Errorf("reading the columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var obj int
		var c model.Column
		if err := rows.Scan(&obj, &c.ID, &c.Name, &c.Type, &c.MaxLength, &c.Nullable, &c.Computed); err != nil {
			return fmt.Errorf("reading the columns: %w", err)
		}
		c.AvgWidth = float64(c.MaxLength)
		if c.MaxLength < 0 {
			// A (max) column: at most 8000 bytes in row.
			c.AvgWidth = 8000
		}
		if t := w.Tables[obj]; t != nil {
			t.Columns = append(t.Columns, c)
		}
	}
	return rows.Err()
}

// indexKind names an index type of sys.indexes.
func indexKind(typ int) string {
	switch typ {
	case 0:
		return model.KindHeap
	case 1:
		return model.KindClustered
	case 2:
		return model.KindNonclustered
	case 3:
		return "xml"
	case 4:
		return "spatial"
	case 5:
		return model.KindClusteredColumnstore
	case 6:
		return model.KindNonclusteredColumnstore
	case 7:
		return "nonclustered hash"
	}
	return "type " + strconv.Itoa(typ)
}

// readIndexes reads the indexes cond selects (a condition on sys.indexes i,
// built from integers only), with their keys, included columns and
// partitioning column.
func readIndexes(ctx context.Context, db *sql.DB, cond string) ([]model.Index, error) {
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ i.object_id, i.index_id, ISNULL(i.name, N''), i.type,
  i.is_unique, i.is_primary_key, i.is_unique_constraint, i.is_disabled, i.is_hypothetical,
  ISNULL(i.filter_definition, N''), i.fill_factor, ISNULL(ds.name, N''),
  ISNULL((SELECT SUM(ps.in_row_data_page_count) FROM sys.dm_db_partition_stats ps
          WHERE ps.object_id = i.object_id AND ps.index_id = i.index_id), 0),
  ISNULL((SELECT TOP (1) p.data_compression_desc FROM sys.partitions p
          WHERE p.object_id = i.object_id AND p.index_id = i.index_id ORDER BY p.partition_number), N'')
FROM sys.indexes i LEFT JOIN sys.data_spaces ds ON ds.data_space_id = i.data_space_id
JOIN sys.objects o ON o.object_id = i.object_id AND o.is_ms_shipped = 0
WHERE `+cond+`
ORDER BY i.object_id, i.index_id;`)
	if err != nil {
		return nil, fmt.Errorf("reading the indexes: %w", err)
	}
	var out []model.Index
	pos := map[[2]int]int{}
	for rows.Next() {
		var ix model.Index
		var typ int
		if err := rows.Scan(&ix.ObjectID, &ix.IndexID, &ix.Name, &typ, &ix.Unique, &ix.PrimaryKey,
			&ix.UniqueConstraint, &ix.Disabled, &ix.Hypothetical, &ix.Filter, &ix.FillFactor,
			&ix.DataSpace, &ix.DataPages, &ix.Compression); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading the indexes: %w", err)
		}
		ix.Kind = indexKind(typ)
		pos[[2]int{ix.ObjectID, ix.IndexID}] = len(out)
		out = append(out, ix)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("reading the indexes: %w", err)
	}
	rows.Close()
	if len(out) == 0 {
		return nil, nil
	}

	// The partitioning column of an index on a partition scheme
	// (partition_ordinal 1) comes as an extra row with key_ordinal -1: it may
	// also be a key or an included column, or neither.
	rows, err = db.QueryContext(ctx, `SELECT /* godta:internal */ u.object_id, u.index_id, u.column_id, u.key_ordinal,
  u.is_descending_key, u.is_included_column
FROM (
  SELECT ic.object_id, ic.index_id, ic.column_id, ic.key_ordinal, ic.is_descending_key, ic.is_included_column, ic.index_column_id
  FROM sys.index_columns ic JOIN sys.indexes i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
  WHERE `+cond+`
  UNION ALL
  SELECT ic.object_id, ic.index_id, ic.column_id, -1, CAST(0 AS bit), CAST(0 AS bit), 0
  FROM sys.index_columns ic JOIN sys.indexes i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
  WHERE (`+cond+`) AND ic.partition_ordinal = 1) u
ORDER BY u.object_id, u.index_id, u.key_ordinal, u.index_column_id;`)
	if err != nil {
		return nil, fmt.Errorf("reading the index columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var obj, idx, col, ord int
		var desc, incl bool
		if err := rows.Scan(&obj, &idx, &col, &ord, &desc, &incl); err != nil {
			return nil, fmt.Errorf("reading the index columns: %w", err)
		}
		p, ok := pos[[2]int{obj, idx}]
		if !ok {
			continue
		}
		switch {
		case ord < 0:
			out[p].PartitionColumn = col
		case ord > 0:
			out[p].Keys = append(out[p].Keys, model.KeyCol{ColumnID: col, Desc: desc})
		case incl:
			out[p].Include = append(out[p].Include, col)
		}
	}
	return out, rows.Err()
}

// readUsage reads the usage and operational counters of the indexes.
func readUsage(ctx context.Context, db *sql.DB, w *model.Workload, ids []int) error {
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ i.object_id, i.index_id,
  ISNULL(u.user_seeks, 0), ISNULL(u.user_scans, 0), ISNULL(u.user_lookups, 0), ISNULL(u.user_updates, 0),
  ISNULL(op.ins, 0), ISNULL(op.upd, 0), ISNULL(op.del, 0)
FROM sys.indexes i
LEFT JOIN sys.dm_db_index_usage_stats u
  ON u.database_id = DB_ID() AND u.object_id = i.object_id AND u.index_id = i.index_id
OUTER APPLY (SELECT SUM(s.leaf_insert_count) AS ins, SUM(s.leaf_update_count) AS upd, SUM(s.leaf_delete_count) AS del
  FROM sys.dm_db_index_operational_stats(DB_ID(), i.object_id, i.index_id, NULL) s) op
WHERE i.object_id IN (`+idList(ids)+`) AND i.is_hypothetical = 0;`)
	if err != nil {
		return fmt.Errorf("reading the index usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var obj, idx int
		var u model.IndexUsage
		if err := rows.Scan(&obj, &idx, &u.Seeks, &u.Scans, &u.Lookups, &u.Updates,
			&u.LeafInserts, &u.LeafUpdates, &u.LeafDeletes); err != nil {
			return fmt.Errorf("reading the index usage: %w", err)
		}
		if t := w.Tables[obj]; t != nil {
			for i := range t.Indexes {
				if t.Indexes[i].IndexID == idx {
					t.Indexes[i].Usage = &u
				}
			}
		}
	}
	return rows.Err()
}

// readStats reads the statistics of the tables, those of hypothetical
// indexes left out, then the average width of each column that leads an
// unfiltered statistics object, from its density vector.
func readStats(ctx context.Context, db *sql.DB, w *model.Workload, ids []int) error {
	in := idList(ids)
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ s.object_id, s.stats_id, s.name, s.auto_created, s.user_created,
  ISNULL(sp.rows, 0), ISNULL(sp.rows_sampled, 0), ISNULL(sp.modification_counter, 0), s.has_filter
FROM sys.stats s
OUTER APPLY sys.dm_db_stats_properties(s.object_id, s.stats_id) sp
WHERE s.object_id IN (`+in+`)
  AND NOT EXISTS (SELECT 1 FROM sys.indexes i
                  WHERE i.object_id = s.object_id AND i.index_id = s.stats_id AND i.is_hypothetical = 1)
ORDER BY s.object_id, s.stats_id;`)
	if err != nil {
		return fmt.Errorf("reading the statistics: %w", err)
	}
	type key [2]int
	pos := map[key]*model.Stat{}
	filtered := map[key]bool{}
	var order []key
	for rows.Next() {
		var obj int
		var st model.Stat
		var filter bool
		if err := rows.Scan(&obj, &st.StatsID, &st.Name, &st.AutoCreated, &st.UserCreated,
			&st.Rows, &st.RowsSampled, &st.ModificationCounter, &filter); err != nil {
			rows.Close()
			return fmt.Errorf("reading the statistics: %w", err)
		}
		if t := w.Tables[obj]; t != nil {
			t.Stats = append(t.Stats, st)
			order = append(order, key{obj, st.StatsID})
			filtered[key{obj, st.StatsID}] = filter
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("reading the statistics: %w", err)
	}
	rows.Close()
	for _, k := range order {
		t := w.Tables[k[0]]
		for i := range t.Stats {
			if t.Stats[i].StatsID == k[1] {
				pos[k] = &t.Stats[i]
			}
		}
	}

	rows, err = db.QueryContext(ctx, `SELECT /* godta:internal */ sc.object_id, sc.stats_id, sc.column_id
FROM sys.stats_columns sc WHERE sc.object_id IN (`+in+`)
ORDER BY sc.object_id, sc.stats_id, sc.stats_column_id;`)
	if err != nil {
		return fmt.Errorf("reading the statistics columns: %w", err)
	}
	for rows.Next() {
		var obj, sid, col int
		if err := rows.Scan(&obj, &sid, &col); err != nil {
			rows.Close()
			return fmt.Errorf("reading the statistics columns: %w", err)
		}
		if st := pos[key{obj, sid}]; st != nil {
			st.Columns = append(st.Columns, col)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("reading the statistics columns: %w", err)
	}
	rows.Close()

	for _, id := range ids {
		t := w.Tables[id]
		if t == nil {
			continue
		}
		done := map[int]bool{}
		for _, st := range t.Stats {
			// A filtered statistics object measures only its rows.
			if len(st.Columns) == 0 || st.Rows == 0 || done[st.Columns[0]] || filtered[key{id, st.StatsID}] {
				continue
			}
			width, ok := densityWidth(ctx, db, t, st.Name)
			if !ok {
				continue
			}
			done[st.Columns[0]] = true
			for i := range t.Columns {
				if t.Columns[i].ID == st.Columns[0] {
					t.Columns[i].AvgWidth = width
				}
			}
		}
	}
	return nil
}

// densityWidth reads the average length of the leading column of a
// statistics object: the first row of its density vector. A statistics
// object the command cannot read (a columnstore one, an empty one) gives
// nothing, and the column keeps max_length.
func densityWidth(ctx context.Context, db *sql.DB, t *model.Table, stat string) (float64, bool) {
	obj := tsql.Obj{Schema: t.Schema, Name: t.Name}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("DBCC SHOW_STATISTICS(%s, %s) WITH DENSITY_VECTOR, NO_INFOMSGS",
		tsql.QuoteString(obj.Quoted()), tsql.QuoteName(stat)))
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, false
	}
	var density, length sql.NullFloat64
	var cols sql.NullString
	if err := rows.Scan(&density, &length, &cols); err != nil || !length.Valid || length.Float64 <= 0 {
		return 0, false
	}
	return length.Float64, true
}

// readNamedRefs finds where indexes are named: INDEX hints in the Query
// Store texts, in the modules and in the plan guides, and the indexes the
// forced plans read. A name found in text is attached to every index of that
// name among the tables collected: the hint's table is not resolved, which
// errs on the side of keeping an index.
func readNamedRefs(ctx context.Context, db *sql.DB, w *model.Workload) error {
	ci := !caseSensitive(w.Database.Collation)
	attach := func(name, where string) {
		for _, t := range w.Tables {
			for i := range t.Indexes {
				ix := &t.Indexes[i]
				if ix.Name == name || (ci && strings.EqualFold(ix.Name, name)) {
					if !slices.Contains(ix.NamedIn, where) {
						ix.NamedIn = append(ix.NamedIn, where)
					}
				}
			}
		}
	}
	texts := func(query, what string, scan func(*sql.Rows) (text, where string, err error)) error {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("reading %s: %w", what, err)
		}
		defer rows.Close()
		for rows.Next() {
			text, where, err := scan(rows)
			if err != nil {
				return fmt.Errorf("reading %s: %w", what, err)
			}
			for _, n := range indexNamesInText(text) {
				attach(n, where)
			}
		}
		return rows.Err()
	}
	if w.Database.QueryStoreState != "OFF" {
		if err := texts(`SELECT /* godta:internal */ query_sql_text FROM sys.query_store_query_text
WHERE query_sql_text LIKE N'%INDEX%'
  AND query_sql_text NOT LIKE N'%`+tsql.Marker+`%' AND query_sql_text NOT LIKE N'%`+tsql.LegacyMarker+`%';`, "the query store texts",
			func(r *sql.Rows) (string, string, error) {
				var s string
				return s, "query_store_text", r.Scan(&s)
			}); err != nil {
			return err
		}
	}
	if err := texts(`SELECT /* godta:internal */ s.name, o.name, m.definition FROM sys.sql_modules m
JOIN sys.objects o ON o.object_id = m.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
WHERE m.definition LIKE N'%INDEX%';`, "the modules",
		func(r *sql.Rows) (string, string, error) {
			var sch, name string
			var def sql.NullString
			err := r.Scan(&sch, &name, &def)
			return def.String, "sql_module:" + sch + "." + name, err
		}); err != nil {
		return err
	}
	if err := texts(`SELECT /* godta:internal */ name, ISNULL(query_text, N'') + N' ' + ISNULL(hints, N'')
FROM sys.plan_guides;`, "the plan guides",
		func(r *sql.Rows) (string, string, error) {
			var name, text string
			err := r.Scan(&name, &text)
			return text, "plan_guide:" + name, err
		}); err != nil {
		return err
	}
	if w.Database.QueryStoreState == "OFF" {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT /* godta:internal */ query_id, query_plan FROM sys.query_store_plan
WHERE is_forced_plan = 1;`)
	if err != nil {
		return fmt.Errorf("reading the forced plans: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var plan sql.NullString
		if err := rows.Scan(&id, &plan); err != nil {
			return fmt.Errorf("reading the forced plans: %w", err)
		}
		p, err := showplan.Parse([]byte(plan.String))
		if err != nil {
			w.Warnings = append(w.Warnings, fmt.Sprintf("forced plan of query %d not read: %v", id, err))
			continue
		}
		where := "forced_plan:" + strconv.FormatInt(id, 10)
		for _, o := range planIndexes(p) {
			for _, t := range w.Tables {
				if t.Schema != o.Schema || t.Name != o.Table {
					continue
				}
				for i := range t.Indexes {
					if ix := &t.Indexes[i]; ix.Name == o.Index && !slices.Contains(ix.NamedIn, where) {
						ix.NamedIn = append(ix.NamedIn, where)
					}
				}
			}
		}
	}
	return rows.Err()
}

// planIndexes lists the indexes a plan reads or maintains.
func planIndexes(p *showplan.Plan) []showplan.ObjRef {
	var out []showplan.ObjRef
	for _, st := range p.Statements {
		for _, a := range st.Accesses {
			if a.Obj.Index != "" {
				out = append(out, a.Obj)
			}
		}
		for _, wr := range st.Writes {
			for _, o := range wr.Objs {
				if o.Index != "" {
					out = append(out, o)
				}
			}
		}
	}
	return out
}

// caseSensitive tells whether a collation compares names by case: _CS_ and
// the binary collations.
func caseSensitive(collation string) bool {
	c := strings.ToUpper(collation)
	return strings.Contains(c, "_CS") || strings.Contains(c, "_BIN")
}

// indexNamesInText returns the index names an INDEX hint cites in a text,
// INDEX(a, [b]) and INDEX = a, INDEX = (a), in order, each once. Comments
// and strings are ignored; numeric index ids are skipped.
func indexNamesInText(text string) []string {
	rs := []rune(text)
	toks := sqlq.Lex(sqlq.Sanitize(text))
	var out []string
	add := func(n string) {
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	for i, t := range toks {
		if !strings.EqualFold(t.Text, "INDEX") || i+1 >= len(toks) {
			continue
		}
		next := toks[i+1]
		switch next.Text {
		case "(":
			for _, n := range nameList(rs, next.End) {
				add(n)
			}
		case "=":
			p := skipSpace(rs, next.End)
			if p < len(rs) && rs[p] == '(' {
				for _, n := range nameList(rs, p+1) {
					add(n)
				}
				continue
			}
			if n, _ := readName(rs, p); !isNumber(n) {
				add(n)
			}
		}
	}
	return out
}

// nameList reads names separated by commas from p up to the closing
// parenthesis.
func nameList(rs []rune, p int) []string {
	var out []string
	for {
		p = skipSpace(rs, p)
		n, q := readName(rs, p)
		if q == p {
			return out
		}
		if !isNumber(n) {
			out = append(out, n)
		}
		p = skipSpace(rs, q)
		if p >= len(rs) || rs[p] != ',' {
			return out
		}
		p++
	}
}

func skipSpace(rs []rune, p int) int {
	for p < len(rs) && unicode.IsSpace(rs[p]) {
		p++
	}
	return p
}

// readName reads one identifier at p: [bracketed]] name], "quoted", or a
// plain word. It returns the name unquoted and where it ends.
func readName(rs []rune, p int) (string, int) {
	if p >= len(rs) {
		return "", p
	}
	if close, ok := map[rune]rune{'[': ']', '"': '"'}[rs[p]]; ok {
		var b strings.Builder
		for q := p + 1; q < len(rs); q++ {
			if rs[q] == close {
				if q+1 < len(rs) && rs[q+1] == close {
					b.WriteRune(close)
					q++
					continue
				}
				return b.String(), q + 1
			}
			b.WriteRune(rs[q])
		}
		return "", p
	}
	q := p
	for q < len(rs) && (unicode.IsLetter(rs[q]) || unicode.IsDigit(rs[q]) || strings.ContainsRune("_@#$", rs[q])) {
		q++
	}
	return string(rs[p:q]), q
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
