# Review of the query catalogue plan (Claude, neutral prompt)

Plan `docs/superpowers/plans/2026-10-04-query-catalog.md` at 3104776, against spec v2, the code
and the driver. Every task's code blocks were pasted verbatim into a copy of `tools/` and
`plugins/` under the scratchpad and run with the plan's own filters; the descriptive parts of
tasks 1, 9 and 10 (main.go wiring, `SavedRun`) had to be written by me to get a compiling
tree, which is itself a finding (defect 12). No SQL Server was used.

## Verified by running

### 1. `Rewrite` corrupts the batch when the marker lists parameters out of file order, or twice

`Rewrite` walks `params` "from the last offset to the first, so earlier offsets stay valid",
but `AnalyseOverrides` returns them in the order of the marker's `params=`, not in offset
order, and `parseMarker` does not deduplicate. A marker `params=top_n,hostname` on a file that
declares `@hostname` first is accepted by the catalogue, and `prepareSaved` then produces:

```
valid entry: true
err=<nil> args=[{{} sqlq_hostname SRV-APP01} {{} sqlq_top_n 5}] run={... Params:map[hostname:SRV-APP01 top_n:5] Defaults:[] ...}
DECLARE @hostname sysname = @sqlq_hostname;
DECLARE @t @sqlq_top_nn int = 20;
refusals on rewritten: []
```

and `params=hostname,HostName` gives `DECLARE @hostname sysname = @sqlq_hostnameq_hostname;`.
With `@b nvarchar(100) = N'a long default value here'` declared after `@a` and the marker
saying `b,a`, the text becomes `DECLARE @b nvarchar( @sqlq_b here';`, an unterminated literal
that swallows the rest of the batch. In every case I built, the server answers with a syntax
error rather than a wrong row, but the agent receives an exit 2 on a catalogued, "valid"
entry, which is exactly the situation the skill forbids it to work around, and the guard
re-run on the rewritten text does not notice anything. No test catches it: I reversed the
loop in `Rewrite` (`for i := 0; i < len(params); i++`) and all 12 tests of task 5 stayed
green, because no test passes two parameters. The real corpus has the shape that triggers it
(`powershell/daily-check/sql/job-exceptions.sql` declares four overridable `INT` on
consecutive lines). Sorting by `start` in `AnalyseOverrides` and refusing a repeated name in
`parseMarker` closes both, plus a test with two passed parameters in reverse marker order.

### 2. `smalldatetime` and supplementary characters: the reported value is not the value the server used

`BindValue` accepts `2026-10-04T10:30:45` for `smalldatetime` and returns
`civil.DateTime 2026-10-04T10:30:45` (run). `smalldatetime` has minute precision and, per the
documentation, rounds 29.999 seconds and more up, so the server uses 10:31 while
`saved.params` reports `10:30:45`, the silent-substitution class §11 exists to prevent. The
spec's own table admits `[:SS]` for `smalldatetime`; either refuse seconds for that type or
document the rounding. Same family, lower probability: the length check counts runes
(`utf8.RuneCountInString`), and `BindValue(nvarchar(10), 10 x "😀")` is accepted (run), while
`nvarchar(n)` counts UTF-16 code units (documentation), so a supplementary character counts
twice and SQL Server truncates without error.

### 3. `TestRealCloneHasNoRejectedEntry` cannot see a collision with the bundled canon

The check the spec gives the user after posing markers (§15, documented in tsql-scripts'
`CLAUDE.md` by task 12) loads `CatalogConfig{TsqlScriptsDir: dir}` only. With a fake clone
holding `-- sqlq: name=tables-largest`:

```
$ DB_AI_TOOLKIT_TSQL_SCRIPTS=$D go test ./internal/sqlq -run '^TestRealCloneHasNoRejectedEntry$' -count=1 -v
--- PASS: TestRealCloneHasNoRejectedEntry (0.00s)
$ sqlq -list-queries -queries ../plugins/.../queries -tsql-scripts $D
  {"name": "tables-largest", "source": "tsql-scripts", "path": "d/t.sql",
   "rejected": "name \"tables-largest\" is taken by bundled"}
```

The design already names the trap (`missing-indexes` exists three times). Task 12 step 3
happens to run `-list-queries` as well, so the first batch would be caught, but every later
use of the documented check certifies a script that the real catalogue rejects. The test
should load the bundled directory (`bundledQueriesDir` is available in the package).

### 4. `-list-queries` publishes SQL extracts and local paths through `params[].default`

The plan's Global Constraint says nothing `-list-queries` prints contains an absolute path,
a database name or an extract of SQL, and §12 "Ce qui est publié" lists only summaries and
file names as author-controlled text. But the catalogue prints every default verbatim. Running
`AnalyseOverrides` on the real clone accepts, among the 45 lines,
`@file nvarchar(max) = N'C:\temp\blocked_processes*.xel'` (an absolute path),
`@RunDateStart DATETIME = DATEADD(hour, -@LookbackHours, GETDATE())` (SQL), and
`@table_name sysname = N'MyTable'`. The rejected lines show what the conversion §15 asks for
would publish next: `@DatabaseName as sysname = 'PachadataFormation'`,
`@procedure_name SYSNAME = 'dbo.ps_CountTiersEncaissement'`. The rule task 12 writes into
tsql-scripts' `CLAUDE.md` only forbids client, host and database in the summary or the name.
`TestListQueriesPublishesNoPathServerOrSQL` forbids `SELECT` and the clone's absolute path,
neither of which a default carries. This is a contradiction between §12's example and §12's
publication rule, so it needs a decision (publish the type only, or extend the author rule to
defaults), not just code.

### 5. Task 1 does not compile as listed

Deleting `refusals` from `bundled_queries_test.go` breaks
`missing_indexes_query_test.go:20`, which the task does not list:

```
vet: internal/sqlq/missing_indexes_query_test.go:20:10: undefined: refusals
FAIL github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq [build failed]
```

The fix is a two-line edit of that file (`Refusals(...)`, `r[0].Reason()`), which the
Files list and the commit's `git add` must include. After it, step 4 gives exactly the 4
announced tests.

### 6. Task 4's break step does not break anything, and its fallback case does not either

Removing `t.Depth == base` from the comma test leaves
`TestDeclaredVariableIsOnlyTheDeclareTarget` green, as the plan anticipates; the fallback it
prescribes (`DECLARE @a nvarchar(10) = LEFT(@src, 3);`, expect `src`) is also green under the
mutation (run: both PASS), because the token after the inner comma is `3`, not a variable.
The implementer is told to "verify it falls then" and cannot. A case that does fall is
`LEFT(@src, @len)` (expect `src,len`). Without it the depth rule of §9 ships untested.

### 7. The `.git` fixture is silently not committed, so the `.git` exclusion is untested in any fresh checkout

In a scratch repository, `git add t` with `t/.git/ignored.sql` prints nothing and stages
nothing (`git status --porcelain -uall` empty). The plan says "vérifier que git add
l'accepte ... s'il est refusé, le créer dans le test avec t.TempDir()": there is no refusal
to see, so the implementer will likely conclude it was accepted, and
`TestCatalogListsExactlyTheFilesPresent` passes on a clean clone whether or not
`loadTsqlScripts` skips `.git`. The fixture should be created in a `t.TempDir()`
unconditionally.

## Concluded by reading

### 8. The integration test profile cannot connect to the container (hypothesis)

The profile the controller is told to write has no `trustServerCertificate`, so `DSN`
sends `encrypt=true&trustservercertificate=false`. The `mssql/server:2025-latest` container
presents a self-signed certificate; go-mssqldb then fails certificate validation (driver
behaviour from its documented TLS handling, not run). The plan's own readiness probe passes
`-C` to sqlcmd for exactly this reason. Every integration step (tasks 2, 9, 10, 12) would end
in exit 4. Adding `"trustServerCertificate": true` to the profile is enough.

### 9. Task 12 writes into the user's real registry, and integration tests can read the real clone

Task 12 step 4 runs `-saved` without `DB_AI_TOOLKIT_REGISTRY`, so `RecordVerified` writes to
`~/.config/db-ai-toolkit/verified.json` entries attributed to the throwaway profile
`catalog-test`; step 5's cleanup removes the container and the profile file but not those
entries, and from then on every bundled query shows `verified` on this machine on the
strength of a container run. Separately, `TestNothingIsSavedWhenTheRunFailed` and
`TestSaveWritesVerifiedEntry` do not blank `DB_AI_TOOLKIT_TSQL_SCRIPTS`, which task 12
exports and which the user is told to set: they then walk the real clone, against the
Global Constraint.

### 10. A missing bundled directory is silent

`loadBlockDir` globs a missing directory and returns nothing, with no message, whereas §7
adds a message for an absent tsql-scripts source precisely "pour que l'agent ne conclue pas
que le dépôt ne contient rien". `bundledQueriesDir()` depends on the binary sitting in the
plugin's `bin/`; a binary built elsewhere (`go build -o /tmp/sqlq-catalog`, as task 12 itself
does without `-queries` in step 4's runs if the implementer forgets it) yields a catalogue
without the canon, and the skill then routes the agent to ad hoc SQL. A
`bundled queries not found` message would make it visible.

### 11. The registry's error message publishes an absolute path

`LoadRegistry` returns `"verification registry unreadable, treated as empty: " +
err.Error()`, and `os.ReadFile` errors carry the full path
(`open /home/<user>/.config/db-ai-toolkit/verified.json: ...`). Task 9 appends that message
to the catalogue's `messages`, which the Global Constraint forbids. Not covered by
`TestListQueriesPublishesNoPathServerOrSQL`, which never routes a registry message.

### 12. Spec tests absent from the plan, and steps that describe instead of showing

Three tests of §16 have no counterpart: `TestBoundQueryRefusedOnAnotherProfile`
(`missingQueryError` exists, untested), `TestParametersLineIsOptionalButChecked` (the
"header that lies" half: no fixture has a `Parameters:` line that disagrees with the SQL, so
the rejection in `blockEntryProblem` is untested), and `TestVerifiedHashesBytesThatRan`.
Steps given as prose where a subagent needs code: task 1's `Refusals` loop in `run`; task 9's
`readQuery` change and the `-saved` block, which assigns `named` before the existing
`named, err := namedArgs(...)` declares it (pasted as is, it does not compile); `SavedRun`
and its `MarshalJSON`; the three `t.Setenv` lines and the `path/filepath` import of
`catalog_cli_test.go` (task 9 lists only `io` and `os`, task 10's `saveEnv` needs
`filepath` too); the `sessions-from-host.sql` fixture "suivi de" a SET. Last, the doc check
`TestEveryFlagIsDocumented` matches `-queries\b` inside `-list-queries`, so after task 11 it
will count `-queries` as documented whether or not it is.

Set aside (7): the `Parameters:` comparison is order-sensitive (false rejection, visible);
variable names on a case-sensitive instance collation; the stand-alone check in
`analyseOne` is redundant with `parseDeclaredType` (mutation survives, harmless); a possible
stall when more than 15 informational messages queue between two tokens (sqlexp queue of 15,
not demonstrated); the readiness `until` loop has no timeout; the setup's `sqlcmd` probe
against AGENTS.md's "no ad hoc sqlcmd"; a rejected bundled entry's reason overwritten by
"defined more than once" on a collision.

## Not a problem

- Tasks 3, 5, 6, 7 and 8 compile verbatim and give exactly the announced counts (10, 12, 3, 12, 2).
- Task 2 compiles; `go mod tidy` moves `sqlexp` (task 2) then `civil` (task 5) to direct `require` and leaves `go.sum` unchanged.
- The message loop matches `Rowsq` in go-mssqldb v1.11.0: non-fatal errors reach only `MsgError`, `HasNextResultSet` is always true, and a showplan read without draining stays in step.
- The timeout check after the loop is needed and right: `Message` returns `MsgNextResultSet` on a done context.
- `fODBC` is set at login, so `SELECT 1/0` does raise 8134 as the integration tests expect.
- `Refusals` agrees with the three guards; on the real clone it passes 214 of 344 files, the spec's figure.
- Of the DECLARE lines in guard-passing files, 45 are accepted, matching §3; every refusal (`as`, missing `;`, multi-line, `xml`, `uniqueidentifier`) is one the spec explains, and every accepted variable that reappears with `=` is a comparison.
- `ParseBlockHeader` gives a full one-sentence summary for all 7 bundled queries, and their `Parameters` lines match `QueryParams`.
- The marker hash excludes the marker line, so a rename or `heavy` edit keeps `verified`, and a SQL edit drops it (tests pass, mutations fall).
- Task 11's expected 12 documentation failures is exact.
- `profile.go:192` and `profile.go:300` are where the plan says; `DefaultProfilePath` is under `~/.config/db-ai-toolkit/`.
- Both repositories were left clean (`git status --porcelain -uall` empty); scratch copies deleted.
