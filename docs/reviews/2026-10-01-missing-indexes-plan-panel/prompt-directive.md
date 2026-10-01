# Review of an implementation plan — no code exists yet

You are reviewing an IMPLEMENTATION PLAN. Nothing has been built from it. Your job is to find
what is wrong with it before it is executed. The plan carries the full text of a T-SQL query, a
Go test, and a Markdown reading protocol; a defect found now costs an edit, and some of these
defects would produce plausible wrong numbers rather than errors.

## Reading order

1. `docs/superpowers/plans/2026-10-01-missing-indexes.md` — the plan under review (French prose,
   English code).
2. `docs/superpowers/specs/2026-10-01-missing-indexes-design.md` — the spec it implements,
   already reviewed once (its §10 lists what that review found and how it was handled).
3. `AGENTS.md` — binding rules for anything that talks to SQL Server through `sqlq`.
4. The tool: `tools/cmd/sqlq/main.go`, `tools/internal/sqlq/guard.go` and the rest of
   `tools/internal/sqlq/`, `tools/internal/sqlq/bundled_queries_test.go`.
5. The skill the work lands in: `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` and
   `queries/*.sql` (`blocked-processes-check.sql` is the model).
6. Microsoft's documentation for every catalog view, DMV, function and statement the plan's SQL
   uses. Use whatever web or documentation access you have.

Judge the plan against the spec, the rules and reality, not against your own taste. If you want a
different approach, first show that the plan violates a stated rule or a verifiable fact.

## What you can and cannot run

There is NO SQL Server instance available to you. Do not connect to any database, do not run
`sqlq` or `sqlcmd`, and do not look for connection profiles or credentials. Engine behaviour must
be argued from documentation, and labelled as such.

You CAN run Go. Execute the plan's own artefacts verbatim where Go can reach them: put the plan's
query text and the plan's Go test, exactly as written, into a scratch copy and run the test,
the existing guard test, `go vet`. Do not simplify them, do not fix them before running them; a
defect in the text as written is what you are looking for. Do this in a scratch location you
create outside the user's repository (or in your own disposable working copy if you were given
one); never modify the user's repository.

## Plan-specific checks

- Every requirement of the spec maps to a task. Name any that does not.
- Every name a later task uses (column names, file names, function names) matches its
  definition in an earlier task.
- Steps that say "handle edge cases", "similar to task N", or carry TBD/TODO, or describe code
  without showing it, are plan defects.
- Each test step states what failure looks like before the implementation, and the stated
  failure is the right one.
- The validation task (Task 3): would each case actually produce the state it claims? Would the
  stated expectation catch the defect it is there for? Is the setup SQL valid as written, and is
  the cleanup complete?

## Report

Three sections, in this order:

    ## Verified
    Things you established by running Go, by reading the code, or from a quoted documentation
    passage. Quote the command and output, the line, or the passage.

    ## Concluded by reasoning
    Clearly labelled as hypotheses rather than confirmed defects.

    ## Not a problem
    Things you checked that turned out fine, one line each.

One paragraph of prose per defect, not a table. Rank by how expensive the defect would be to
discover after the work is done — a silent wrong answer outranks a loud failure. At most eight
defects; say how many you set aside. If you find nothing serious, say so in one sentence.

## The claims to attack

1. **Load-bearing: the query as written returns exactly what the spec's §4 promises.** Read the
   SQL line by line. Does it compile on SQL Server 2012 (every column and construct)? Do the
   three UNION ALL branches agree in column count and type, position by position? Can any join
   drop or duplicate a row the spec promises (index_count inner join, sys.objects / sys.schemas
   joins, the usage LEFT JOIN, the size aggregate)? Is ORDER BY total, and is it legal where it
   is placed? Does TOP (3) + ROW_NUMBER give rank 1 to the highest sum? Does any value come out
   of sqlq as a JSON string when it should be a number (check what each CAST really yields)?
2. **The context row.** Is every counter computed over what the spec says (all suggestions of
   the database, before caps)? Is `instance_uptime_days` computed correctly (DATEDIFF unit,
   local time on both sides, float division)? Is `database_in_ag` from `sys.databases.replica_id`
   correct on 2012?
3. **The Go contract test.** Run it against the plan's query text verbatim. Does `Sanitize` turn
   the query into text where every needle is found and no forbidden word is? Would the test
   actually fail if the guarded property were removed (try at least two removals)? Does it fail
   for the reason Step 2 says before the file exists?
4. **The guard.** Does the plan's query, header comment included, pass `refusals()`? Any word in
   the code (not the comments) that the write guard keyword list catches?
5. **Task 3.** For M2, M3, M3b, M6, M7b and M9: would the setup create the state claimed (e.g.
   does a 20,000-row table with that query really produce a missing-index suggestion and not a
   trivial plan; does creating indexes before the reads keep the suggestions; does M3b really
   give 10 suggestions and 17 indexes)? Is every setup statement valid T-SQL that sqlq will run
   as one batch with -allow-write (CREATE PARTITION FUNCTION/SCHEME, memory-optimized table and
   filegroup in one batch, CREATE DATABASE)? Is the cleanup complete?
6. **The reading protocol.** Does it contradict the query header or the spec anywhere?
