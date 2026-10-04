# Review of an implementation plan: no code exists yet

You are reviewing an IMPLEMENTATION PLAN and the design it implements. Nothing has been built
from it. It will be executed task by task by subagents who each see only their own task. Your
job is to find what is wrong before they start: a defect found now costs an edit; found during
execution it costs a task, and some would never be found, because they make the agent report a
value other than the one the server used, or present an unverified script as verified.

## Reading order

1. `docs/superpowers/plans/2026-10-04-query-catalog.md`: the plan under review (in French; the
   code in it is Go).
2. `docs/superpowers/specs/2026-10-04-query-catalog-design.md`: the design (v2) the plan argues
   from. It was itself revised after a first review panel, whose reports and triage are in
   `docs/reviews/2026-10-04-query-catalog-design-panel/`. The revisions are the least reviewed
   part of the design.
3. `AGENTS.md`: the binding rules for anything that talks to SQL Server through `sqlq`.
4. The code the plan modifies: `tools/cmd/sqlq/main.go`, everything in `tools/internal/sqlq/`
   and their tests, `tools/go.mod`; and the skill `plugins/sqlserver-toolkit/skills/live-query/`.
5. The driver behaviour the plan relies on, in the Go module cache:
   `$(go env GOMODCACHE)/github.com/golang-sql/sqlexp@v0.1.0/messages.go` and
   `$(go env GOMODCACHE)/github.com/microsoft/go-mssqldb@v1.11.0/` (messages, civil types).
6. The external repository the design reads in place: `/home/rudi/Sources/Repos/tsql-scripts`.
   It is READ-ONLY for you.

Judge the plan against the design, the code and reality, not against your own taste. If you
want to recommend a different approach, first show that the plan violates a stated rule or a
verifiable fact.

## What you can and cannot run

There is NO SQL Server instance available to you. Do not connect to any database, do not start
containers, do not look for profiles or credentials. Engine behaviour must be argued from
documentation and labelled as such.

You CAN run Go. Execute the plan's own code verbatim: copy the code blocks of a task into YOUR
working copy of `tools/` (the plan's file paths), with its tests, and run the plan's own test
command for that task. Do not fix the code before running it, do not fill in what it leaves
out, and do not substitute an equivalent you wrote to isolate the behaviour. Whatever does not
compile, or whose test count differs from what the plan states, is a finding. Running a task's
code against the real files of `/home/rudi/Sources/Repos/tsql-scripts` is good evidence.
Delete everything you created before you finish. Do not create, modify or delete anything in
`/home/rudi/Sources/Repos/tsql-scripts`, in `/home/rudi/Sources/Repos/db-ai-toolkit` (the
user's live checkout, which is not your working copy), or under `~/.config/db-ai-toolkit/`.

Plan-specific checks: every requirement of the design should map to a task; every name,
signature and type a later task uses should match its definition in an earlier task; a step
that describes instead of showing, or says "similar to", is a defect; each test step should
fail for the right reason before the implementation; no task may consume something no earlier
task produces.

## Report

Three sections, in this order:

    ## Verified by running
    Include the command you ran and the output that proves the point.

    ## Concluded by reading
    Clearly labelled as hypotheses rather than confirmed defects.

    ## Not a problem
    Things you checked that turned out to be fine, one line each.

One paragraph of prose per defect, not a table (tables only for measurement matrices). Rank
defects by how expensive they would be to discover after the code is written: a silent wrong
answer outranks a compile error. At most twelve defects; if you set aside more, say how many.
If you find nothing serious, say so in one sentence rather than padding with style opinions.

## What to look at

Decide for yourself what is worth testing and what is worth reading; nobody is going to tell
you where the risk is, working that out is the review.
