# Validation — `missing-indexes.sql`

Date: 2026-10-01 and 2026-10-02. Query:
`plugins/sqlserver-toolkit/skills/live-query/queries/missing-indexes.sql`.
Plan: `docs/superpowers/plans/2026-10-01-missing-indexes.md`, task 4.

## Instance

- SQL Server 2022 (RTM-GDR, 16.0.1200.5), Developer Edition, Windows Server 2019.
- HADR not enabled. No database in an availability group.
- Test login: `sysadmin`. Two temporary logins were created for M8 and M9 and dropped.
- At the start: instance up for 15.1 days, 30 missing-index groups on the instance, no
  `mi_val_*` database or login.

## Probes

Every probe ends with `AND 1 = (SELECT 1)`. One probe was run with `-plan`: the plan carried
`StatementOptmLevel="FULL"` and a `<MissingIndexes>` element. Before each case, the suggestions
of each table involved were read directly from the DMVs (precondition `P`); every precondition
held.

One expectation of the plan did not match the engine, and the query was not at fault: for
`SELECT TOP (10) id, d ... WHERE a = 5 AND b = 3`, the DMV records `[a], [b]` as equality
columns and **no** included column, where the plan expected `[d]`. The query returns what the
DMV holds; the cases were judged against `P`.

## Cases

| case | precondition read | expected | observed | verdict |
|---|---|---|---|---|
| V0 | none (system databases) | no error; 38 columns in order with their types; `context` first; no number in quotes | as expected | pass |
| M1 | 1 suggestion (`t1`, from the plan check) | `tables_with_suggestions` 0 or 1; nothing unresolved | 1; unresolved 0; system 0 | pass |
| M2 | `t1`..`t4`: one group each, `[a], [b]`, 9 / 4 / 2 / 1 seeks | 4 tables with suggestions, 3 shown, ranked by score sum; `pk_tN` `CLUSTERED` `[id]`; two runs identical | `t1`, `t2`, `t3`, scores 112.4 / 50.0 / 25.0; identical runs | pass |
| M11 | as M2 | `is_auto_close_on` true | true; and every suggestion of the database was gone (see below) | pass |
| M3 | `m3`: 3 groups (`[f]`; `[g]` + `[h]`; `[h]`); no usage row for `ix_m3_unused` | 3 suggestions, 5 indexes in `index_id` order with their flags | as expected; `ix_m3_unused` counters 0; `ix_m3_disabled` `is_disabled` true; `ix_m3_filtered` `([c]=(1))`; `ix_m3_desc` `[a] DESC, [b]` include `[d]` | pass |
| M4 | `m4&<x`: 1 group | names with `&` and `<` returned intact | `m4&<x`, `[p&<q]`, `ix_m4&<z` `[k&<y]`; no `&amp;` or `&lt;` | pass |
| M3b | `w1`..`w3`: 10 groups each | 70 rows, `truncated` false, three blocks of 8 + 15, totals 10 and 17 | as expected; the 8 suggestions shown are the 8 best scores of a direct DMV read | pass |
| M5 | `o1` in another database: 1 group | output for `mi_val_a` unchanged except uptime and the instance group count | only `instance_uptime_days` and `suggestion_groups_on_instance` (68 to 69) differ | pass |
| M6 | `r1`: 1 group | after `ALTER INDEX ... REBUILD`, `P(r1)` empty and `r1` no longer ranked | as expected | pass |
| M6b | `r2`: 1 group | recorded as observed | after `ALTER INDEX ... REORGANIZE`, `P(r2)` **still** returns its group | recorded |
| M7 | `h1` (heap): 1 group | `existing` row `HEAP`, name and key NULL | as expected | pass |
| M7b | `p1`: 1 group; 4 partitions, 4 populated, 76 pages | one row for `pk_p1`; `used_mb` = 76 × 8 / 1024; counters equal to the DMV | `used_mb` 0.59; counters 0 / 3 / 0 / 1, equal to the DMV; `table_index_count` 1 | pass |
| M8 | login with `db_datareader` only, no `VIEW SERVER STATE` | a plain error, no rows | `sqlq` exit code 2, error 297 at line 104, no rows | pass |
| M9 | login with `VIEW SERVER STATE`, no `VIEW DEFINITION`, `SELECT` on `v1` only; `v1`, `v2`: 1 group each | a plain error, or `v2` absent **and** counted | `v2` absent, `suggestions_on_unresolved_objects` 1; `v1` rows identical to the sysadmin run (key, filter, size 0.93 MB, counters) | pass |
| M10 | — | memory-optimized table flagged, usage NULL | not executed (see below) | not executed |

## Findings

**`REORGANIZE` does not clear the suggestions on SQL Server 2022; `REBUILD` does (M6, M6b).**
Microsoft documents `ALTER INDEX` as a whole. The header of the query now says what was
observed, and the reading protocol says "index maintenance (a rebuild at least)".

**Switching `AUTO_CLOSE` on emptied every suggestion of the database (M11).** Four tables with
suggestions before, none after; the instance count fell from 34 to 30. This is what the header
already says: the database's counters and suggestions can be far younger than the instance.

**Size of the largest possible result (M3b).** 70 rows produce 64,240 characters of JSON.

## Not executed, not tested

- **M10, memory-optimized table.** The `MEMORY_OPTIMIZED_DATA` filegroup was created, but
  `ALTER DATABASE ... ADD FILE` to it failed twice with error 35221, which states that the
  Always On availability groups replica manager is disabled on this instance. Without the file
  no memory-optimized table can be created (error 41337). The `memory_optimized` and
  `usage_not_tracked` flags are therefore not validated on an instance.
- **Availability groups.** HADR is not enabled: `database_in_ag` was only seen false.
- **`collection_capped`.** The instance held at most 74 groups; the cap was never approached.
- **Versions.** Only SQL Server 2022 was tested. SQL Server 2012 to 2019, Azure SQL Managed
  Instance and Azure SQL Database were not.
- **Spatial indexes.** Not created; `usage_not_tracked` for them is not validated.

## Cleanup

The eight `mi_val_*` databases were set to `SINGLE_USER` and dropped, then the two test logins.
Afterwards no `mi_val_*` database, login or file remained on the instance. The two test
profiles in `mssql-profiles.json` and their two variables in the environment file are the
user's to remove.
