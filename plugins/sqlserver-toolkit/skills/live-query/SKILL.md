---
name: live-query
description: Answer questions about a live SQL Server instance over a real connection - largest tables, which procedures reference a table, why a view returns the wrong rows, whether a procedure or trigger is well written. Use when the user asks something that can only be answered by querying an instance, and names or implies a connection profile. Runs read-only through the bundled sqlq tool, which returns JSON.
---

# Live SQL Server questions

Query a connected instance and answer from what the server actually says. All
access goes through `sqlq`, a bundled tool that resolves the connection from a
named profile, blocks writing statements on read-only profiles, and returns one
JSON object.

## Workflow

1. **Check the tool.** Run `sqlq -list-profiles`. It prints the configured
   profiles as JSON.
   - *Command not found*: the binary has not been built. Tell the user to run
     `./scripts/build-tools.ps1` (or `./scripts/build-tools.sh`) from the
     repository root, which needs the Go toolchain. Do not fall back to writing
     ad-hoc `sqlcmd` invocations: the guardrails live in `sqlq`.
   - *No profile file*: point the user at "Configuration" below. Never invent a
     server name or connection string.
2. **Pick the profile.** If the user named one, use it. If exactly one exists,
   use it and say which. If several exist and the question does not identify
   one, ask — do not guess which instance to touch.

   `-list-profiles` gives names, `auth`, `readonly`, and `environment`. It
   deliberately does **not** give server or database names: every session starts
   with this call, and the host names and database names are the estate map. A
   human who wants them has `%LOCALAPPDATA%\db-ai-toolkit\servers.json`.

   A profile marked `"environment": "prod"` is a production instance. **Before
   the first query of a session against one, name the profile to the user and
   wait for an explicit yes.** The damage this prevents is not a write — it is a
   diagnostic query run against production while you believed you were on the
   staging copy: production rows in the transcript, read locks on a busy
   instance, and a conclusion drawn from the wrong environment. Instances here
   are commonly named PRD / STA / REC / DEV of the same application, in more
   than one country, so the names are nearly identical by design.

   A profile marked `"unusable"` cannot run here — a DPAPI-backed profile on a
   non-Windows machine. Say so; do not try it.
3. **Route the question.** Run `sqlq -list-queries -profile <name>`. Prefer a
   catalogue entry to writing SQL: see "The query catalogue" below.
4. **Answer from the JSON.** Quote the values the server returned. If
   `truncated` is `true`, say so and give the real `rowcount`. If
   `more_results` is not empty, read every set before concluding.

## The query catalogue

`sqlq -list-queries -profile <name>` prints every stored query this profile can
run, from three sources:

| Source | Where the file is (`path` is relative to it) |
|---|---|
| `bundled` | `${CLAUDE_PLUGIN_ROOT}/skills/live-query/queries/<path>` |
| `tsql-scripts` | `$DB_AI_TOOLKIT_TSQL_SCRIPTS/<path>`, the user's local clone |
| `personal` | `~/.config/db-ai-toolkit/queries/<path>` (`_generic/` or `profiles/<profile>/`) |

Call it once the profile is chosen: without `-profile`, the queries saved for
that profile are not shown. Its `messages` say when a source is missing
(`tsql-scripts source not configured`, `bundled queries not found`): a missing
source is not an empty one, so do not conclude that no stored query exists.

Every catalogue file is either listed or listed as `rejected` with a reason;
none disappears in silence.

| Field | What it requires of you |
|---|---|
| `verified: null` | Never run with `sqlq` on this machine in its current form. Say so before running it. Otherwise it gives the date and profile of the last success, which proves the SQL ran, not that its answer is right. |
| `rejected` | **Do not run it, and do not rewrite the script as an ad-hoc query to get around the refusal.** Report the reason; the fix belongs to the user, in the source file. |
| `dirty_reads: true` | The query reads uncommitted data (`NOLOCK`, `READ UNCOMMITTED`). **Never rely on its result to answer a question about whether data is correct.** |
| `heavy: true` | Expensive by its author's judgement. **On a `prod` profile, announce the cost and wait for a yes**, even if the session was already confirmed. |
| `params` | Pass each with `-param name=value`. A `bundled` or `personal` parameter has no default: all are required. A `tsql-scripts` parameter carries its declared type and keeps the file's default when not passed; the default itself is in the file's header, not in the catalogue. |

**Before the first run of a catalogue query in a session, read its header**, at
the location in the table above. That is where its warnings live: which column
to read, which `-maxrows` keeps it whole, which checklist in
`references/review-checklists.md` or `references/missing-index-reading.md`
comes next. A `tsql-scripts` header is the block of `--` lines that opens the
file.

**Always pass `-maxrows` to a catalogue query.** The `TOP (n)` rule below is for
the SQL you write; for a stored query its author answers for the size of the
result, and `-maxrows` stays the safety net.

| The user asks | Do this |
|---|---|
| Why is this query slow | Run it with `-plan`, then hand the `plan` field to the `sqlserver-query-plans` plugin. Do not analyse showplan XML by hand here. With several statements, `plan` is the first statement's plan and the others are in `more_plans`; run the slow one alone when you want its plan at the root. When the user wants to see the plan, pipe the same run into `sqlplan`, which draws the `plan` field (the first plan) in the browser. |
| Anything no catalogue entry answers | Write the query yourself, but keep the output discipline below. |

After an ad-hoc query that succeeded and proved useful, offer in one line to
save it: `-save-query <name> -summary "<one line>"`. **Never save without the
user's explicit yes.** Saving is a run: the file is written only if the run
succeeds, and it is saved for this profile only. Keep clients, hosts and
databases out of the name and the summary: the catalogue is printed in every
session.

### Parameters of a tsql-scripts entry

A `tsql-scripts` value replaces the initial value of a `DECLARE` in the script,
bound as a typed parameter, never spliced into the text. `sqlq` checks it before
connecting: a value too long for its `nvarchar(n)` or `varchar(n)`, non-ASCII
text for a `varchar`, an integer outside its type's range, a `bit` other than
`0`, `1`, `true`, `false`, is refused with exit code `1`. Dates are
`YYYY-MM-DD`, and date-times `YYYY-MM-DDTHH:MM[:SS]` (`smalldatetime`: minutes
only), never with fractional seconds, and within the type's year range (from
`0001` for `date` and `datetime2`, from `1753-01-01` for `datetime`,
`1900-01-01` to `2079-06-06` for `smalldatetime`). A `-param` the entry does
not declare is refused: a typo must not silently run the default.

A `rejected` reason on a tsql-scripts entry usually names one of these rules,
which the user applies in the clone:

- The marker is one line in the header (the `--` lines opening the file):
  `-- sqlq: name=<name> params=<a>,<b> heavy`. `name=` is required, `params=`
  and `heavy` are optional; any other key, a repeated parameter, a second
  marker or one below the header is rejected.
- The summary is the nearest non-empty comment line above the marker that is
  not only dashes and not a URL. None: rejected.
- A parameter is accepted only on a standalone line
  `DECLARE @p <type> = <one expression>;` (or `DECLARE @p AS <type> = ...;`),
  nothing else on the line, no `IF`, `ELSE`, `WHILE`, `BEGIN`, `GOTO` or label
  before it in the script, never assigned again in the script, its name ASCII letters, digits and `_`. Types:
  `nvarchar`, `nchar`, `sysname`, `varchar`, `char`, the integer types, `bit`,
  `date`, `datetime`, `datetime2`, `smalldatetime`.

## Calling sqlq

```bash
sqlq -profile <name> -query "SELECT TOP (20) name FROM sys.tables ORDER BY name"
sqlq -profile <name> -file "${CLAUDE_PLUGIN_ROOT}/skills/live-query/queries/tables-largest.sql"
sqlq -profile <name> -file <path> -param name=dbo.Orders -maxrows 100
sqlq -profile <name> -query "<sql>" -plan          # capture the actual execution plan
sqlq -profile <name> -query "<sql>" -statistics    # SET STATISTICS IO and TIME ON, text in messages
sqlq -profile <name> -query "<sql>" -database Other # override the profile's database
sqlq -profile <name> -query "<sql>" -timeout 120   # default is 30 seconds
sqlq -list-queries -profile <name>                 # the catalogue for this profile
sqlq -profile <name> -saved tables-largest -maxrows 20
sqlq -profile <name> -saved sessions-by-host -param hostname=SRV-APP01 -maxrows 50
sqlq -profile <name> -query "<sql>" -save-query orders-late -summary "Orders past their promised date."
```

Two flags move the catalogue's sources, for development and for a local clone:
`-queries <dir>` replaces the bundled directory (found next to the binary by
default), and `-tsql-scripts <dir>` names the tsql-scripts clone instead of
`$DB_AI_TOOLKIT_TSQL_SCRIPTS`. When the bundled directory is not found, unreadable or empty,
`-saved` and `-save-query` refuse with exit code `1` (`bundled queries not
found`): without the canon, a name cannot be checked against it. Pass
`-queries <plugin>/skills/live-query/queries` in that case. `-save-query` also refuses a run made with
`-database` or `-dirty-reads` (the saved file would not record them): save from
a profile whose database is the right one, and write the isolation level into
the query.

Every run prints one JSON object, on success and on failure alike:

```json
{"profile":"prod-erp","server":"SRV01","database":"ERP","elapsed_ms":42,
 "columns":[{"name":"n","type":"INT"}],"rows":[{"n":1}],
 "rowcount":1,"truncated":false,"incomplete":false,"more_results":[],
 "more_results_truncated":false,"messages":[],"messages_truncated":false,
 "plan":null,"more_plans":[],"more_plans_truncated":false,"error":null,
 "saved":{"name":"tables-largest","source":"bundled","path":"tables-largest.sql",
          "params":{},"defaults":[],"verified":null}}
```

- `more_results` holds every result set after the first, each as
  `{columns, rows, rowcount, truncated, incomplete}`; `-maxrows` applies to each.
- `incomplete: true`, on the top level or on a set, means an error or the
  timeout cut that set short: its rows are not the whole set. Never report them
  as complete.
- `messages` carries the server's `PRINT` and low-severity `RAISERROR` output,
  in order, plus what `sqlq` itself has to say: on a `-saved` run, the
  catalogue's own messages (a missing tsql-scripts source, a disabled personal
  layer) are copied here.
- `plan` is the plan of the first statement that has one; the later plans are in
  `more_plans`, in execution order, and each plan contains the `StatementText`
  of its statement. None is attached to a set of `more_results`, because the
  server's stream does not allow it without guessing: a trivial statement with
  no table has no plan, while an `UPDATE` has one and returns no rows.
- `more_results_truncated: true` means `-maxresultsets` (default 20, counting the
  first set) was reached: the later sets were read and dropped. Say so rather
  than report the kept sets as all of them.
- `messages_truncated: true` means `-maxmessages` (default 200) was reached: the
  later notices and later errors were read and dropped. `error` still holds the
  first error. The cap applies to server messages: on a `-saved` run `sqlq` can
  add its own notes (catalog, registry) beyond it.
- `more_plans_truncated: true` means `-maxresultsets` was reached for plans:
  `plan` plus `more_plans` hold at most that many, and the rest were dropped.
- `-statistics` adds `SET STATISTICS IO ON` and `SET STATISTICS TIME ON` to the
  session, and the server's text lands in `messages`, one notice per table read
  plus the TIME notices, so it counts toward `-maxmessages`. `sqlq` does not
  parse it: the text follows the session language. Under `SET LANGUAGE French`
  the notice reads `Table 'sysschobjs'. Nombre d'analyses '1', lectures
  logiques 66, ...` and the TIME notice `Temps UC = 0 ms, temps écoulé = 0 ms`.
  Quote the lines as they are, and read the counters from the wording in front
  of you rather than assuming `logical reads`.
- Past any of these caps, `sqlq` reads everything and stops storing: the batch
  runs to its end, and an error after the cap still sets `error` and exit `2`.
  `0` lifts a cap; raise it when the dropped part is what you need.
- `saved` appears on a `-saved` run only: which entry ran, the `params` you
  passed, the `defaults` left untouched, and `verified` as it was before the run.

On failure, `error` carries `{number, severity, state, line, procedure,
message}`. Quote the error number and line — that is what makes a SQL Server
error diagnosable. `error` keeps the first SQL error; later ones are in
`messages` as `error <n>: <text>`, and the sets read before the error are kept.

Exit codes: `0` success, `1` usage or configuration, `2` any failure after
the query started (SQL error, timeout or transport), `3` refused by the write
guard, `4` could not connect (opening the connection failed); a failure of the
session preamble or of the batch exits `2`. When a SQL error and the timeout
both occur, `error` is the first SQL error, and the rows read before a timeout
are kept, but only for output the server already sent. SQL Server buffers a
statement's output until a network packet fills or the batch ends: with
`SELECT 1/0; WAITFOR DELAY ...` under a short `-timeout`, the error and the
earlier rows never arrive and the result is just the timeout. Put
`RAISERROR('', 0, 1) WITH NOWAIT;` before a long statement to flush what
precedes it (it adds one blank line to `messages`), or run the slow statement
alone.

Two flags exist that this skill must not reach for on its own:

- `-allow-write` lets a writing batch through, and only on a `readwrite`
  profile. **Show the user the exact statement and get an explicit yes to that
  statement before passing it.** A profile permitting writes is not consent.
- `-dirty-reads` runs the batch at `READ UNCOMMITTED`. It stops the query
  waiting on locks, at the price of dirty reads and missing or duplicated rows.
  Never use it to answer a question about whether data is correct — that is
  the one question it can answer wrongly, and silently.

## Output discipline

- **Always bound the result.** Put `TOP (n)` in the query. `-maxrows` (default
  50) is a backstop, not a substitute — it truncates after the server has
  already done the work.
- **Never `SELECT *`** against a user table. Name the columns you need.
- **Never paste a large result verbatim.** Summarise, then show the rows that
  carry the answer.
- **Pass values as `-param`, never by string concatenation.** Object names are
  filtered with a predicate against the catalog views; they are not spliced
  into SQL text.

## What not to do

- **Do not work around a refusal.** Exit code `3` means the batch would write.
  Report it and stop; do not rephrase the SQL to slip past the check, and do not
  reach for `OPENQUERY` or `OPENROWSET` — they are refused for the same reason.
- **Do not write on your own initiative.** Never pass `-allow-write`, and never
  suggest switching a profile to `readwrite`, to complete a task the user asked
  a *question* about.
- **Do not use `EXEC` or `DBCC`.** Both are refused on read-only profiles.
  Use `OBJECT_DEFINITION()` instead of `sp_helptext`.
- **Do not invent results.** If the query failed, say what the server returned.
  Never fill a gap with a plausible-looking row.
- **Do not turn a missing-index suggestion into DDL.** Never paste a suggestion
  as `CREATE INDEX`; never conclude "no index is missing" from an empty result;
  never recommend dropping an index from `missing-indexes.sql`, whose counters
  cover one replica and a window shorter than the instance uptime; never add a
  key column to a unique index or a primary key. See
  `references/missing-index-reading.md`.
- **Do not assume the version.** Some catalog objects and columns only exist
  from a given release; check `SELECT @@VERSION` before relying on one.
- **Do not send `USE`.** It is refused. It writes nothing, so it slips past the write guard,
  but it changes the database for the rest of the batch — which makes the `database` field of
  the answer name a catalog the query did not run in, and overrides `-database` from inside the
  text that flag was meant to govern. Choose the catalog with `-database`, or name it in the
  object (`Other.dbo.T`), which stays allowed.
- **Do not send `GO`.** It is a batch separator belonging to SSMS and `sqlcmd`,
  not T-SQL; `sqlq` refuses a batch containing it. Send one batch per call.
- **Do not retry after error 18456.** A rejected login is the one failure where
  trying again causes harm: repeated failures can lock the account out, and a
  burst of failed administrator logins from a workstation is what credential
  stuffing looks like in the other team's security log. Report it and stop. The
  fix is the user's: correct the password in SSMS, then re-run the import.

## Configuration

Profiles live outside the repository, resolved in this order: `-profiles
<path>`, then `$MSSQL_PROFILES`, then
`~/.config/db-ai-toolkit/mssql-profiles.json`.

```json
{
  "prod-erp":    { "server": "SRV01", "database": "ERP",
                   "auth": "integrated", "mode": "readonly" },
  "prod-azure":  { "server": "x.database.windows.net", "database": "D",
                   "auth": "entra", "fedauth": "ActiveDirectoryDefault",
                   "mode": "readonly" },
  "prod-legacy": { "server": "SRV02\\SQLEXPRESS,1433", "database": "L",
                   "auth": "sql", "user": "svc_claude",
                   "passwordEnv": "MSSQL_LEGACY_PWD", "mode": "readonly" }
}
```

`auth` is `integrated`, `sql` or `entra`. `mode` is `readonly` (the default when
omitted) or `readwrite`. A profile must never contain a password: name where the
password lives, with exactly one of

- `passwordEnv` — an environment variable, or
- `passwordDpapi` — a key into the DPAPI-encrypted store written by
  `registered-servers/Import-RegisteredServerCredentials.ps1` (Windows only).

`sqlq` refuses to load a file with an inline `password`.

Entries carrying `"managedBy": "registered-servers"` are generated from the SSMS
registered servers and are rewritten on every export; edits to them are lost.
Entries without that marker are hand-written and never touched. **Never run the
import script yourself** — it is the user's to run, and it is the one command in
this toolkit that handles secrets.

> **The guard in `sqlq` is accident prevention, not security.** A lexical filter
> can be worked around. The control that matters is the login: give the agent a
> dedicated one with `db_datareader` and `VIEW DEFINITION`, and nothing else.
> `VIEW SERVER STATE` is optional, see "Permissions and the Query Store" below.

On Windows, `integrated` means SSPI and needs no setup. On Linux and macOS it
means Kerberos, and the profile must supply `krb5Realm` plus one of
`krb5ConfigFile`, `krb5KeytabFile` or `krb5CredCacheFile`.

## Permissions and the Query Store

`VIEW SERVER STATE` is not required. It opens the instance DMVs, among them
`sys.dm_exec_sql_text`, which returns the text of running queries with their
literal values. Without it the catalogs, the database DMVs (with `VIEW DATABASE
STATE`) and the Query Store remain.

When the instance DMVs are closed, `sys.query_store_runtime_stats` gives
`avg_logical_io_reads` and `avg_duration` as an ordinary result set. It needs
`VIEW DATABASE STATE` (on SQL Server 2022 and later, `VIEW DATABASE PERFORMANCE
STATE`, which it includes), and the recommended minimal login does not have it:
without it, the query fails with error 262 or 297. These are averages over an
aggregation interval, written with a delay: a historical hint to find the expensive queries, not the
measurement of one call. To measure a call, use `-statistics`.

## Known limits

- The verification registry and the personal queries belong to this machine;
  nothing synchronises them.
- A tsql-scripts parameter can only override a one-line `DECLARE` of a simple
  type. Placeholders such as `<database>` and hard-coded object names are not
  adjustable until the script is converted.
