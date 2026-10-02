/*  Missing indexes in the current database, beside the indexes that already
    exist on the same tables and how much they are used.

    No parameter. Read-only. Run it with -database <db> -maxrows 70.

    READ THIS BEFORE TRUSTING THE RESULT.

    One result set, three kinds of row, grouped by table:
      context   - exactly one, always first (table_rank 0). Whether any
                  conclusion is possible at all.
      missing   - what the optimizer asked for: at most 8 per table, best
                  score first.
      existing  - what the table already has, heap or clustered included:
                  at most 15 per table, by index_id.
    Only the 3 tables with the highest SUM of suggestion scores are shown.
    The sum is a ranking device, not a gain: near-identical suggestions each
    count. Every table row carries table_suggestion_count and
    table_index_count, so you know what is not shown. The result can never
    exceed 70 rows, so -maxrows 70 never cuts a table block.

    Flags are bit columns: sqlq returns them as JSON true / false. "= 1"
    below means true.

    This is not a snapshot. The views are read more than once while the
    query runs; if the context counts and the rows disagree (a table total
    smaller than the rows shown), something changed during the read: run it
    again.

    Read the context row first.
      - instance_uptime_days < 1, or collection_capped = 1: no conclusion is
        possible, in either direction. Say so and stop.
      - instance_uptime_days is an UPPER BOUND on what the counters cover. A
        restore, taking the database offline, AUTO_CLOSE (is_auto_close_on)
        or an availability group failover empties this database's counters
        and suggestions without moving the instance start time.
      - collection_capped: Microsoft documents a cap of 600 suggestion
        groups for the whole instance, after which nothing more is
        collected. Older versions are reported to cap at 500, which the
        documentation does not confirm, so the flag trips at 500. A 0 does
        not prove nothing was lost: a cap reached, then freed by later
        clean-ups, leaves no trace.
      - suggestions_on_unresolved_objects > 0: suggestions on objects this
        login cannot see, or that no longer exist. Not shown, not ranked.
      - suggestions_on_system_objects: suggestions on is_ms_shipped objects
        (msdb tables, cdc.*, replication tables) or on objects that are not
        user tables. Excluded on purpose.
      - database_in_ag = 1: counters and suggestions belong to THIS replica.
        An index used only by reports on a readable secondary shows 0 reads
        here.

    What the absence of a suggestion does not prove. Suggestions for a table
    are deleted when its metadata changes (a column added or dropped, an
    index created) and when ALTER INDEX runs on any of its indexes - nightly
    index maintenance included, so a table rebuilt every night is
    under-represented in the ranking. Observed on SQL Server 2022: REBUILD
    cleared them, REORGANIZE left them in place; Microsoft documents
    ALTER INDEX as a whole. None are made for trivial plans, and
    an eager index spool suppresses the request it stands for.

    What a suggestion is not. The order of equality_columns means nothing:
    compare them as a set. included_columns carries no size analysis.
    avg_user_impact is an estimate of an improvement to an estimate. The
    feature never suggests a unique, filtered, clustered or columnstore
    index. For a memory-optimized table (memory_optimized = 1), ignore
    included_columns: every column is in every memory-optimized index.

    What the existing rows do not say.
      - key_columns and included_columns are the DECLARED columns. Every
        nonclustered index also carries the clustered key (read it on the
        clustered row of the same table), and an undeclared partitioning
        column appears in neither list.
      - filter_definition is NULL for an unfiltered index AND when the login
        may not read it: trust has_filter.
      - user_seeks .. user_updates count operations since the database's
        counters were last emptied (see above), on this replica only. An
        index with no usage row shows 0. usage_not_tracked = 1 (spatial
        index, memory-optimized table) shows NULL: unknown, not unused. A
        rebuild is reported to reset these counters on some 2012 and 2014
        builds; Microsoft does not document it.

    THIS RESULT DOES NOT JUSTIFY DROPPING ANY INDEX.

    No DDL column, on purpose: never paste a suggestion as CREATE INDEX.
    Widen an existing index where one fits; a nonclustered unique index or
    primary key is widened through INCLUDE only, and a clustered index is
    never widened. The reading protocol is
    references/missing-index-reading.md.

    Excluded on purpose: is_ms_shipped objects, hypothetical indexes
    (counted in hypothetical_indexes), other databases.

    Permissions. Missing-index DMVs and sys.dm_db_index_usage_stats: VIEW
    SERVER STATE, or VIEW SERVER PERFORMANCE STATE from SQL Server 2022.
    sys.dm_db_partition_stats: VIEW DATABASE STATE and VIEW DEFINITION on the
    database, or VIEW DATABASE PERFORMANCE STATE and VIEW SECURITY DEFINITION
    from 2022. What happens without them is recorded in
    docs/validation/2026-10-01-missing-indexes.md.

    Written for SQL Server 2012 and later and Azure SQL Managed Instance.
    Tested on SQL Server 2022 only. Azure SQL Database: not tested.

    Dates are returned as text without a time zone, in the server's local
    time: sqlq renders a datetime with a false Z suffix.
*/
WITH
suggestion AS (
    -- Every suggestion of the current database. The grain is the group:
    -- nothing documents that an index_handle belongs to one group only.
    SELECT
        g.index_group_handle,
        d.index_handle,
        d.object_id,
        d.equality_columns,
        d.inequality_columns,
        d.included_columns,
        s.user_seeks,
        s.user_scans,
        s.last_user_seek,
        s.last_user_scan,
        s.avg_user_impact,
        s.unique_compiles,
        CAST(s.avg_total_user_cost AS float)
            * CAST(s.avg_user_impact AS float)
            * CAST(s.user_seeks + s.user_scans AS float) AS score,
        CASE
            WHEN o.object_id IS NULL THEN 'unresolved'
            WHEN o.is_ms_shipped = 1 OR o.type <> 'U' THEN 'system'
            ELSE 'user'
        END AS resolution
    FROM sys.dm_db_missing_index_details AS d
    JOIN sys.dm_db_missing_index_groups AS g
      ON g.index_handle = d.index_handle
    JOIN sys.dm_db_missing_index_group_stats AS s
      ON s.group_handle = g.index_group_handle
    LEFT JOIN sys.objects AS o
      ON o.object_id = d.object_id
    WHERE d.database_id = DB_ID()
),
suggested_table AS (
    -- Ranking and totals over ALL suggestions, before any cap.
    SELECT
        object_id,
        SUM(score) AS table_score,
        COUNT(*)   AS table_suggestion_count
    FROM suggestion
    WHERE resolution = 'user'
    GROUP BY object_id
),
ranked AS (
    SELECT TOP (3)
        object_id,
        table_suggestion_count,
        ROW_NUMBER() OVER (ORDER BY table_score DESC, object_id) AS table_rank
    FROM suggested_table
    ORDER BY table_score DESC, object_id
),
index_count AS (
    SELECT i.object_id, COUNT(*) AS table_index_count
    FROM sys.indexes AS i
    JOIN ranked AS r
      ON r.object_id = i.object_id
    WHERE i.is_hypothetical = 0
    GROUP BY i.object_id
),
missing AS (
    SELECT
        sg.*,
        ROW_NUMBER() OVER (PARTITION BY sg.object_id
                           ORDER BY sg.score DESC, sg.index_group_handle, sg.index_handle)
            AS miss_seq
    FROM suggestion AS sg
    JOIN ranked AS r
      ON r.object_id = sg.object_id
),
existing AS (
    SELECT
        i.object_id,
        i.index_id,
        i.name,
        i.type,
        i.type_desc,
        i.is_unique,
        i.is_primary_key,
        i.is_disabled,
        i.has_filter,
        i.filter_definition,
        ROW_NUMBER() OVER (PARTITION BY i.object_id ORDER BY i.index_id) AS idx_seq
    FROM sys.indexes AS i
    JOIN ranked AS r
      ON r.object_id = i.object_id
    WHERE i.is_hypothetical = 0
),
index_size AS (
    -- Aggregated per index BEFORE the join: one row per partition would
    -- otherwise duplicate the index row.
    SELECT ps.object_id, ps.index_id, SUM(ps.used_page_count) AS used_pages
    FROM sys.dm_db_partition_stats AS ps
    JOIN ranked AS r
      ON r.object_id = ps.object_id
    GROUP BY ps.object_id, ps.index_id
),
context_counts AS (
    SELECT
        (SELECT COUNT(*) FROM sys.dm_db_missing_index_groups)              AS groups_on_instance,
        (SELECT COUNT(*) FROM suggested_table)                             AS tables_with_suggestions,
        (SELECT COUNT(*) FROM suggestion WHERE resolution = 'system')      AS on_system,
        (SELECT COUNT(*) FROM suggestion WHERE resolution = 'unresolved')  AS on_unresolved,
        (SELECT COUNT(*) FROM sys.indexes WHERE is_hypothetical = 1)       AS hypothetical
)
SELECT TOP (70)
    row_kind, table_rank, schema_name, table_name,
    table_suggestion_count, table_index_count, memory_optimized,
    index_name, index_type, is_unique, is_primary_key, is_disabled, has_filter,
    filter_definition, key_columns,
    equality_columns, inequality_columns, included_columns,
    user_seeks, user_scans, user_lookups, user_updates, last_used,
    used_mb, usage_not_tracked,
    avg_user_impact, score, unique_compiles,
    instance_start_time, instance_uptime_days, is_auto_close_on, database_in_ag,
    suggestion_groups_on_instance, collection_capped, tables_with_suggestions,
    suggestions_on_system_objects, suggestions_on_unresolved_objects,
    hypothetical_indexes
FROM (
    SELECT
        CAST('context' AS varchar(8))       AS row_kind,
        CAST(0 AS int)                      AS table_rank,
        CAST(NULL AS sysname)               AS schema_name,
        CAST(NULL AS sysname)               AS table_name,
        CAST(NULL AS int)                   AS table_suggestion_count,
        CAST(NULL AS int)                   AS table_index_count,
        CAST(NULL AS bit)                   AS memory_optimized,
        CAST(NULL AS sysname)               AS index_name,
        CAST(NULL AS nvarchar(60))          AS index_type,
        CAST(NULL AS bit)                   AS is_unique,
        CAST(NULL AS bit)                   AS is_primary_key,
        CAST(NULL AS bit)                   AS is_disabled,
        CAST(NULL AS bit)                   AS has_filter,
        CAST(NULL AS nvarchar(max))         AS filter_definition,
        CAST(NULL AS nvarchar(max))         AS key_columns,
        CAST(NULL AS nvarchar(4000))        AS equality_columns,
        CAST(NULL AS nvarchar(4000))        AS inequality_columns,
        CAST(NULL AS nvarchar(max))         AS included_columns,
        CAST(NULL AS bigint)                AS user_seeks,
        CAST(NULL AS bigint)                AS user_scans,
        CAST(NULL AS bigint)                AS user_lookups,
        CAST(NULL AS bigint)                AS user_updates,
        CAST(NULL AS varchar(19))           AS last_used,
        CAST(NULL AS float)                 AS used_mb,
        CAST(NULL AS bit)                   AS usage_not_tracked,
        CAST(NULL AS float)                 AS avg_user_impact,
        CAST(NULL AS float)                 AS score,
        CAST(NULL AS bigint)                AS unique_compiles,
        CONVERT(varchar(19), si.sqlserver_start_time, 126)
                                            AS instance_start_time,
        CAST(DATEDIFF(second, si.sqlserver_start_time, GETDATE()) AS float) / 86400.0
                                            AS instance_uptime_days,
        CAST(db.is_auto_close_on AS bit)    AS is_auto_close_on,
        CAST(CASE WHEN db.replica_id IS NULL THEN 0 ELSE 1 END AS bit)
                                            AS database_in_ag,
        CAST(cc.groups_on_instance AS int)  AS suggestion_groups_on_instance,
        CAST(CASE WHEN cc.groups_on_instance >= 500 THEN 1 ELSE 0 END AS bit)
                                            AS collection_capped,
        CAST(cc.tables_with_suggestions AS int) AS tables_with_suggestions,
        CAST(cc.on_system AS int)           AS suggestions_on_system_objects,
        CAST(cc.on_unresolved AS int)       AS suggestions_on_unresolved_objects,
        CAST(cc.hypothetical AS int)        AS hypothetical_indexes,
        0                                   AS kind_order,
        CAST(0 AS bigint)                   AS seq
    FROM sys.dm_os_sys_info AS si
    CROSS JOIN context_counts AS cc
    CROSS JOIN sys.databases AS db
    WHERE db.database_id = DB_ID()

    UNION ALL

    SELECT
        CAST('missing' AS varchar(8)),
        CAST(r.table_rank AS int),
        CAST(sch.name AS sysname),
        CAST(o.name AS sysname),
        CAST(r.table_suggestion_count AS int),
        CAST(ic.table_index_count AS int),
        CAST(CASE WHEN OBJECTPROPERTY(m.object_id, 'TableIsMemoryOptimized') = 1
                  THEN 1 ELSE 0 END AS bit),
        CAST(NULL AS sysname),
        CAST(NULL AS nvarchar(60)),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS nvarchar(max)),
        CAST(NULL AS nvarchar(max)),
        CAST(m.equality_columns AS nvarchar(4000)),
        CAST(m.inequality_columns AS nvarchar(4000)),
        CAST(m.included_columns AS nvarchar(max)),
        CAST(m.user_seeks AS bigint),
        CAST(m.user_scans AS bigint),
        CAST(NULL AS bigint),
        CAST(NULL AS bigint),
        CONVERT(varchar(19),
                CASE WHEN m.last_user_seek >= m.last_user_scan OR m.last_user_scan IS NULL
                     THEN m.last_user_seek ELSE m.last_user_scan END, 126),
        CAST(NULL AS float),
        CAST(NULL AS bit),
        CAST(m.avg_user_impact AS float),
        m.score,
        CAST(m.unique_compiles AS bigint),
        CAST(NULL AS varchar(19)),
        CAST(NULL AS float),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        1,
        CAST(m.miss_seq AS bigint)
    FROM missing AS m
    JOIN ranked AS r
      ON r.object_id = m.object_id
    JOIN index_count AS ic
      ON ic.object_id = m.object_id
    JOIN sys.objects AS o
      ON o.object_id = m.object_id
    JOIN sys.schemas AS sch
      ON sch.schema_id = o.schema_id
    WHERE m.miss_seq <= 8

    UNION ALL

    SELECT
        CAST('existing' AS varchar(8)),
        CAST(r.table_rank AS int),
        CAST(sch.name AS sysname),
        CAST(o.name AS sysname),
        CAST(r.table_suggestion_count AS int),
        CAST(ic.table_index_count AS int),
        CAST(f.memory_optimized AS bit),
        CAST(e.name AS sysname),
        CAST(e.type_desc AS nvarchar(60)),
        CAST(e.is_unique AS bit),
        CAST(e.is_primary_key AS bit),
        CAST(e.is_disabled AS bit),
        CAST(e.has_filter AS bit),
        CAST(e.filter_definition AS nvarchar(max)),
        CAST(STUFF((
            SELECT N', ' + QUOTENAME(c.name)
                   + CASE WHEN xc.is_descending_key = 1 THEN N' DESC' ELSE N'' END
            FROM sys.index_columns AS xc
            JOIN sys.columns AS c
              ON c.object_id = xc.object_id
             AND c.column_id = xc.column_id
            WHERE xc.object_id = e.object_id
              AND xc.index_id = e.index_id
              AND xc.key_ordinal > 0
            ORDER BY xc.key_ordinal
            FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'') AS nvarchar(max)),
        CAST(NULL AS nvarchar(4000)),
        CAST(NULL AS nvarchar(4000)),
        CAST(STUFF((
            SELECT N', ' + QUOTENAME(c.name)
            FROM sys.index_columns AS xc
            JOIN sys.columns AS c
              ON c.object_id = xc.object_id
             AND c.column_id = xc.column_id
            WHERE xc.object_id = e.object_id
              AND xc.index_id = e.index_id
              AND xc.is_included_column = 1
            ORDER BY xc.index_column_id
            FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'') AS nvarchar(max)),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_seeks, 0) END AS bigint),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_scans, 0) END AS bigint),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_lookups, 0) END AS bigint),
        CAST(CASE WHEN f.untracked = 1 THEN NULL ELSE COALESCE(us.user_updates, 0) END AS bigint),
        CASE WHEN f.untracked = 1 THEN NULL ELSE
            CONVERT(varchar(19), (SELECT MAX(v)
                                  FROM (VALUES (us.last_user_seek),
                                               (us.last_user_scan),
                                               (us.last_user_lookup)) AS t(v)), 126)
        END,
        CASE WHEN f.memory_optimized = 1 THEN NULL
             ELSE ROUND(CAST(sz.used_pages AS float) * 8.0 / 1024.0, 2) END,
        CAST(f.untracked AS bit),
        CAST(NULL AS float),
        CAST(NULL AS float),
        CAST(NULL AS bigint),
        CAST(NULL AS varchar(19)),
        CAST(NULL AS float),
        CAST(NULL AS bit),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS bit),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        CAST(NULL AS int),
        2,
        CAST(e.index_id AS bigint)
    FROM existing AS e
    JOIN ranked AS r
      ON r.object_id = e.object_id
    JOIN index_count AS ic
      ON ic.object_id = e.object_id
    JOIN sys.objects AS o
      ON o.object_id = e.object_id
    JOIN sys.schemas AS sch
      ON sch.schema_id = o.schema_id
    CROSS APPLY (
        SELECT
            CASE WHEN OBJECTPROPERTY(e.object_id, 'TableIsMemoryOptimized') = 1
                 THEN 1 ELSE 0 END AS memory_optimized,
            CASE WHEN OBJECTPROPERTY(e.object_id, 'TableIsMemoryOptimized') = 1
                   OR e.type = 4
                 THEN 1 ELSE 0 END AS untracked
    ) AS f
    LEFT JOIN sys.dm_db_index_usage_stats AS us
      ON us.database_id = DB_ID()
     AND us.object_id = e.object_id
     AND us.index_id = e.index_id
    LEFT JOIN index_size AS sz
      ON sz.object_id = e.object_id
     AND sz.index_id = e.index_id
    WHERE e.idx_seq <= 15
) AS result
ORDER BY table_rank, kind_order, seq
OPTION (RECOMPILE, MAXDOP 1);
