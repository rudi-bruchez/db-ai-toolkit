# Review of a design document — no code exists yet

You are reviewing a DESIGN SPECIFICATION. Nothing has been built from it. Your job is to find
what is wrong with it before the SQL query and the skill text it describes are written. A defect
found now costs an edit; the same defect found after implementation costs the implementation,
and some of these defects would never be found at all, because they produce plausible wrong
numbers rather than errors.

## Reading order

1. `docs/superpowers/specs/2026-10-01-missing-indexes-design.md` — the document under review
   (written in French).
2. `AGENTS.md` — the binding rules for anything that talks to SQL Server through `sqlq`.
3. The tool the query will run inside: `tools/cmd/sqlq/main.go`, `tools/internal/sqlq/guard.go`
   (and the rest of `tools/internal/sqlq/`), `tools/internal/sqlq/bundled_queries_test.go`.
4. The existing bundled queries and skill it will sit beside:
   `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` and
   `plugins/sqlserver-toolkit/skills/live-query/queries/*.sql` — in particular
   `blocked-processes-check.sql`, the model this design copies, and its spec
   `docs/superpowers/specs/2026-10-01-blocked-processes-check-design.md`.
5. Microsoft's documentation for the objects the design relies on:
   sys.dm_db_missing_index_details / _groups / _group_stats, sys.dm_db_index_usage_stats,
   sys.indexes, sys.index_columns, sys.dm_db_partition_stats, OBJECTPROPERTY, and
   "Tune nonclustered indexes with missing index suggestions". Use whatever web or
   documentation access you have.

Judge the design against those documents and against reality, not against your own taste in
architecture. If you want to recommend a different approach, first show that the design violates
a stated rule or a verifiable fact.

## What you can and cannot run

There is NO SQL Server instance available to you. Do not connect to any database, do not run
`sqlq` or `sqlcmd`, and do not look for connection profiles or credentials. Anything that depends
on engine behaviour must be argued from documentation, and labelled as such.

You CAN read the Go code, and you can reason precisely about what `sqlq` does with a result —
how it renders each SQL type, what its write guard refuses, how it truncates. Where a claim in
the design is about `sqlq` itself, check it against the code, not against the design's
description of the code. You may write a scratch draft of the query in your head or in your
report to test whether the design's constraints can all hold at once (single batch, no INTO,
no DECLARE, 2012 compatibility, the column list and the ordering), but do not create or modify
any file in the repository.

## Report

Three sections, in this order:

    ## Verified
    Things you established from the code or from a quoted documentation passage. Quote the
    line or the passage that proves it.

    ## Concluded by reasoning
    Clearly labelled as hypotheses rather than confirmed defects.

    ## Not a problem
    Things you checked that turned out to be fine, one line each. This section is what lets
    the reader tell a reviewer who found nothing from one who looked at nothing.

One paragraph of prose per defect, not a table. Rank defects by how expensive they would be to
discover after the query is written and in use — a silent wrong answer outranks a loud failure.
At most eight defects; if you set aside more, say how many. If you find nothing serious, say so
in one sentence rather than padding the list with style opinions.

## What to look at

Decide for yourself what is worth testing and what is worth reading; nobody is going to tell
you where the risk is, working that out is the review.
