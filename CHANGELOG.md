# Changelog

The version is the one of the `sqlserver-toolkit` plugin
(`plugins/sqlserver-toolkit/.claude-plugin/plugin.json`), which carries the
Go tools. The other plugins have their own version in their `plugin.json`.

## [Unreleased]

## [0.6.0] - 2026-10-25

Reading what the server says, and seeing the plan. `sqlq` now returns every
result set of a batch, the server's messages and every plan, with caps that
bound what reaches the model's context without ever stopping the read. A new
tool, `sqlplan`, draws SQL Server and PostgreSQL execution plans in the
browser or as an image. Stored queries can be run by name from a catalogue.
This release covers everything since 0.4.0: the plugin version was set to
0.5.0 on 4 October without a release, so 0.6.0 is the first one to publish it.

### Added: server messages and result sets in `sqlq`

- Every result set of a batch is returned: the first in `columns`, `rows`,
  `rowcount` and `truncated`, the others in `more_results`, each with
  `-maxrows` applied.
- `messages` carries `PRINT` and low-severity `RAISERROR` output in the order
  the server sent it; later SQL errors go there as `error <n>: <text>`, while
  `error` keeps the first one.
- `incomplete: true` marks a set that an error or the timeout cut short, so its
  rows are not the whole set. Rows read before a timeout are kept.
- `-statistics` runs `SET STATISTICS IO ON` and `SET STATISTICS TIME ON` for
  the session. Their text arrives in `messages` as the server wrote it, in the
  session language, unparsed.
- With `-plan` and several statements, `plan` holds the plan of the first
  statement that has one and `more_plans` the others, in execution order. Plans
  are not paired with result sets: measured on SQL Server 2022, a batch run
  through `sp_executesql` (any `-param`) gives the same stream for a `SELECT`
  followed by its plan and for a trivial `SELECT` followed by an `UPDATE` plan.
- `-maxmessages` (default 200) and `-maxresultsets` (default 20, which also
  caps plans) bound what is kept, not what is read: past a cap `sqlq` still
  reads the whole batch, so a later error is still reported, and sets
  `messages_truncated`, `more_results_truncated` or `more_plans_truncated`.
- Notices and plans go through the same secret redaction as errors, including
  the XML-escaped spelling of a password that a plan holds when a query
  contains it as a literal.

### Added: `sqlplan`

- `sqlplan` draws a `.sqlplan` file, or the JSON of `sqlq -plan` on stdin, as
  one standalone HTML page: operator icons, row counts on the arrows, costly
  operators in color, light or dark theme. Several files go on one page.
- `-png` writes the plan as an image, through a headless Chrome, Chromium or
  Edge; `-node <NodeId>` keeps one operator and its subtree.
- PostgreSQL `EXPLAIN` output (JSON, the auto_explain JSON object, or psql
  text) is drawn with Dalibo's pev2, embedded, so the plan never leaves the
  machine.

### Added: the query catalogue

- `sqlq -list-queries` lists the stored queries of three sources: the queries
  bundled with the plugin, personal queries under
  `~/.config/db-ai-toolkit/queries/`, and a local clone of tsql-scripts whose
  scripts carry a `-- sqlq:` marker line.
- `-saved <name>` runs an entry, with `-param name=value`. A tsql-scripts
  parameter replaces the initial value of its `DECLARE`, bound as a typed value,
  never spliced into the text.
- `-save-query <name> -summary "<line>"` keeps a successful run under the
  profile, after reading it back through the catalogue.
- A registry records each successful run by the SHA-256 of the content; the
  catalogue shows an entry as `verified` only while its file is unchanged.
- Every catalogue file is listed, as an entry or as `rejected` with a reason;
  none disappears in silence.

### Added: the `page-salvage` skill

- Recovers the rows of a data page whose checksum is wrong (errors 824, 8928,
  8939) before `DBCC CHECKDB ... REPAIR_ALLOW_DATA_LOSS` deallocates it: the page
  is dumped with `DBCC PAGE`, its records parsed and checked against a copy of
  the data, then put back after the repair. It writes, and needs sysadmin
  through sqlcmd.

### Fixed

- `sqlq` no longer hangs until `-timeout` when more than 15 messages arrive
  while a single result set is being read, or between two sets of a
  parameterized batch. `SET STATISTICS IO ON` on a query reading 16 tables or
  more was enough.
- The first SQL error is no longer lost when a timeout follows it; it was
  replaced by `context deadline exceeded`.
- A one-column result set whose column name contains `showplan` is no longer
  taken for a plan when `-plan` is not set.

### Upgrading

- Rebuild the tools: `./scripts/build-tools.sh` (or `build-tools.ps1`).
- The JSON of `sqlq` gains `more_results`, `more_results_truncated`,
  `messages_truncated`, `more_plans`, `more_plans_truncated`, and `saved` on a
  `-saved` run. A caller reading only the first set sees no change.
- With `-plan` and several statements, `plan` is now the first plan, not the
  last, and so is the plan `sqlplan` draws from `sqlq -plan`. Run the slow
  statement alone, or read `more_plans`.
- Exit code `4` now means only that the connection could not be opened. A
  failure of the session preamble, of the batch, a timeout or a transport error
  exits `2`.
- The minimal login is `db_datareader` and `VIEW DEFINITION`. `VIEW SERVER
  STATE` is optional: it opens the instance DMVs, including the text of
  running queries with their literals.

### Known limits

- SQL Server buffers a statement's output until a network packet fills or the
  batch ends. Under a short `-timeout`, a batch like `SELECT 1/0; WAITFOR DELAY
  ...` returns only the timeout: the error and earlier rows never reached the
  client. `RAISERROR('', 0, 1) WITH NOWAIT;` before the long statement flushes
  what precedes it.
- The Query Store fallback for measuring queries without `VIEW SERVER STATE`
  needs `VIEW DATABASE STATE`, which the minimal login does not have.
- `-png` and `-node` apply to SQL Server plans only.
- The `sqlq` changes were tested against SQL Server 2022 CU26.

## [0.4.0] - 2026-10-02

The `missing-indexes.sql` query for the `live-query` skill. See the
[release page](https://github.com/rudi-bruchez/db-ai-toolkit/releases/tag/v0.4.0).

## [0.3.0] - 2026-10-01

The `blocked-processes-check.sql` query for the `live-query` skill. See the
[release page](https://github.com/rudi-bruchez/db-ai-toolkit/releases/tag/v0.3.0).
