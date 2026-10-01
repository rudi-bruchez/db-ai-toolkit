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

## What to look at

Decide for yourself what is worth testing and what is worth reading; nobody is going to tell
you where the risk is, working that out is the review.
