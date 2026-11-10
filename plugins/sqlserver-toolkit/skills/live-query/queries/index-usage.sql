/*  How each index of a table is used and what it costs to maintain.

    Definition, size, reads and writes since the counters started, and the
    physical work behind them (rows inserted and deleted, page allocations,
    latch and I/O waits).

    Parameter: @name - the table, schema-qualified ('dbo.Orders') or bare
               (resolved in the login's default schema), or '*' for every
               user table of the database (the 200 largest indexes).

    Read-only. Run it with -database <db> -maxrows 200. One row per index,
    heap included (index_id 0, index_name NULL), hypothetical indexes
    excluded. A name that does not resolve raises error 50000 instead of
    returning an empty result, which would read as "no index".

    READ THIS BEFORE TRUSTING THE RESULT.

    What the counters cover.
      - user_seeks .. user_updates (sys.dm_db_index_usage_stats) count since
        the database's counters were last emptied: instance restart, restore,
        database offline, AUTO_CLOSE (is_auto_close_on), availability group
        failover. instance_uptime_days is an UPPER BOUND on that window. For
        an index created since, they start at its creation: read
        stats_last_updated, which is the build date until the statistics are
        next updated.
      - leaf_inserts .. row_lock_wait_ms (sys.dm_db_index_operational_stats)
        live in the metadata cache: they also start again when the index's
        cache entry is evicted, without any restart. Compare them with each
        other on one run, never with the usage counters of an earlier run.
      - database_in_ag = 1: everything here belongs to THIS replica. An index
        read only by reports on a readable secondary shows 0 reads here.

    Two traps, both met on production.
      - user_updates counts STATEMENTS, not rows. 232 updates can be 1.4
        million rows inserted and deleted; 16,000 updates can be 16,000
        single-row inserts. Read leaf_inserts and leaf_deletes for the rows,
        and reads_per_update only as an order of magnitude.
      - leaf_inserts includes the build: a new index starts with one insert
        per row of the table. Subtract row_count before reading it as
        activity.

    The other columns.
      - user_scans also counts partial scans (a range read through a TOP, a
        MIN or MAX, an ordered read that stops early). A scan on a narrow
        index is not a full read of it: check the logical reads of the
        queries before concluding.
      - leaf_allocations counts leaf page allocations, which Microsoft
        documents as page splits. Appends at the end of an ascending key were
        not counted: 0 for 204 million inserts into a clustered index on an
        ascending key (SQL Server 2019). A high figure therefore means rows
        landing in the middle of the index.
      - page_latch_wait_ms is contention in memory (hot pages, last-page
        inserts); page_io_latch_wait_ms is waiting for pages from disk. Both
        are totals: divide by the count columns of the same DMV when the
        question is the average.
      - key_columns and included_columns are the DECLARED columns. Every
        nonclustered index also carries the clustered key (read it on the
        clustered row of the same table).
      - filter_definition is NULL for an unfiltered index AND when the login
        may not read it: trust has_filter.
      - usage_not_tracked = 1 (memory-optimized table): the usage columns are
        NULL, which means unknown, not unused.

    THIS RESULT ALONE DOES NOT JUSTIFY DROPPING AN INDEX. Zero reads over a
    short window, or over a window without a month end, prove nothing; an
    index may also back a constraint (is_unique, is_primary_key,
    is_unique_constraint) or a foreign key, or be named in a hint.

    Permissions. sys.dm_db_index_usage_stats and sys.dm_os_sys_info: VIEW
    SERVER STATE, or VIEW SERVER PERFORMANCE STATE from SQL Server 2022.
    sys.dm_db_index_operational_stats and sys.dm_db_partition_stats: VIEW
    DATABASE STATE, or VIEW DATABASE PERFORMANCE STATE from 2022.

    Written for SQL Server 2012 and later. Tested on SQL Server 2019 only.

    Dates are returned as text without a time zone, in the server's local
    time: sqlq renders a datetime with a false Z suffix.
*/
DECLARE @all bit = CASE WHEN @name = N'*' THEN 1 ELSE 0 END;
DECLARE @oid int = CASE WHEN @name = N'*' THEN NULL ELSE OBJECT_ID(@name) END;

IF @all = 0 AND (@oid IS NULL OR OBJECTPROPERTY(@oid, 'IsUserTable') = 0)
BEGIN
    RAISERROR(N'No user table named %s in this database: check the schema, or pass -database.', 16, 1, @name);
    RETURN;
END;

WITH
ix AS (
    SELECT i.object_id, i.index_id, i.name, i.type_desc, i.is_unique,
           i.is_primary_key, i.is_unique_constraint, i.is_disabled,
           i.has_filter, i.filter_definition
    FROM sys.indexes AS i
    JOIN sys.tables AS t
      ON t.object_id = i.object_id
    WHERE i.is_hypothetical = 0
      AND t.is_ms_shipped = 0
      AND (@all = 1 OR i.object_id = @oid)
),
size AS (
    -- Aggregated per index BEFORE the join: one row per partition would
    -- otherwise duplicate the index row.
    SELECT ps.object_id, ps.index_id,
           SUM(ps.row_count)       AS row_count,
           SUM(ps.used_page_count) AS used_pages
    FROM sys.dm_db_partition_stats AS ps
    WHERE @all = 1 OR ps.object_id = @oid
    GROUP BY ps.object_id, ps.index_id
),
comp AS (
    SELECT p.object_id, p.index_id,
           MIN(p.data_compression_desc) AS min_compression,
           MAX(p.data_compression_desc) AS max_compression
    FROM sys.partitions AS p
    WHERE @all = 1 OR p.object_id = @oid
    GROUP BY p.object_id, p.index_id
),
ops AS (
    -- One call for the table, or one for the whole database: never one call
    -- per index. Partitions are summed.
    SELECT o.object_id, o.index_id,
           SUM(o.leaf_insert_count)                       AS leaf_inserts,
           SUM(o.leaf_delete_count + o.leaf_ghost_count)  AS leaf_deletes,
           SUM(o.leaf_update_count)                       AS leaf_updates,
           SUM(o.leaf_allocation_count)                   AS leaf_allocations,
           SUM(o.range_scan_count)                        AS range_scans,
           SUM(o.singleton_lookup_count)                  AS singleton_lookups,
           SUM(o.page_latch_wait_in_ms)                   AS page_latch_wait_ms,
           SUM(o.page_io_latch_wait_in_ms)                AS page_io_latch_wait_ms,
           SUM(o.row_lock_wait_in_ms)                     AS row_lock_wait_ms
    FROM sys.dm_db_index_operational_stats(DB_ID(), @oid, NULL, NULL) AS o
    GROUP BY o.object_id, o.index_id
)
SELECT TOP (200)
    SCHEMA_NAME(t.schema_id)                AS schema_name,
    t.name                                  AS table_name,
    ix.name                                 AS index_name,
    ix.index_id,
    ix.type_desc                            AS index_type,
    CAST(ix.is_unique AS bit)               AS is_unique,
    CAST(ix.is_primary_key AS bit)          AS is_primary_key,
    CAST(ix.is_unique_constraint AS bit)    AS is_unique_constraint,
    CAST(ix.is_disabled AS bit)             AS is_disabled,
    CAST(ix.has_filter AS bit)              AS has_filter,
    ix.filter_definition,
    CAST(STUFF((
        SELECT N', ' + QUOTENAME(c.name)
               + CASE WHEN xc.is_descending_key = 1 THEN N' DESC' ELSE N'' END
        FROM sys.index_columns AS xc
        JOIN sys.columns AS c
          ON c.object_id = xc.object_id
         AND c.column_id = xc.column_id
        WHERE xc.object_id = ix.object_id
          AND xc.index_id = ix.index_id
          AND xc.key_ordinal > 0
        ORDER BY xc.key_ordinal
        FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'') AS nvarchar(max))
                                            AS key_columns,
    CAST(STUFF((
        SELECT N', ' + QUOTENAME(c.name)
        FROM sys.index_columns AS xc
        JOIN sys.columns AS c
          ON c.object_id = xc.object_id
         AND c.column_id = xc.column_id
        WHERE xc.object_id = ix.object_id
          AND xc.index_id = ix.index_id
          AND xc.is_included_column = 1
        ORDER BY xc.index_column_id
        FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'') AS nvarchar(max))
                                            AS included_columns,
    CASE WHEN cp.min_compression = cp.max_compression THEN cp.min_compression
         ELSE N'MIXED' END                  AS data_compression,
    sz.row_count,
    CAST(sz.used_pages * 8 / 1024.0 AS decimal(18, 1)) AS used_mb,
    CAST(CASE WHEN t.is_memory_optimized = 1 THEN NULL ELSE COALESCE(us.user_seeks, 0) END AS bigint)   AS user_seeks,
    CAST(CASE WHEN t.is_memory_optimized = 1 THEN NULL ELSE COALESCE(us.user_scans, 0) END AS bigint)   AS user_scans,
    CAST(CASE WHEN t.is_memory_optimized = 1 THEN NULL ELSE COALESCE(us.user_lookups, 0) END AS bigint) AS user_lookups,
    CAST(CASE WHEN t.is_memory_optimized = 1 THEN NULL ELSE COALESCE(us.user_updates, 0) END AS bigint) AS user_updates,
    CAST(CAST(us.user_seeks + us.user_scans + us.user_lookups AS float)
         / NULLIF(us.user_updates, 0) AS decimal(18, 2)) AS reads_per_update,
    CONVERT(varchar(19), us.last_user_seek, 126)   AS last_user_seek,
    CONVERT(varchar(19), us.last_user_scan, 126)   AS last_user_scan,
    CONVERT(varchar(19), us.last_user_lookup, 126) AS last_user_lookup,
    CONVERT(varchar(19), us.last_user_update, 126) AS last_user_update,
    op.leaf_inserts,
    op.leaf_deletes,
    op.leaf_updates,
    op.leaf_allocations,
    op.range_scans,
    op.singleton_lookups,
    op.page_latch_wait_ms,
    op.page_io_latch_wait_ms,
    op.row_lock_wait_ms,
    CONVERT(varchar(19), STATS_DATE(ix.object_id, ix.index_id), 126) AS stats_last_updated,
    CONVERT(varchar(19), si.sqlserver_start_time, 126)               AS instance_start_time,
    CAST(CAST(DATEDIFF(second, si.sqlserver_start_time, GETDATE()) AS float) / 86400.0
         AS decimal(9, 2))                  AS instance_uptime_days,
    CAST(db.is_auto_close_on AS bit)        AS is_auto_close_on,
    CAST(CASE WHEN db.replica_id IS NULL THEN 0 ELSE 1 END AS bit) AS database_in_ag,
    CAST(t.is_memory_optimized AS bit)      AS usage_not_tracked
FROM ix
JOIN sys.tables AS t
  ON t.object_id = ix.object_id
LEFT JOIN size AS sz
  ON sz.object_id = ix.object_id AND sz.index_id = ix.index_id
LEFT JOIN comp AS cp
  ON cp.object_id = ix.object_id AND cp.index_id = ix.index_id
LEFT JOIN ops AS op
  ON op.object_id = ix.object_id AND op.index_id = ix.index_id
LEFT JOIN sys.dm_db_index_usage_stats AS us
  ON us.database_id = DB_ID() AND us.object_id = ix.object_id AND us.index_id = ix.index_id
CROSS JOIN sys.dm_os_sys_info AS si
CROSS JOIN sys.databases AS db
WHERE db.database_id = DB_ID()
ORDER BY CASE WHEN @all = 1 THEN sz.used_pages END DESC,
         t.name, ix.index_id;
