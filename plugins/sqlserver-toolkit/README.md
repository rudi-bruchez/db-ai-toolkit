# SQL Server Toolkit

Claude Code plugin: diagnostics, performance tuning and query design for
Microsoft SQL Server.

## Components

| Type | Name | Purpose |
|------|------|---------|
| Skill | `errorlog-diagnostics` | Triage SQL Server ERRORLOG files from a folder |
| Skill | `live-query` | Answer questions against a connected instance, read-only |
| Command | `/diagnose-errorlog <folder>` | Entry point that runs the skill on a path |
| Tool | `errorlog-parse` (Go, in `bin/`) | Strips ERRORLOG noise before the skill reads it |
| Tool | `sqlq` (Go, in `bin/`) | Runs a guarded read-only query and returns JSON |

## The `errorlog-parse` preprocessor

Raw ERRORLOG files are dominated by routine noise (backups, successful logins,
startup and recovery messages). `errorlog-parse` reads the raw log and emits a
compact digest — errors, high severities, failed logins, I/O stalls, corruption
and dump signatures — so the skill analyzes a fraction of the original size.

The binary lives in `bin/`, which Claude Code puts on the Bash tool's PATH when
the plugin is enabled, so the skill calls it as a bare command.

### Building the binary

The binary is **not committed**. Build it once (requires the Go toolchain):

```powershell
# from the repository root
./scripts/build-tools.ps1     # Windows
./scripts/build-tools.sh      # macOS, Linux
```

This compiles `tools/cmd/*` into `plugins/sqlserver-toolkit/bin/`.

If the binary is absent (not built, or unsupported platform), the skill degrades
gracefully and reads the raw ERRORLOG directly — correct results, just more
tokens. For distribution to machines without Go, ship prebuilt binaries via a
release (`./scripts/build-tools.ps1 -All` cross-compiles the matrix).

### Using it directly

```bash
errorlog-parse "C:\Program Files\Microsoft SQL Server\MSSQL16.MSSQLSERVER\MSSQL\Log"
errorlog-parse -severity 17 ERRORLOG
type ERRORLOG | errorlog-parse -
```

Output: a `#`-prefixed header (total / kept / dropped counts and dropped
categories) followed by the kept entries verbatim.

## The `sqlq` query runner

`sqlq` connects to a SQL Server instance named by a profile, runs one batch and
prints a single JSON object. It exists so the `live-query` skill never has to
rebuild connection ceremony, parse column-aligned text, or be trusted not to
write to production.

It embeds `microsoft/go-mssqldb` rather than shelling out to `sqlcmd`, which
buys three things that matter for diagnosis: SQL errors keep their number,
severity, line and procedure; showplan XML comes back intact instead of wrapped
to column widths; and values keep their types instead of being reformatted as
text.

### Profiles

Profiles live outside the repository, resolved in order: `-profiles <path>`,
`$MSSQL_PROFILES`, then `~/.config/db-ai-toolkit/mssql-profiles.json`.

```json
{
  "prod-erp":    { "server": "SRV01", "database": "ERP",
                   "auth": "integrated", "mode": "readonly" },
  "prod-azure":  { "server": "x.database.windows.net", "database": "D",
                   "auth": "entra", "fedauth": "ActiveDirectoryDefault",
                   "mode": "readonly" },
  "prod-legacy": { "server": "SRV02\SQLEXPRESS,1433", "database": "L",
                   "auth": "sql", "user": "svc_claude",
                   "passwordEnv": "MSSQL_LEGACY_PWD", "mode": "readonly" }
}
```

`auth` is `integrated` (SSPI on Windows, Kerberos elsewhere), `sql` or `entra`.
`mode` defaults to `readonly` when omitted — the safe setting has to be the one
you get by forgetting. A profile carrying an inline `password` is **refused**:
name the environment variable in `passwordEnv` instead.

### Guardrails

On a read-only profile, `sqlq` strips comments and string literals, then refuses
any batch containing a writing keyword — including `SELECT ... INTO`, `EXEC` and
`DBCC`. `OPENQUERY`, `OPENROWSET` and `OPENDATASOURCE` are refused outright: the
statement they hand to the remote server travels as a string literal, so
stripping literals — the very thing that makes `SELECT 'DROP TABLE x'` safe —
would hide a `DELETE` aimed at a linked server.

Writing takes two independent yeses: the profile must be in `readwrite` mode,
**and** the invocation must pass `-allow-write`. A profile is something somebody
edited once and forgot; the flag is a statement about this run.

Every batch is prefixed with `SET NOCOUNT ON` and `SET LOCK_TIMEOUT 5000`, so a
query gives up rather than queueing behind production work. The isolation level
is deliberately left alone. `READ UNCOMMITTED` would also stop the query waiting
on shared locks, but it permits dirty reads and missing or duplicated rows during
page splits — and the questions this tool exists to answer are correctness
questions. `-dirty-reads` turns it on for callers who accept that trade.

`-maxrows` (default 50) bounds what reaches the model, not what the server does:
rows past the cap are read off the wire and discarded, so `rowcount` stays true.
Bound the query with `TOP (n)` as well.

> **This is accident prevention, not security.** A lexical filter can be worked
> around. The control that matters is the login: give the agent a dedicated one
> with `db_datareader`, `VIEW DEFINITION` and `VIEW SERVER STATE`, and nothing
> else — and no linked server whose connection mapping is more privileged than
> the caller.

### Using it directly

```bash
sqlq -list-profiles
sqlq -profile prod-erp -query "SELECT TOP (10) name FROM sys.tables ORDER BY name"
sqlq -profile prod-erp -file queries/tables-largest.sql
sqlq -profile prod-erp -file queries/object-references.sql -param name=dbo.Orders
sqlq -profile prod-erp -query "<sql>" -plan
sqlq -profile prod-erp -query "<sql>" -database Other -timeout 120
sqlq -list-queries -profile prod-erp
sqlq -profile prod-erp -saved tables-largest -maxrows 20
sqlq -profile prod-erp -saved sessions-by-host -param hostname=SRV-APP01 -maxrows 50
sqlq -profile prod-erp -query "<sql>" -save-query orders-late -summary "Orders past their promised date."
```

`-database` overrides the profile's database for one run. `-timeout` (default 30
seconds) is how long the query may take before it is cancelled; raise it for a
deliberately long diagnostic, not to wait out a blocked session.

Exit codes: `0` success, `1` usage or configuration, `2` SQL error, `3` refused
by the write guard, `4` connection failure. The JSON goes to stdout in every
case, so a caller never has two formats to parse.

A batch can return several result sets. The first fills `columns`, `rows`,
`rowcount` and `truncated`; every later one goes to `more_results`, each as
`{columns, rows, rowcount, truncated, incomplete}`, with `-maxrows` applied to
each. `incomplete: true` (on the top level for the first set) means an error cut
that set short, so its rows are not the whole set. `error` keeps the first SQL
error and the exit code stays `2`; later errors go to `messages` as
`error <n>: <text>`. `messages` also carries `PRINT` and low-severity
`RAISERROR` output, in the order the server sent it. With `-plan` and several
statements, only the last statement's plan is kept.

### Query catalogue

Stored queries are run by name, so a question already answered once is not
rewritten from scratch. `sqlq -list-queries` prints the catalogue as JSON; with
`-profile`, it adds the queries saved for that profile. The catalogue reads
three sources:

| Source | Location | Name |
|---|---|---|
| `bundled` | `skills/live-query/queries/*.sql`, found next to the binary | the file name |
| `personal` | `~/.config/db-ai-toolkit/queries/_generic/*.sql`, and `profiles/<profile>/*.sql` for one profile | the file name |
| `tsql-scripts` | a local clone of tsql-scripts, walked recursively, `.git/` excluded | the `name=` of its `-- sqlq:` marker line |

The tsql-scripts clone is named by `-tsql-scripts <dir>`, or else by
`$DB_AI_TOOLKIT_TSQL_SCRIPTS`; there is no guessed default. When it is not set
or not found, the catalogue says so in `messages`, so that a missing source is
not mistaken for an empty one. `-queries <dir>` replaces the bundled directory,
for development outside the plugin. A bundled name wins a collision with
another source; between two other sources, every entry holding the name is
rejected. Only scripts carrying a marker enter the catalogue, and only the
user adds one.

Every catalogue file is listed, either as an entry or as `rejected` with a
reason (a guard refusal such as `batch separator GO at line 9`, an invalid or
duplicate marker, a missing summary, a parameter that cannot be overridden). No
file disappears in silence. A reason names a keyword, a parameter or a line,
never an excerpt of the file, because the catalogue reaches the model provider
in every session; for the same reason it prints paths relative to their source
and no parameter default.

`-saved <name>` runs an entry, with its values given as `-param name=value`.
The result gains a `saved` object naming the entry, the values passed, the
parameters left at their default, and its verification state before the run;
the catalogue's own messages are copied into `messages`. A bundled or personal
parameter is required and bound as `nvarchar`. A tsql-scripts parameter
replaces the initial value of its `DECLARE`, bound as a typed value and never
spliced into the text. That is accepted only on a standalone line
`DECLARE @p <type> = <one expression>;` (or `AS <type>`), with no `IF`,
`ELSE`, `WHILE`, `BEGIN`, `GOTO` or label before it, where the variable is never
assigned again and its name is ASCII; otherwise the entry is rejected. The value
is checked against the type before connecting: string length and ASCII for
`varchar`, integer range, `bit` as `0`, `1`, `true` or `false`, dates as
`YYYY-MM-DD` and date-times as `YYYY-MM-DDTHH:MM[:SS]` without fractional
seconds, within the year range of each type.

`-save-query <name> -summary "<one line>"` saves a successful `-query` or
`-file` run under `profiles/<profile>/`. The query runs first; the file is
written only if it exits `0`, then read back through the catalogue and removed
if it does not come out as a valid entry. Writing batches are never saved, nor a
name already present in the profile's view. `sqlq` never writes into the plugin
or into tsql-scripts.

`-saved` and `-save-query` refuse with exit code `1` when the bundled directory
is not found: without the canon, a name cannot be checked against it. Pass
`-queries <plugin>/skills/live-query/queries` to a binary built elsewhere.

Each successful `-saved` run, and each save, is recorded in the verification
registry, `~/.config/db-ai-toolkit/verified.json`, keyed by the SHA-256 of the
content. The catalogue shows `verified` (date and profile) only while the file
is unchanged, and `null` otherwise. The registry and the personal queries are
local to the machine. `$DB_AI_TOOLKIT_QUERIES` moves the personal directory and
`$DB_AI_TOOLKIT_REGISTRY` the registry file: both are entry points for tests and
development.

### Known limits

- A tsql-scripts parameter can only override a one-line `DECLARE` of a simple
  type. A marked script using `GO`, a temporary table or `EXEC` is listed as
  `rejected`: the guard is not relaxed for the catalogue.
- The verification registry attests that a content ran successfully, not that
  its answer is right. It is not synchronised between machines, and two runs
  finishing at the same instant can lose one entry.
- The binary is ~15 MB because Entra ID support pulls in the Azure identity
  libraries.

## Official documentation (Microsoft Learn MCP)

The plugin declares the Microsoft-hosted **Microsoft Learn** MCP server in
`.mcp.json` (remote HTTP, no install, no API key). When you enable the plugin,
Claude Code asks you to approve the server once — like any project MCP server;
the plugin cannot enable it without your consent. Once approved, the
`errorlog-diagnostics` skill uses it to ground findings in official docs
(error numbers, trace flags, `sp_configure` options, wait types) and to cite
authoritative URLs.

It is strictly **optional enrichment**: decline it and the skill falls back to
built-in knowledge and `references/error-patterns.md` with no loss of function.

> **Already have the Microsoft Learn MCP** (e.g. via a separate `microsoft-docs`
> plugin or your user config)? Skip approving it here — registering the same
> remote server twice just loads its tool schemas into context twice.
