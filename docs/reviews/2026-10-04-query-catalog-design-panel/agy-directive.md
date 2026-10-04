## Verified by running

**1. DECLARE override deletes subsequent statements on the same line (Silent wrong execution)**
If a variable is initialized with a string containing a semicolon, and followed by another statement on the same line (e.g., `DECLARE @p VARCHAR(10) = 'a;b' SELECT 2`), `sqlq.Sanitize` blanks the string. The rewrite logic then fails to find the semicolon and replaces until the end of the line. The rewritten SQL becomes `DECLARE @p VARCHAR(10) = @sqlq_p`, silently deleting the `SELECT 2` statement.
*Evidence:* A throwaway Go program calling `sqlq.Sanitize` and mirroring the §9 logic produced exactly `DECLARE @p VARCHAR(10) = @sqlq_p` from the input `DECLARE @p VARCHAR(10) = 'a;b' SELECT 2`.

**2. Executability mismatch: multiple result sets are silently discarded (Silent missing data)**
The design takes passing the guard as evidence a script gives a usable answer. However, `sqlq` only collects the first result set (`rows.NextResultSet()` is drained but not reported). 69 of the 214 "passing" scripts contain multiple `SELECT` statements. The agent will silently miss the second and subsequent tables of diagnostic information, rendering many of these scripts useless.
*Evidence:* `tools/cmd/sqlq/main.go` explicitly states `// Extra result sets beyond the first are drained, not reported`. The Go analyzer counted >1 `SELECT` in 69 of the 214 passing scripts.

**3. DECLARE override breaks syntax on multi-line initializers (Loud failure)**
When an initializer spans multiple lines (e.g., `DECLARE @newFolder NVARCHAR(MAX) =\n 'F:\TempDB\';`), the rewrite replaces the text from `=` to the end of the first line. The rewritten SQL becomes `DECLARE @newFolder NVARCHAR(MAX) = @sqlq_newFolder\n 'F:\TempDB\';`, which is invalid T-SQL.
*Evidence:* The Go analyzer running against `tsql-scripts` found multiline DECLAREs in `move-tempdb-files.sql`, `stored-procedure-stats.sql`, `blocked-processes-read.sql`, and `lock-escalation-read.sql`.

**4. Header parsing ignores scripts starting with USE or SET (Silent omission)**
The rule in §7 states the header must be "le bloc de lignes -- contigu qui ouvre le fichier". If a script starts with `USE [msdb]` or `SET NOCOUNT ON;` followed by the `--` block, it does not "open" the file. The parser will not see it as a header, and the script will be silently excluded from the catalog even if marked.
*Evidence:* The Go analyzer found 11 such files that pass the guard, including `020.sql-agent-jobs.sql` (starts with `USE [msdb]`) and `delete-event-files.sql` (starts with `SET NOCOUNT ON;`).

## Concluded by reading

**5. Name collisions disable bundled queries (Denial of service)**
The rule in §6 states that "Un nom présent deux fois dans cet ensemble rend les deux entrées rejected." This means a user creating a personal query named `tables-largest` will cause both their query AND the bundled `tables-largest` to be marked `rejected`. If the `live-query` skill relies on `tables-largest` to answer a standard question, the skill will break because it is instructed not to run `rejected` queries.

**6. Registry race condition (Data loss)**
In §8, the registry is updated by reading `verified.json`, modifying it in memory, writing a temp file, and renaming. If two `sqlq -saved` runs finish simultaneously, they will both read the old JSON and write their own temp files. The second `rename` will overwrite the first, silently losing the verification record of the first script.

**7. Inconsistent parameter writing for `-save-query`**
§7 states that `-save-query` writes a block containing the summary and the `Parameters:` line, "fournis par l'agent par -summary <texte>". However, `-summary` is a single CLI argument. Expecting the agent to format and inject a `Parameters: ` line inside the `-summary` flag contradicts the goal of delegating formatting to `sqlq` and the flag's intended purpose.

**8. Disclosure in `-list-queries` through filenames**
§10 states `path` is printed for all catalog entries. For personal scripts, the filename is printed. If a user names a file `check-ERP-SRV01.sql`, the server name is broadcast to the LLM on every session start, violating the intent of the profile masking rules from `AGENTS.md`.

**9. Inconsistent `Heavy: yes` rule**
§12 claims that `Heavy: yes` is carried over from the 2 September design. However, the 2 September design (`2026-09-02-query-catalog-design.md`) and the current `SKILL.md` never mention `Heavy: yes`.

## Not a problem
- `dirty_reads` lexical detection of `READ UNCOMMITTED`: Checked, all occurrences in `tsql-scripts` use exactly one space.
- BOM/UTF-16 causing header parsing failures: Checked, no `.sql` files in `tsql-scripts` use BOM or UTF-16.
- `SET @p +=` and compound assignments: These alter the passed value, but correctly execute the script writer's intent.
- The hash of an edited file breaking `verified`: If edited after running, the new hash won't match the old run, safely marking it unverified.
- The registry key mapping a moved clone: The path is relative to the clone, so verification legitimately survives moving the clone directory.
- `sqlq` executing `SET NOCOUNT ON;`: Harmless, as `sqlq` already runs it in the preamble.
