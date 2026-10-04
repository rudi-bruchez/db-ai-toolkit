# Review of `2026-10-04-query-catalog-design.md` (fifth reader, neutral prompt)

Repository read at commit 2811bfc (branch `feat/query-catalog`). tsql-scripts read at d5a9376.
All Go experiments ran in a copy of `tools/` under
`scratchpad/panel/claude-work/tools/cmd/zzreview*`, calling the real `sqlq.Sanitize`,
`FindWrites`, `FindBatchSeparators`, `FindContextChanges` and `Statements`; the programs were
deleted afterwards. Both live repositories were checked clean with `git status` at the end.
No SQL Server was contacted.

The §3 baseline reproduces exactly: 344 files, 214 pass, 98 GO, 38 USE, CREATE 73, ALTER 29,
INSERT 19, INTO 14, EXEC 24 + EXECUTE 14. Among the 214 passing files, 45 contain a `DECLARE`.

## Verified by running

### 1. The DECLARE rewrite rule changes the query silently when the initializer does not end the line

§9 says the replaced portion "commence après le `=` et finit au premier `;` de la même ligne,
ou à la fin de la ligne", and the only structural refusals are multi-variable lines and
unbalanced parentheses. I implemented that rule verbatim (regex on `Sanitize` output, rune
offsets, cut at first `;` or `\n`) and ran it on two inputs that pass every refusal of §9:

```go
src := "-- sqlq: name=tables-like params=pattern\nDECLARE @pattern nvarchar(200) = N'%'\n    + N'Orders%';\nSELECT TOP (20) name FROM sys.tables WHERE name LIKE @pattern;\n"
src2 := "DECLARE @top_n int = 20 SELECT TOP (@top_n) name FROM sys.tables;\n"
```

```
DECLARE @pattern nvarchar(200) = @sqlq_pattern
    + N'Orders%';
SELECT TOP (20) name FROM sys.tables WHERE name LIKE @pattern;
reassigned: false writes: 0
one-line declare+select rewritten: "DECLARE @top_n int = @sqlq_top_n;\n" statements=1
```

In the first, the initializer continues on the next line with an operator: the rewrite yields
valid T-SQL in which the passed value is concatenated with `N'Orders%'`, so the agent believes it
filtered on its value and the server filtered on something else. In the second (no `;` after the
DECLARE, statement on the same line), the portion runs to the first `;`, which belongs to the
SELECT: the SELECT is deleted, the batch returns no result set, and by the existing `collect`
code this would most likely come back as exit 0 with zero rows (inferred from `main.go`, not run
against a server), which also writes a `verified` entry. Neither case is caught by the guard
re-run on the rewritten text. The same rule also leaves an empty portion unrefused
(`DECLARE @file nvarchar(max) =` at end of line, present in `extended-events/on-prem/blocked-processes-read.sql:33`
and `lock-escalation-read.sql:31`; those two happen to be caught by the `SET @file` rule). The fix
is to make the rule positive instead of listing failure modes: accept only a line of the form
`DECLARE @p <type> = <expr>;` where the `;` closes the declaration and nothing but whitespace
follows on that sanitized line, and the next non-blank sanitized token does not continue the
expression; reject everything else. The current test `TestUnbalancedInitializerIsRejected` does
not cover either case, since both are balanced.

### 2. The reassignment refusal, as listed, misses the forms that occur in real T-SQL

§9 calls the reassignment case "le plus important des cinq" and lists `SET @p =`, `SELECT @p =`,
and `FETCH … INTO @p`. Implemented as listed (case-insensitive, on sanitized text):

```
refused=false  "SELECT TOP (1) @db = name FROM sys.databases WHERE database_id = 1;"
refused=false  "SELECT @x = 1, @db = N'master';"
refused=false  "SET @n += 100;"
refused=true   "SELECT @DB = N'master';"
refused=true   "SET @db=N'master';"
```

`SELECT TOP (1) @p = …` is the standard way to fill a variable from a query, a variable later in
a multi-assignment SELECT list is equally common, and compound assignment (`+=`, `-=`, …) is
another write. All three reproduce the exact silent failure the design names (a result reported
for ERP, computed on another value). The blunt alternative, refusing any `@p =` outside the
DECLARE, is not acceptable either: run over the 63 initialized declarations of the 214 passing
files, it hits 13 variables, 9 of them comparisons such as `IIF(@online = 1, …)` or
`OR @onlyAuto = 0` (the other 4 are genuine `SET @x =`). The rule needs to be stated as "an
assignment target in a SET statement (any assignment operator) or anywhere in the select list of
a SELECT that has no result-set column before FROM", and the test needs those three inputs. A
case-sensitive implementation would also miss `SET @DB =` against `DECLARE @db`; T-SQL variable
names are case-insensitive under a case-insensitive collation, so the document should require
case-insensitive matching everywhere a variable name is compared.

### 3. A `-summary` containing `/*` produces a saved query that is entirely a comment, and it is recorded as verified

`-save-query` writes "un bloc contenant le résumé et la ligne `Parameters:` … puis le SQL
exécuté", with the summary taken verbatim from `-summary`. Built exactly that way with the
summary `Tables under the /* archive schema`:

```
sanitized non-blank: ""
statements: 0 writes: 0
```

`Sanitize` nests block comments, and so does SQL Server (documented: "Nested comments are
supported. If the /* character pattern occurs anywhere within an existing comment, it is treated
as the start of a nested comment"), so the whole file, SQL included, is one unterminated comment.
The save itself succeeds, because what ran was the `-query` text, not the file; §8 then writes
the hash of the file on disk into the registry, so the catalog shows `verified` for a file whose
content has never run. Every later `-saved` returns no result set: most likely exit 0 and zero
rows, a silent "nothing found". Whether the catalog flags it depends on the header parser: one
that scans for the first `*/` reads a valid header and lists the entry as healthy; one that uses
nested-comment logic reports "en-tête illisible". The document does not say which. A summary
containing `*/` is worse in kind (`"Orders */ SELECT 1 AS x; /* late"` gives the statements
`SELECT 1 AS x` and the real query), though it would probably fail loudly. Fix: refuse a
`-summary` containing `/*`, `*/` or a line break, and either hash the bytes of the written file
only after re-running guard and parse on them, or state that `-save-query` verification covers
the SQL body only.

### 4. Scripts returning several result sets will be answered from the first one only

`sqlq` returns only the first result set (SKILL.md, "Known limits"; `collect` in `main.go`
drains the rest). The design never mentions this, yet SSMS scripts commonly return several.
Counting result-producing statements (from `sqlq.Statements`, SELECT or WITH that is not a
variable assignment) in the 214 passing files:

```
MULTI-RS 2 cloud/azure/azure-sql-database/service-level-info.sql
MULTI-RS 3 database-administration/ddl-generation/text-to-varchar-max.sql
MULTI-RS 2 database-information/database-collations.sql
MULTI-RS 2 database-information/in-memory/in-memory-consumers.sql
MULTI-RS 3 database-information/size-and-allocation/filegroup-analysis.sql
MULTI-RS 2 hadr/log-shipping-metadata.sql
MULTI-RS 3 security/permissions-audit.sql
MULTI-RS 4 server-information/cores-and-numa.sql
multi-result-set files among passing: 8
```

`cores-and-numa.sql` (read by hand) returns CPU per NUMA node, then the sys_info row, then the
scheduler list, then memory per node: marked as it stands, the agent receives the first and
answers "memory per NUMA node" questions from nothing, with no indication that three sets were
dropped. The catalog should compute the number of result-producing statements and either reject
an entry with more than one (consistent with "un script refusé reste dans SSMS") or expose
`result_sets` and have the run report how many sets were drained.

### 5. A marker that is almost right makes the script vanish, the failure `rejected` exists to prevent

§7 says a header without a marker makes the file absent, "pas même en `rejected`", and the marker
must start with `-- sqlq:` inside the contiguous `--` block opening the file. With the rule
implemented as written:

```
no BOM: true  with BOM: false
```

A UTF-8 BOM before the first `--` (what SSMS writes when it saves "with signature") makes the
header undetected; so would `--sqlq:`, `-- SQLQ:`, `-- sqlq :`, or a marker placed after a blank
line inside a two-part header. All of these are silent absences, and the agent then concludes no
stored query exists and writes ad hoc SQL. The design's own argument for `rejected` ("Sans ce
champ, un marqueur posé sur un script contenant `GO` le ferait disparaître sans explication")
applies equally here, and `TestRealCloneHasNoRejectedEntry`, the user's only check after
marking, cannot see an entry that is absent. Fix: strip a BOM; treat any line matching
`(?i)^\s*--\s*sqlq\s*:` anywhere in the file as a marker attempt, which is either accepted (inside
the header, exact form) or listed as `rejected` with the reason.

### 6. `dirty_reads` misses the `READUNCOMMITTED` table hint

§10 defines `dirty_reads` as "le texte nettoyé contient `READ UNCOMMITTED` ou `NOLOCK`". Applied
verbatim:

```
database-information/columnstore/wait-stats-azure.sql dirty_reads = false
diagnostics/Memory/plan-cache-usage.sql dirty_reads = true
hint only: false
newline between words: false
```

Nine passing files contain `READUNCOMMITTED` (one of them only inside a dynamic-SQL literal), the one-word hint equivalent to NOLOCK;
`wait-stats-azure.sql` uses only the hint and is reported `false`. `READ` and `UNCOMMITTED`
separated by a newline or two spaces is also missed. The design presents this field as
"calculé, donc impossible à oublier", which is what makes a false `false` dangerous: the skill
treats it as the signal that the result is safe for a correctness question
(`number-of-NULL-in-table.sql` reads user tables, as §12 notes). Fix: decide on tokens, not
substrings: the token `NOLOCK` or `READUNCOMMITTED`, or the token `READ` followed by the token
`UNCOMMITTED`.

### 7. The design's worked examples contradict the repository they describe

The catalog example in §10 and the header example in §7 use
`diagnostics/wait-statistics/waits-statistics.sql` as a healthy, verified entry with
`params=database_name,top_n`. The real file is refused by the guard (`GO` at line 9, right after
the `SET` preamble), contains no `@` variable at all, and its first header content line, the
summary under §7's rule, is `copied from https://www.sqlskills.com/...`. The example of a
rejected entry, `diagnostics/IO/dm_io_virtual_file_stats.sql` "batch separator GO at line 41",
has 33 lines and passes the guard (it is in my list of 214). Separately, under §7's summary rule
four passing files would get the summary `rudi@babaluga.com, go ahead license`
(`diagnostics/Memory/resource-semaphore.sql`, `diagnostics/query-store/longest-queries-in-a-period.sql`,
`hadr/log-shipping-metadata.sql`, `monitoring/monitor-backup-operations.sql`), because their
header has no description line; "résumé absent" does not catch it. The examples are what the
implementer and the skill author will copy into test fixtures and prose, so they should come from
files that actually behave as shown, and the summary rule should skip the license line.

## Concluded by reading

### 8. A `-param` value can be silently truncated or reinterpreted by the declared type

§9 delegates conversion to the server and §15 notes that values arrive as `nvarchar`; the design
argues only the loud case (an invalid `bit` or `int` raises an error). Assignment to a shorter
character variable truncates without error (CAST and CONVERT documentation: values too long for
the new type are truncated), and `nvarchar` to `varchar` replaces characters outside the code page
with `?`. A real passing candidate: `index-management/missing-indexes.sql` declares
`@compressionType varchar(10) = 'ROW'`; `-param compressionType=COLUMNSTORE_ARCHIVE` would run
with `COLUMNSTOR`, silently. For `datetime` and `smalldatetime` (not `datetime2`), the string
`yyyy-mm-dd` is interpreted according to `DATEFORMAT`/`LANGUAGE` of the login (documented
behaviour), so `2026-03-04` can become 3 April under a French login. The rewrite already locates
`<type>`; the catalog should publish it and `sqlq` should refuse, before connecting, a value longer
than a declared character length and a non-ISO-8601 (`yyyy-mm-ddThh:mm:ss`) date for
`datetime`/`smalldatetime`. Documentation-based; not run against a server.

### 9. `-list-queries` has no profile, but visibility, collisions and scope are defined per profile

§6 defines the visible set "depuis un profil P" and makes collisions, hence `rejected`, a function
of P; §10 says `-list-queries` "n'exige pas de profil". Two readings. If it lists only the
profile-independent sources when no profile is given, the agent never sees its own saved queries
(the skill rule is "appeler `-list-queries` avant d'écrire du SQL"), and the whole save path is
invisible unless the skill also passes `-profile`, which the document does not say. If it lists
every `personal/<profil>/` directory, an entry's `rejected` status cannot be a single field (a
name can collide from profile A and not from B), and every profile's model-written summaries go
to the provider at each session, which §8 says must obey the `-list-profiles` rule. The document
should say that `-list-queries` takes `-profile` when given, lists exactly the set visible from
it, and without it lists only generic sources plus a message saying profile-scoped entries were
not listed.

### 10. Profile names become path components without validation

Query names are validated before building a path, but `-save-query` writes under
`personal/<profil>/` and the scope comes from the directory name. Profile names are arbitrary
JSON keys in a hand-written file (only the generated ones are slugs, via
`ConvertTo-RegisteredServerSlug`). A profile named `_generic` aliases the generic directory, so a
save on it creates a generic-scope query; a name with `/` or `..` writes elsewhere; and on
Windows and macOS, the primary platforms here, `Prod-ERP` and `prod-erp` share one directory, so a
query bound to one profile is visible from the other, which is the "bonne réponse à la mauvaise
base" class `TestBoundQueryRefusedOnAnotherProfile` claims to remove. Fix: apply the name regex
(or a refusal of `_generic` and of names differing only by case) to profile names before using
them as a directory.

Set aside (three, lower cost): `params` in the `-saved` result uses the string `"default"` for a
parameter not passed, indistinguishable from `-param x=default`; concurrent `sqlq` runs doing a
read-modify-write of `verified.json` lose entries despite the atomic rename (errs on the prudent
side); and the multi-variable refusal is underspecified: implemented as "a comma followed by `@`
in the portion" it falsely rejects `powershell/daily-check/sql/job-exceptions.sql:17-18`
(`CONVERT(INT, CONVERT(VARCHAR(8), @RunDateStart, 112))`), while `DECLARE @x AS int = …`
(`030.ola-check-log-in-period.sql`) is rejected under a literal reading of `DECLARE @p <type> =`;
both are loud.

## Not a problem

- The §3 measurement reproduces exactly with the real guard (344, 214, 98, 38, and every keyword count).
- `Sanitize` preserves the rune count on all 214 passing files: rune-offset rewriting onto the original text is sound, as §9 assumes.
- Literal defaults blank out in the sanitized portion, so a `;` inside `N';'` does not cut the portion, provided the cut is made on sanitized text as §9 states.
- The parameter derivation rule ("`@x` referenced and not declared") applied to the seven bundled queries gives exactly what their headers say: `@name` for the three that declare it, none for the others, `missing-indexes` and `blocked-processes-check` ("No parameter.") included.
- The guard already ignores `-- sqlq:` marker lines and the `rudi@babaluga.com` header line, since both are comments.
- Binding through `sql.Named("sqlq_x", …)` reuses the existing `namedArgs` path; no concatenation is introduced.
- No tsql-scripts file is UTF-16 or carries a BOM today, and only one uses CRLF (`follow-a-session_id.sql`), so finding 5 is a future hazard rather than a present one.
- `-save-query` refusing any `FindWrites` hit regardless of profile and `-allow-write` is consistent with AGENTS.md.
- The `verified` registry recording a profile name only, not server or database, matches the `-list-profiles` rule in AGENTS.md.
- The current `SET @file =` reassignments in the extended-events scripts and `SET @sql_to_search =` in `query-hints-set.sql` are all caught by the listed `SET @p =` pattern.
