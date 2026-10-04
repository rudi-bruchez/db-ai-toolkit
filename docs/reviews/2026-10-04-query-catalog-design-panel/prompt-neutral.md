# Review of a design document: no code exists yet

You are reviewing a DESIGN SPECIFICATION. Nothing has been built from it. Your job is to find
what is wrong with it before the Go code and the skill text it describes are written. A defect
found now costs an edit; the same defect found after implementation costs the implementation,
and some of these defects would never be found at all, because they make the agent run a query
with a value other than the one it believes it passed, or present an unverified script as
verified.

## Reading order

1. `docs/superpowers/specs/2026-10-04-query-catalog-design.md`: the document under review
   (written in French).
2. `docs/superpowers/specs/2026-09-02-query-catalog-design.md`: the earlier design it replaces
   and claims to carry forward, except where its §12 lists a deviation.
3. `AGENTS.md`: the binding rules for anything that talks to SQL Server through `sqlq`.
4. The tool the design extends: `tools/cmd/sqlq/main.go` and everything in
   `tools/internal/sqlq/` (in particular `guard.go`: `Sanitize`, `FindWrites`,
   `FindBatchSeparators`, `FindContextChanges`), and their tests.
5. The skill that will change: `plugins/sqlserver-toolkit/skills/live-query/SKILL.md` and the
   bundled queries in `plugins/sqlserver-toolkit/skills/live-query/queries/`.
6. The external repository the design reads in place: `/home/rudi/Sources/Repos/tsql-scripts`
   (its `CLAUDE.md`, `AGENTS.md`, and the `.sql` files themselves). It is READ-ONLY for you.

Judge the design against those documents and against reality, not against your own taste in
architecture. If you want to recommend a different approach, first show that the design violates
a stated rule or a verifiable fact.

## What you can and cannot run

There is NO SQL Server instance available to you. Do not connect to any database, do not run
`sqlq` against a profile, and do not look for connection profiles or credentials. Anything that
depends on engine behaviour must be argued from documentation, and labelled as such.

You CAN run Go. The guard functions are in an internal package, so a throwaway program that
calls them has to live inside the `tools/` module of YOUR working copy (for example
`tools/cmd/zzreview/`); create it there, run it, and delete it before you finish. Running the
real `Sanitize` / `FindWrites` / `FindBatchSeparators` / `FindContextChanges` over the real files
of `/home/rudi/Sources/Repos/tsql-scripts` is exactly the kind of evidence wanted. Do not create,
modify or delete anything in `/home/rudi/Sources/Repos/tsql-scripts`, nor in
`/home/rudi/Sources/Repos/db-ai-toolkit` (the user's live checkout, which is not your working
copy), nor under `~/.config/db-ai-toolkit/`.

Execute the document's own artefacts verbatim: the literal marker line, header layout, rewrite
rule, refusal rule, JSON shape or command as it appears in the document. Do not simplify it, do
not fill in parameters it leaves out, and do not substitute an equivalent you wrote to isolate
the behaviour. A rule the document leaves underspecified is part of what you are testing: say
what the two plausible readings would do.

## Report

Three sections, in this order:

    ## Verified by running
    Include the program or command you ran and the output that proves the point.

    ## Concluded by reading
    Clearly labelled as hypotheses rather than confirmed defects.

    ## Not a problem
    Things you checked that turned out to be fine, one line each. This section is what lets
    the reader tell a reviewer who found nothing from one who looked at nothing.

One paragraph of prose per defect, not a table (tables only for measurement matrices). Rank
defects by how expensive they would be to discover after the code is written and in use: a
silent wrong answer outranks a loud failure. At most ten defects; if you set aside more, say how
many. If you find nothing serious, say so in one sentence rather than padding the list with style
opinions. Do not re-list the test table of the spec as "missing tests" unless a specific test
would fail to catch a specific defect you found.

## What to look at

Decide for yourself what is worth testing and what is worth reading; nobody is going to tell
you where the risk is, working that out is the review.
