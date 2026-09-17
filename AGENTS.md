# db-ai-toolkit — instructions for coding agents

This file is the entry point for any coding agent working in this repository: Claude Code,
Codex, or anything else that reads `AGENTS.md`. Claude Code users additionally get the
`sqlserver-toolkit` plugin, whose skills carry the same rules in more detail; where the two
disagree, the skill is more specific and wins.

The repository is a Claude Code plugin marketplace plus the Go tools its plugins use. See
`README.md` for the layout and `docs/conventions.md` for the structural rules.

## Querying a live SQL Server instance

**All access goes through `sqlq`.** Do not write ad-hoc `sqlcmd` invocations, do not build a
connection string, and do not invent a server name. The guardrails — the write guard, the row
cap, the error redaction — live inside `sqlq`, and a second path would carry none of them.

```bash
sqlq -list-profiles                                    # always start here
sqlq -profile <name> -query "SELECT TOP (20) name FROM sys.tables ORDER BY name"
sqlq -profile <name> -file <path> -param name=dbo.Orders
sqlq -profile <name> -query "<sql>" -plan              # capture the actual execution plan
sqlq -profile <name> -query "<sql>" -maxrows 100       # default 50
sqlq -profile <name> -query "<sql>" -timeout 120       # default 30 seconds
sqlq -profile <name> -query "<sql>" -database Other    # override the profile's database
```

Every run prints one JSON object, on success and on failure alike:

```json
{"profile":"prod-erp","server":"SRV01","database":"ERP","elapsed_ms":42,
 "columns":[{"name":"n","type":"INT"}],"rows":[{"n":1}],
 "rowcount":1,"truncated":false,"messages":[],"plan":null,"error":null}
```

On failure `error` carries `{number, severity, state, line, procedure, message}`. Quote the
error number and line: that is what makes a SQL Server error diagnosable. Usage errors, `-help`
included, come back in this same shape: there is never a second format to parse.

`database` names the catalog the **connection** opened in. It is not a claim about every object
the query touched — a multi-part name can legitimately read elsewhere.

Exit codes: `0` success, `1` usage or configuration, `2` SQL error, `3` refused by the write
guard, `4` connection failure.

### Choosing a profile

`-list-profiles` returns `name`, `auth`, `readonly`, and sometimes `environment` and
`unusable`. It deliberately does **not** return server or database names: every session starts
with this call, so whatever it prints reaches a model provider every time, and host names,
database names and the group taxonomy are the estate map. A human who needs them reads
`%LOCALAPPDATA%\db-ai-toolkit\servers.json`.

- If the user named a profile, use it. If exactly one exists, use it and say which. If several
  exist and the question does not identify one, **ask** — do not guess which instance to touch.
- **`"environment": "prod"` — before the first query of a session against that profile, name it
  to the user and wait for an explicit yes.** The damage this prevents is not a write: it is a
  diagnostic query run against production while you believed you were on the staging copy.
  Production rows in the transcript, read locks on a busy instance, and a conclusion drawn from
  the wrong environment. Instances are commonly named PRD / STA / REC / DEV of the same
  application, in more than one country, so the names are nearly identical by design.
- **`"unusable"`** means the profile cannot run here — a DPAPI-backed profile on a non-Windows
  machine. Say so; do not try it.

### What not to do

- **Do not work around a refusal.** Exit code `3` means the batch would write. Report it and
  stop. Do not rephrase the SQL to slip past the check, and do not reach for `OPENQUERY`,
  `OPENROWSET` or `OPENDATASOURCE` — they are refused for the same reason.
- **Do not write on your own initiative.** Never pass `-allow-write`, and never suggest
  switching a profile to `readwrite`, to complete a task the user asked a *question* about.
  `-allow-write` needs the exact statement shown to the user and an explicit yes to *that
  statement*; a profile permitting writes is not consent.
- **Do not send `USE`.** It is refused. It writes nothing, so it slips past the write guard,
  but it changes the database for the rest of the batch — which makes the `database` field of
  the answer name a catalog the query did not run in, and overrides `-database` from inside the
  text that flag was meant to govern. Choose the catalog with `-database`, or name it in the
  object (`Other.dbo.T`), which stays allowed.
- **Do not send `GO`.** It is a batch separator belonging to SSMS and `sqlcmd`, not T-SQL.
  `sqlq` refuses a batch containing it. Send one batch per call.
- **Do not retry after error 18456.** A rejected login is the one failure where trying again
  causes harm: repeated failures can lock the account out, and a burst of failed administrator
  logins from a workstation is what credential stuffing looks like in the other team's security
  log. Report it and stop. The fix is the user's — correct the password in SSMS, then re-run
  the import.
- **Do not use `-dirty-reads` to answer a question about whether data is correct.** It runs at
  `READ UNCOMMITTED`, which permits dirty reads and missing or duplicated rows. That is the one
  question it answers wrongly, and silently.
- **Do not use `EXEC` or `DBCC`.** Both are refused on read-only profiles. Use
  `OBJECT_DEFINITION()` instead of `sp_helptext`.
- **Do not invent results.** If the query failed, say what the server returned.
- **Do not assume the version.** Some catalog objects and columns exist only from a given
  release; check `SELECT @@VERSION` before relying on one.

### Output discipline

- **Always bound the result** with `TOP (n)` in the query. `-maxrows` is a backstop, not a
  substitute: it truncates after the server has already done the work. If `truncated` is
  `true`, say so and give the real `rowcount`.
- **Never `SELECT *`** against a user table. Name the columns you need.
- **Never paste a large result verbatim.** Summarise, then show the rows carrying the answer.
- **Pass values with `-param`, never by string concatenation.**

### The guard is accident prevention, not security

`sqlq`'s write guard is a lexical filter on statement text. It stops a mistake; it does not
stop an agent that goes around the tool, and it removes no permission on the server. It also
protects **integrity only** — every `SELECT` passes it by design, and fifty rows of a personnel
table are fifty identifiable people. The control that matters is the login: a dedicated one
with `db_datareader`, `VIEW DEFINITION` and `VIEW SERVER STATE`, and nothing else.

### Profiles

Resolved in order: `-profiles <path>`, `$MSSQL_PROFILES`, then
`~/.config/db-ai-toolkit/mssql-profiles.json`.

```json
{
  "prod-erp":    { "server": "SRV01", "database": "ERP",
                   "auth": "integrated", "mode": "readonly" },
  "prod-legacy": { "server": "SRV02\\SQLEXPRESS,1433", "database": "L",
                   "auth": "sql", "user": "svc_claude",
                   "passwordEnv": "MSSQL_LEGACY_PWD", "mode": "readonly" }
}
```

`auth` is `integrated`, `sql` or `entra`. `mode` is `readonly` (the default when omitted) or
`readwrite`. A profile never contains a password: it names where the password lives, with
exactly one of `passwordEnv` (an environment variable) or `passwordDpapi` (a key into the
DPAPI-encrypted store, Windows only). `sqlq` refuses to load a file holding an inline
`password`.

Entries carrying `"managedBy": "registered-servers"` are generated and are rewritten on every
export; edits to them are lost. Entries without that marker are hand-written and never touched.

## Reading a SQL Server error log

`errorlog-parse` collapses an ERRORLOG into what is worth reading, at roughly a tenth of the
tokens. Prefer it over reading the raw file.

```bash
errorlog-parse <path>                      # severity >= 16, aggregated, with a summary
errorlog-parse -severity 10 <path>
errorlog-parse -redact <path>              # pseudonymize databases, logins and IPs
errorlog-parse -from 2026-09-01 -to "2026-09-17 14:30" <path>
errorlog-parse -format md <path>
```

## registered-servers

`registered-servers/` turns the SQL Server instances registered in SSMS into `sqlq` profiles,
keeping the passwords under DPAPI. Read `registered-servers/README.md` before touching it.

Three rules for an agent:

- **Never run `Import-RegisteredServerCredentials.ps1`.** It is the user's to run, and it is
  the one command in this repository that handles secrets.
- **Never read `credentials.json` or the SSMS `RegSrvr*.xml`.** Both hold encrypted passwords,
  and reading them only copies blobs into the transcript. A `PreToolUse` hook refuses this; the
  hook is accident prevention, not a boundary — do not look for a way round it.
- **Read `%LOCALAPPDATA%\db-ai-toolkit\servers.json` instead.** It is the same inventory with
  the secrets left out.

To refresh things after a change in SSMS, ask the user to run both scripts themselves.

## Building and testing

```powershell
./scripts/build-tools.ps1     # Windows: builds tools/cmd/* into plugins/*/bin/
./scripts/build-tools.sh      # macOS, Linux
```

```bash
cd tools
go test ./...
go vet ./...
```

`sqlq` has no fallback: if it is not built, the query path does not work, because the
guardrails live inside it. `errorlog-parse` degrades gracefully — its skill still works by
reading the raw log, at a higher token cost.

---

<!-- rtk-instructions v2 -->
# RTK (Rust Token Killer) - Token-Optimized Commands

## Golden Rule

**Always prefix commands with `rtk`**. If RTK has a dedicated filter, it uses it. If not, it passes through unchanged. This means RTK is always safe to use.

**Important**: Even in command chains with `&&`, use `rtk`:
```bash
# ❌ Wrong
git add . && git commit -m "msg" && git push

# ✅ Correct
rtk git add . && rtk git commit -m "msg" && rtk git push
```

## RTK Commands by Workflow

### Build & Compile (80-90% savings)
```bash
rtk cargo build         # Cargo build output
rtk cargo check         # Cargo check output
rtk cargo clippy        # Clippy warnings grouped by file (80%)
rtk tsc                 # TypeScript errors grouped by file/code (83%)
rtk lint                # ESLint/Biome violations grouped (84%)
rtk prettier --check    # Files needing format only (70%)
rtk next build          # Next.js build with route metrics (87%)
```

### Test (60-99% savings)
```bash
rtk cargo test          # Cargo test failures only (90%)
rtk go test             # Go test failures only (90%)
rtk jest                # Jest failures only (99.5%)
rtk vitest              # Vitest failures only (99.5%)
rtk playwright test     # Playwright failures only (94%)
rtk pytest              # Python test failures only (90%)
rtk rake test           # Ruby test failures only (90%)
rtk rspec               # RSpec test failures only (60%)
rtk test <cmd>          # Generic test wrapper - failures only
```

### Git (59-80% savings)
```bash
rtk git status          # Compact status
rtk git log             # Compact log (works with all git flags)
rtk git diff            # Compact diff (80%)
rtk git show            # Compact show (80%)
rtk git add             # Ultra-compact confirmations (59%)
rtk git commit          # Ultra-compact confirmations (59%)
rtk git push            # Ultra-compact confirmations
rtk git pull            # Ultra-compact confirmations
rtk git branch          # Compact branch list
rtk git fetch           # Compact fetch
rtk git stash           # Compact stash
rtk git worktree        # Compact worktree
```

Note: Git passthrough works for ALL subcommands, even those not explicitly listed.

### GitHub (26-87% savings)
```bash
rtk gh pr view <num>    # Compact PR view (87%)
rtk gh pr checks        # Compact PR checks (79%)
rtk gh run list         # Compact workflow runs (82%)
rtk gh issue list       # Compact issue list (80%)
rtk gh api              # Compact API responses (26%)
```

### JavaScript/TypeScript Tooling (70-90% savings)
```bash
rtk pnpm list           # Compact dependency tree (70%)
rtk pnpm outdated       # Compact outdated packages (80%)
rtk pnpm install        # Compact install output (90%)
rtk npm run <script>    # Compact npm script output
rtk npx <cmd>           # Compact npx command output
rtk prisma              # Prisma without ASCII art (88%)
```

### Files & Search (60-75% savings)
```bash
rtk ls <path>           # Tree format, compact (65%)
rtk read <file>         # Code reading with filtering (60%)
rtk grep <pattern>      # Search grouped by file (75%). Format flags (-c, -l, -L, -o, -Z) run raw.
rtk find <pattern>      # Find grouped by directory (70%)
```

### Analysis & Debug (70-90% savings)
```bash
rtk err <cmd>           # Filter errors only from any command
rtk log <file>          # Deduplicated logs with counts
rtk json <file>         # JSON structure without values
rtk deps                # Dependency overview
rtk env                 # Environment variables compact
rtk summary <cmd>       # Smart summary of command output
rtk diff                # Ultra-compact diffs
```

### Infrastructure (85% savings)
```bash
rtk docker ps           # Compact container list
rtk docker images       # Compact image list
rtk docker logs <c>     # Deduplicated logs
rtk kubectl get         # Compact resource list
rtk kubectl logs        # Deduplicated pod logs
```

### Network (65-70% savings)
```bash
rtk curl <url>          # Compact HTTP responses (70%)
rtk wget <url>          # Compact download output (65%)
```

### Meta Commands
```bash
rtk gain                # View token savings statistics
rtk gain --history      # View command history with savings
rtk discover            # Analyze Claude Code sessions for missed RTK usage
rtk proxy <cmd>         # Run command without filtering (for debugging)
rtk init                # Add RTK instructions to CLAUDE.md
rtk init --global       # Add RTK to ~/.claude/CLAUDE.md
```

## Token Savings Overview

| Category | Commands | Typical Savings |
|----------|----------|-----------------|
| Tests | vitest, playwright, cargo test | 90-99% |
| Build | next, tsc, lint, prettier | 70-87% |
| Git | status, log, diff, add, commit | 59-80% |
| GitHub | gh pr, gh run, gh issue | 26-87% |
| Package Managers | pnpm, npm, npx | 70-90% |
| Files | ls, read, grep, find | 60-75% |
| Infrastructure | docker, kubectl | 85% |
| Network | curl, wget | 65-70% |

Overall average: **60-90% token reduction** on common development operations.
<!-- /rtk-instructions -->
