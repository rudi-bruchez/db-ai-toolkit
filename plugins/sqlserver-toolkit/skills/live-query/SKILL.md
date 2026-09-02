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
3. **Route the question** using the decision tree below.
4. **Answer from the JSON.** Quote the values the server returned. If
   `truncated` is `true`, say so and give the real `rowcount`.

## Decision tree

| The user asks | Do this |
|---|---|
| Largest tables, space used | `-file queries/tables-largest.sql` |
| What references this table/object | `-file queries/object-references.sql -param name=<object>` — **read the warning in that file about dynamic SQL** |
| Why a view returns wrong or missing rows | `-file queries/view-diagnose.sql -param name=<view>`, then work the view checklist in `references/review-checklists.md` |
| Is this procedure well written | `-file queries/proc-source.sql -param name=<proc>`, then the procedure checklist |
| Are there badly built triggers | `-file queries/triggers-inventory.sql`, then the trigger checklist |
| Why is this query slow | Run it with `-plan`, then hand the `plan` field to the `sqlserver-query-plans` plugin. Do not analyse showplan XML by hand here. |
| Anything else | Write the query yourself, but keep the output discipline below |

Bundled queries live in `${CLAUDE_PLUGIN_ROOT}/skills/live-query/queries/`.

## Calling sqlq

```bash
sqlq -profile <name> -query "SELECT TOP (20) name FROM sys.tables ORDER BY name"
sqlq -profile <name> -file "${CLAUDE_PLUGIN_ROOT}/skills/live-query/queries/tables-largest.sql"
sqlq -profile <name> -file <path> -param name=dbo.Orders -maxrows 100
sqlq -profile <name> -query "<sql>" -plan          # capture the actual execution plan
sqlq -profile <name> -query "<sql>" -database Other # override the profile's database
sqlq -profile <name> -query "<sql>" -timeout 120   # default is 30 seconds
```

Every run prints one JSON object, on success and on failure alike:

```json
{"profile":"prod-erp","server":"SRV01","database":"ERP","elapsed_ms":42,
 "columns":[{"name":"n","type":"INT"}],"rows":[{"n":1}],
 "rowcount":1,"truncated":false,"messages":[],"plan":null,"error":null}
```

On failure, `error` carries `{number, severity, state, line, procedure,
message}`. Quote the error number and line — that is what makes a SQL Server
error diagnosable.

Exit codes: `0` success, `1` usage or configuration, `2` SQL error,
`3` refused by the write guard, `4` connection failure.

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
- **Do not assume the version.** Some catalog objects and columns only exist
  from a given release; check `SELECT @@VERSION` before relying on one.

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
omitted) or `readwrite`. A profile must never contain a password: name the
environment variable that holds it in `passwordEnv`. `sqlq` refuses to load a
file with an inline `password`.

> **The guard in `sqlq` is accident prevention, not security.** A lexical filter
> can be worked around. The control that matters is the login: give the agent a
> dedicated one with `db_datareader`, `VIEW DEFINITION` and `VIEW SERVER STATE`,
> and nothing else.

On Windows, `integrated` means SSPI and needs no setup. On Linux and macOS it
means Kerberos, and the profile must supply `krb5Realm` plus one of
`krb5ConfigFile`, `krb5KeytabFile` or `krb5CredCacheFile`.

## Known limits

- `messages` is always empty: `PRINT` and `RAISERROR` output below severity 11
  is not captured yet.
- Only the first result set is returned. Extra result sets are drained, except a
  showplan, which lands in `plan`.
