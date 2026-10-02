## Verified

**`observed_days` overstates the observation window for attached or restored databases:** The design calculates `observed_days` using `sqlserver_start_time` (the instance start time). However, Microsoft documentation establishes that usage stats and missing index suggestions are cleared when a database is set offline, detached, or restored. If a database is brought online after the instance started, `observed_days` will incorrectly reflect the instance uptime rather than the database's online time. This silent defect would lead an agent to confidently recommend dropping a 0-usage index because the observation window appears deceptively long.

**The reading protocol in §6 gives flawed widening advice:** The protocol instructs the agent to "find an existing index whose key starts with the same columns and widen it." Microsoft documentation explicitly states that the order of columns in the `equality_columns` list returned by the DMV is not significant. Because any equality column can be the leading edge of an index without penalty, an agent looking for a strict prefix match will wrongly ignore existing indexes that start with the second or third equality column, leading to redundant index creation.

**`sqlq` renders `DATETIME` with a false `Z`:** Verified in the `go-mssqldb` driver and `tools/internal/sqlq/result.go`. The driver parses SQL Server's timezone-less `DATETIME` as UTC `time.Time`, and `sqlq` formats it using `time.RFC3339Nano`, which unconditionally appends the `Z` timezone marker. The design's workaround to convert dates to ISO string (`varchar(19)`) in the query correctly avoids this bug.

## Concluded by reasoning

**Duplicate suggestions for the same `index_handle`:** A single missing index (`index_handle`) can be associated with multiple query compilations, creating multiple groups (`index_group_handle` in `sys.dm_db_missing_index_groups`). If the query joins `details` to `groups` and `group_stats` without aggregating by `index_handle`, a table will output multiple identical `missing` rows that the agent cannot distinguish. Conversely, if it aggregates them by `index_handle`, a simple `SUM` of `avg_total_user_cost` and `avg_user_impact` is mathematically invalid because they are averages.

**Partitioned tables duplicating `existing` index rows:** The design prescribes obtaining `used_mb` by summing `used_page_count` across partitions. If this is done via a direct `LEFT JOIN` to `sys.dm_db_partition_stats` alongside `sys.indexes` without a pre-aggregated subquery or a strict `GROUP BY` per index, each partition of an index will multiply the `existing` row in the output.

## Not a problem

- The write guard does not refuse the query; `Sanitize()` replaces all comments with spaces before tokenization, rendering `ALTER INDEX` invisible, and `user_updates` does not match the `UPDATE` keyword.
- `collection_capped` correctly detects the limit because the documented cap is exactly 600 groups.
- `suggestions_on_hidden_objects` is computable; a login without `VIEW DEFINITION` cannot see the object in `sys.objects`, but the DMV still provides the `object_id`.
- `OBJECTPROPERTY 'TableIsMemoryOptimized'` compiles safely on SQL Server 2012, returning `NULL` instead of a compilation failure.
- The `ORDER BY` is deterministic and total, correctly using primary keys (`object_id`, `index_handle`, `index_id`) to break ties.
- Table write intensity is supported, as `user_updates` is available to the agent via the `existing` row for the heap or clustered index.
- Floating point score calculation avoids the arithmetic overflows that can occur with `decimal` multiplication.
- Column types like float score, bit, and nvarchar(max) from XML do not come out in a misleading form; they marshal cleanly to JSON primitives in `sqlq`.
