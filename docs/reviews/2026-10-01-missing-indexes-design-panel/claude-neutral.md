# Review: docs/superpowers/specs/2026-10-01-missing-indexes-design.md

Reviewer: Claude (neutral prompt). No database was touched. Scratch work: a copy of
`tools/internal/sqlq/guard.go` in `%TEMP%\mi-panel\claude-work\guardcheck\`, plus a
draft of the query (`draft.sql`) and a size estimate (`size.py`). Nothing in the repository
was modified.

Defects are ranked by how expensive they would be to find once the query is in use. Seven are
reported below. I set aside three more, listed at the end.

## Verified

**1. `observed_days` is measured from the wrong clock, and the gate that relies on it passes when it should block.**
The spec computes `observed_days` from `sqlserver_start_time` (§4) and makes "`observed_days` < 1 day" the
condition that forbids any conclusion (§5). The same spec (§3) and Microsoft's documentation both say the data
is erased by events that do not move `sqlserver_start_time`. From *Tune nonclustered indexes…*: "Missing index
suggestions in DMVs are cleared by events such as instance restarts, failovers, and setting a database
offline." From `sys.dm_db_index_usage_stats`: "whenever a database is detached or is shut down (for example,
because AUTO_CLOSE is set to ON), all rows associated with the database are removed."
Take a database that was restored, taken offline and brought back, failed over in an availability group an
hour ago, or one with `AUTO_CLOSE` that closed overnight. On an instance up for 90 days, the context row shows
`observed_days = 90.0`. The gate lets the agent conclude, and the skill's own safeguard, "do not recommend
dropping an index without citing `observed_days`", makes it cite the wrong number in support of the drop. This
is the most expensive defect here: the output is a confident, well-sourced "unused for 90 days" on an index
whose counters are a few hours old. The §5 header text ("since startup *or since the database came online*")
admits the problem, but the column cannot express it.
At minimum, the context row should carry what can be read cheaply: `sys.databases.is_auto_close_on`, and
whether the database is in an availability group together with its role. `observed_days` should also be
renamed or documented as an upper bound, not a measurement. M1 to M10 contain no case that would catch this.

**2. The `-maxrows 150` truncation drops the very rows the design exists to show, and the size budget is about three times what the design assumes.**
`sqlq` serialises every row as a `map[string]any` holding every column (`result.go`, `type Row = map[string]any`;
`scanAll` in `main.go` assigns each column name for every row). NULL columns are therefore not omitted. With the
32 columns of §4, I measured a context row at about 710 characters of compact JSON and a typical `missing` or
`existing` row at 790 to 810. The 40,000-character alert in §7 is crossed at about 50 rows. The "usually under
100 rows" case is already about 80,000 characters, and the 150 the skill asks for is about 120,000.
The 40,000-character check is run on the throwaway test database, whose handful of tables keeps it well under
the limit, so validation will pass while real ERP databases go over it.
Truncation is also worse than §7 says. Within each table `missing` comes before `existing` (§4, "Ordre"), so a
cut always removes the end of the list: first the existing indexes of the last table, then whole tables. Nothing
limits the number of suggestions per table, and the 600-group cap is the only bound. A wide table with 40
suggestions and 25 indexes, followed by other large tables, uses up 150 rows before table 4 or 5 lists a single
existing index. The agent then has suggestions with no existing indexes beside them, which is exactly the
"copy the suggestion" situation §1 sets out to prevent. The rule "the last table is incomplete" is false as
written: several tables can be missing entirely.

**3. Decimal values come out as JSON strings, which contradicts "the counters stay numeric".**
go-mssqldb v1.11.0 decodes DECIMAL/NUMERIC/MONEY to `[]byte` (`decodeDecimal` returns `[]byte`, `types.go:943`).
`normalise` in `main.go` turns any `[]byte` that is not a binary type into `string(value)`. The natural ways to
write the columns described in §4 are `ROUND(DATEDIFF(MINUTE, start, GETDATE()) / 1440.0, 1)` for "one
decimal", and `SUM(used_page_count) * 8 / 1024.0` for `used_mb`. Both produce `numeric`, so they reach the agent
as `"0.4"` and `"123.45"` (I checked the first expression in my draft). The §5 gate compares `observed_days` with
1.
§4 never states the SQL type of these columns, so V0 ("types of §4") has nothing to check them against. The spec
should name `float` (or `int` for MB) for `observed_days`, `used_mb` and `score`, and V0 should check the
`type` field of `columns`.

**4. Readable secondaries: the counters only describe the replica that is queried.**
Microsoft's AG guide says: "To monitor index usage activity on a secondary replica, query the user_seeks,
user_scans, and user_lookups columns of sys.dm_db_index_usage_stats". The counters are therefore per replica.
On a primary whose reports run on a readable secondary, a reporting index shows reads = 0 and a high
`user_updates`. Step 4 of the protocol (weigh `user_updates` against reads) and the "index at 0" reading in §5
both point towards dropping it, and the drop then replicates to the secondary that was using it. Missing-index
suggestions from the reporting workload are likewise only visible on the secondary.
Neither the header, the context row nor the protocol mentions replicas, even though the model this design copies
(`blocked-processes-check`) handles availability groups explicitly. Required fix: an AG/role indicator in the
context row, and a header line saying that a 0 counter on one replica says nothing about the others.

## Concluded by reasoning

**5. `suggestions_on_hidden_objects` mixes "excluded by design" with "invisible to the login" (hypothesis, partly sourced).**
§4 removes `is_ms_shipped = 1` objects in the same `sys.objects` join that decides whether an object is visible.
It defines the counter as suggestions "whose object is not visible or no longer exists", and §5 tells the agent
that a value above 0 means "the login cannot see everything". The obvious implementation counts the suggestions
that the filtered join leaves unmatched; that is how my draft wrote it without any effort.
Under that implementation, `is_ms_shipped` user tables push the counter up. Microsoft's CDC documentation lists
the `cdc` schema objects as `is_ms_shipped = 1` and puts the change tables there. Replication tables
(`MSmerge_*` and similar) are in the same situation, and cleanup jobs on those tables do produce suggestions.
In any database with CDC or replication, a login with full rights would report missing visibility. The opposite
reading is also possible. `tables_with_suggestions` has the same ambiguity: over all object_ids from the DMV, or
only the visible ones?
The spec should say that the counter includes only suggestions whose object_id is missing from `sys.objects`
(no type or `is_ms_shipped` filter). `is_ms_shipped` suggestions should get their own counter, or be stated as
excluded from every counter. M9 should be accompanied by a CDC table.

**6. In-memory tables: suggestions are carried as-is, with no indication (hypothesis on the consequence; the fact is documented).**
`sys.dm_db_missing_index_details` states: "For memory-optimized indexes (both hash and memory-optimized
nonclustered), ignore `included_columns`. All columns of the table are included in every memory-optimized
index." The `missing` rows return `included_columns` "as-is" and `usage_not_tracked` is NULL on them, so the only
way to tell that the table is in-memory is from its `existing` rows. Those rows can be truncated (defect 2).
Step 3 of the protocol, "widen an existing index", produces an `INCLUDE` that cannot exist on that kind of table.
Also, `used_mb` is presumably 0 rather than NULL for these tables (their storage is not in pages), which goes
against the spec's own rule "0 = never, NULL = unknown". I have not checked this against the documentation for
`sys.dm_db_partition_stats`. The flag should be carried on the `missing` rows, and `used_mb` should be NULL when
`usage_not_tracked = 1`.

**7. Ranking by the sum of scores rewards near-duplicate variants (hypothesis).**
The documentation says: "Missing index requests might offer similar variations of indexes on the same table and
column(s) across queries." If one query family produces five overlapping variants, the same potential gain is
counted five times. That table can push out a table with a single suggestion that is genuinely more costly. The
classification looks normal, so nobody will catch this. This is not a fatal flaw for an overview, but the header
should say so, or the ranking should use `MAX(score)` alongside the sum.

## Not a problem

- Single batch with no `GO`/`USE`/`EXEC`/`INTO`/`DECLARE`, CTEs, `FOR XML PATH(''), TYPE).value(...)`, a three-way `UNION ALL` with hidden sort columns, and `OPTION (RECOMPILE, MAXDOP 1)`. My draft passes `FindWrites`, `FindContextChanges` and `FindBatchSeparators` (one statement, zero refusals). `user_updates`, `usage_not_tracked` and `create_date` are single tokens for `tokens()`. The constraints in §4 can all hold at once.
- Score formula: identical to Microsoft's (`avg_total_user_cost * avg_user_impact * (user_seeks + user_scans)`), and `float` avoids the `decimal(28,1)` cast that the official example uses.
- The 600 cap: documented ("gathered for a maximum of 600 missing index groups… no more… data is gathered"; "result set… limited to 600 rows"). `>= 600` is the right test. (A cap reached and then released by a later purge leaves no trace; see set aside.)
- Clearing by `ALTER INDEX` and by metadata change: the passage is quoted correctly. The documentation says "an ALTER INDEX operation" without separating REORGANIZE, so observing it in M6b is justified.
- Permissions: `VIEW SERVER STATE`, and `VIEW SERVER PERFORMANCE STATE` from 2022, documented on the details, group_stats and index_usage_stats pages. Server-scoped DMVs fail rather than returning nothing, so exit code 2 is consistent with `execute()`.
- `OBJECTPROPERTY(..., 'TableIsMemoryOptimized')`: documented as 2014+, with NULL returned "when property is not a valid property name". So it does not fail on 2012, provided the implementation uses `COALESCE(..., 0)` for the "otherwise 0".
- `sys.dm_db_index_usage_stats` excludes memory-optimized and spatial indexes: documented ("does not return information about memory-optimized indexes or spatial indexes").
- `database_id = DB_ID()` on both DMVs: necessary and sufficient. `-database` sets the connection catalog, so `DB_ID()` names the right database.
- Dates: the driver builds `datetime` with `time.Date(..., loc)` and `normalise` formats it as RFC3339, so a `Z` suffix on a local time is real. Converting server-side with `CONVERT(varchar(19), x, 126)` avoids it.
- Columnstore indexes: `is_included_column = 1` for their columns (documented), so they show up under `included_columns` with an empty key and are identifiable through `index_type`. This is acceptable.
- Partitioning column with `key_ordinal = 0` and not included: documented ("partitioning column are returned as 0"), and correctly excluded.
- Column order: `sqlq` serialises a map, so row keys come out alphabetically. Only the `columns` array keeps the order, and that is what V0 must check. The design is not wrong on this point.
- `TestBundledQueriesPassTheReadOnlyGuard` picks up every `*.sql` file automatically (`filepath.Glob`), so "no new Go test" holds.

## Set aside (3)

- `collection_capped = 0` does not prove that nothing was lost: a cap reached and then released by purges leaves no trace.
- `key_columns` (unbracketed names, `, ` separator) and the DMV columns (bracketed names) use different formats. That makes step 3 a fragile text comparison, and a name containing `, ` is ambiguous.
- Validation runs on 2019 only, while the header claims 2012+. The spec does not say that the header will state this, as `blocked-processes-check.sql` does ("Validated on SQL Server 2019 only").
