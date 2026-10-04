# Validation of the query catalogue

Date: 2026-10-04. Binary: `sqlq` built from branch `feat/query-catalog` (commit `904f84b` plus
this task). Spec: `docs/superpowers/specs/2026-10-04-query-catalog-design.md`. Plan:
`docs/superpowers/plans/2026-10-04-query-catalog.md`, task 12.

## Instance

- SQL Server 2025 (RTM-CU7, KB5096981, 17.0.4065.4), Enterprise Developer Edition (64-bit),
  in a test container started for this branch. 22 schedulers on two NUMA nodes, up for about
  an hour at the time of the runs.
- Test login: `sa`, through a profile in `readonly` mode, database `master`. Nothing was
  created on the instance.
- The registry and the personal directory were moved to temporary directories
  (`DB_AI_TOOLKIT_REGISTRY`, `DB_AI_TOOLKIT_QUERIES`): a verification made on a test container
  says nothing about the user's instances, and the user's `verified.json` was not touched.
- Every run used `-maxrows 20 -timeout 120`, and `-queries` pointing at the bundled queries of
  the repository, since `-saved` refuses a catalogue without the canon.

## The first set of tsql-scripts

Ten scripts of `tsql-scripts` received a marker line, and nothing else: `git diff` shows one
added line per file, and every file is still plain ASCII with LF line endings. Each one reads
DMVs or catalog views only and passes the guard as it stands.

| name | file | params |
|---|---|---|
| `sessions-by-host` | `diagnostics/sessions/sessions-from-host.sql` | `hostname` (`sysname`) |
| `trigger-execution-stats` | `diagnostics/execution-stats/trigger-stats.sql` | `forcurrentdbonly` (`bit`) |
| `database-data-log-sizes` | `database-information/size-and-allocation/database-sizes.sql` | `systemdbs` (`bit`) |
| `cpu-numa-layout` | `server-information/cores-and-numa.sql` | none |
| `requests-running-now` | `diagnostics/execution/running-requests-short.sql` | none |
| `transactions-open` | `diagnostics/execution/active-transactions.sql` | none |
| `memory-grants-live` | `diagnostics/Memory/memory-grants.sql` | none |
| `query-store-state` | `diagnostics/query-store/query-store-state.sql` | none |
| `instance-uptime` | `server-information/server-uptime.sql` | none |
| `instance-version-detail` | `server-information/sql-version.sql` | none |

`TestRealCloneHasNoRejectedEntry` on the clone: PASS, 10 marked scripts, 17 entries with the
7 bundled queries, no message. Before the markers it failed as intended (no marked script).
On a copy of the clone where `instance-uptime` was renamed `tables-largest`, it failed on
`name "tables-largest" is taken by bundled`: the test loads the bundled directory too, which
the plan's version did not.

Some candidates were left out without being tried on the instance: the scripts that use
`GO` (`analyze-blocked-sessions.sql`, `what-is-locked.sql`, `running-requests-detailed.sql`,
`waits-statistics.sql`), `USE` (`tempdb-space-usage.sql`) or `DROP`
(`sqlserver-memory.sql`), and two whose description runs over two comment lines
(`dm_io_virtual_file_stats.sql`, `query_stats.sql`), where the summary would be half a
sentence wherever the marker goes.

## Runs

`sets` is `1 + len(more_results)`, `rows` the `rowcount` of the first set. `verified` before
is `saved.verified`; after is the entry in `-list-queries -profile catalog-test` once every run
was done. No run had `incomplete` true in any set, none had an `error`, and none wrote to
stderr.

| run | passed | exit | sets | rows | messages | verified before | verified after |
|---|---|---|---|---|---|---|---|
| `-list-profiles` | | 0 | | | | | |
| `-query` version | | 0 | 1 | 1 | 0 | | |
| `-query` objects (plan, point 2) | | 0 | 1 | 3 | 0 | | |
| `blocked-processes-check` | | 0 | 1 | 1 | 0 | null | 2026-10-04 |
| `missing-indexes` | `-database master` | 0 | 1 | 1 | 0 | null | 2026-10-04 |
| `object-references` | `name=sys.all_columns` | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `proc-source` | `name=None` (see below) | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `proc-source` | `name=sys.sp_add_agent_parameter` | 0 | 1 | 0 | 0 | 2026-10-04 | 2026-10-04 |
| `tables-largest` | | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `triggers-inventory` | | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `view-diagnose` | `name=sys.all_columns` | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `sessions-by-host` | default | 0 | 1 | 2 | 0 | null | 2026-10-04 |
| `sessions-by-host` | `hostname=<host of the test machine>` | 0 | 1 | 1 | 0 | 2026-10-04 | 2026-10-04 |
| `trigger-execution-stats` | default | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `trigger-execution-stats` | `forcurrentdbonly=0` | 0 | 1 | 0 | 0 | 2026-10-04 | 2026-10-04 |
| `database-data-log-sizes` | default | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `database-data-log-sizes` | `systemdbs=1` | 0 | 1 | 3 | 0 | 2026-10-04 | 2026-10-04 |
| `cpu-numa-layout` | | 0 | 4 | 2 | 0 | null | 2026-10-04 |
| `requests-running-now` | | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `transactions-open` | | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `memory-grants-live` | | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `query-store-state` | | 0 | 1 | 0 | 0 | null | 2026-10-04 |
| `instance-uptime` | | 0 | 1 | 1 | 0 | null | 2026-10-04 |
| `instance-version-detail` | | 0 | 1 | 1 | 0 | null | 2026-10-04 |

Every `verified` after carries `"profile":"catalog-test"`. Both `-list-queries` calls (before
the runs, and after with `-profile catalog-test`) returned no `rejected` entry and no message.

## Findings

The parameterised runs used the value passed. On each second run, `saved.params` held
exactly the value given (`systemdbs` `1`, `forcurrentdbonly` `0`, the host name) and
`saved.defaults` was empty; on the first run, `saved.params` was empty and `saved.defaults`
named the parameter. The results moved accordingly: `database-data-log-sizes` went from no
row (the container has no user database and the default leaves out `msdb` and `tempdb`) to
three (`(ALL)`, `msdb`, `tempdb`), and `sessions-by-host` from two sessions (the sqlq session
and `SQLServerCEIP`) to the sqlq session only. `trigger-execution-stats` returned no row
either way: the container has no trigger, so that run proves the `bit` binding compiles and
runs, not that the filter changes the answer.

`cpu-numa-layout` returned four result sets (2, 1, 22 and 1 rows). The third was cut at 20
rows with `truncated` true, `rowcount` still 22, and `incomplete` false: `-maxrows` applies to
each set.

The objects query of the plan (point 2) returned three views (`sys.all_columns`,
`sys.all_objects`, `sys.all_parameters`) and no procedure, since `ORDER BY name` puts the
`all_*` views first. The validation script took "the first procedure" from that result and
ran `proc-source` with the literal value `None`. The run succeeded with no row, and was
repeated with `sys.sp_add_agent_parameter`, read by the same query restricted to `type = 'P'`.

`object-references`, `proc-source` and `view-diagnose` returned no row for the system objects
the plan chose. Not a defect of the queries: `view-diagnose` reads `sys.views`, `proc-source`
reads `sys.objects`, and `object-references` lists user modules (`is_ms_shipped = 0`); none of
them sees a system object, and `master` on the container holds no user object. These three
runs prove that each query compiles and runs with a bound `@name`, not that it answers.
Likewise `tables-largest`, `triggers-inventory`, `requests-running-now`, `transactions-open`,
`memory-grants-live` and `query-store-state` (no Query Store in `master`) ran on an idle,
empty instance and returned nothing.

## Not executed, not tested

- Any instance other than this SQL Server 2025 container: no older version, no Azure SQL.
- A `-saved` run of a marked script whose parameter is an `int` or a date: the first set has
  `sysname` and `bit` only. The `int` binding rests on the unit tests of `BindValue`, the
  `datetime` binding on those and on an integration test in `tools/cmd/sqlq`.
- An entry marked `heavy`: none in the first set.
- The three object queries on a user object, and the activity scripts under load.

## Cleanup

Nothing was created on the instance. The temporary registry, the personal directory and the
binary live in the session's scratch directory. The test container and its profile file
belong to the controller and were not touched.
