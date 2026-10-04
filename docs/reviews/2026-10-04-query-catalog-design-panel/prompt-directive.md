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

## The claims to attack

Each question has an answer. Give it, with the evidence.

1. Load-bearing: the DECLARE override (§9) makes the server use the value passed with `-param`,
   or refuses. Can you construct a script, ideally one of the real tsql-scripts files that pass
   the guard and declare variables, that the §9 refusal rules accept but where the rewritten
   batch either does not compile, or compiles and runs with a value other than the one passed
   (compound assignment `SET @p += …`, assignment inside a multi-assignment `SELECT`, `@p` as an
   OUTPUT target, a `WHILE` loop, the variable shadowed by a table variable or used inside a
   string, a `DECLARE` without initializer followed by `SET`, an initializer that is a subquery,
   a `;` inside the initializer, a CRLF file)? Do Sanitize's position guarantees (runes, line
   breaks) hold for every input the rewrite will see? If this claim is wrong, everything the
   agent reports from a parameterised tsql-scripts run is suspect: say so with the output that
   proves it.
2. The tsql-scripts header rules (§7). Run them, verbatim, against the real files: does "the
   first non-empty, non-dash line of the leading `--` block" give the right summary for the
   files that would be marked? Where would the marker line fall under the stated rules, and can
   a real file put it somewhere the parser would not look, or make it see one it should not?
3. Executability. The design takes "passes the guard" as "can run through `sqlq -saved`". Read
   how `sqlq` executes a batch and renders its result: what happens with a script that returns
   several result sets, prints with `PRINT` or `RAISERROR`, sets `SET NOCOUNT ON`, sets its own
   isolation level, uses a table variable (`DECLARE @t TABLE` followed by `INSERT @t`), or runs
   longer than the default timeout? How many of the 214 "passing" files would actually give the
   agent a usable answer?
4. Names and visibility (§6). `-list-queries` needs no profile: which `personal/<profil>/`
   entries does it list, and how does it evaluate collisions without a profile? Can the
   "both entries become rejected" rule be used, or happen by accident, to disable a bundled
   query that the skill depends on? Is the naming rule coherent with `-save-query` and the
   marker?
5. The registry (§8). Is the key (source plus relative path) correct when the same file is
   reachable twice, when the clone moves, when two `sqlq` processes finish at once? Can
   `verified` be shown for content that did not run (CRLF normalisation, a rewritten batch,
   a file edited between hashing and running)?
6. Disclosure. `-list-queries` runs at the start of every session and its output goes to the
   model provider. What in the proposed output (summaries written by humans, `path`, `rejected`
   reasons, `messages`, `verified.profile`) can still carry server, database, or client names,
   against the rule `AGENTS.md` applies to `-list-profiles`?
7. `dirty_reads` (§10). Is lexical detection of `READ UNCOMMITTED` and `NOLOCK` correct on the
   real files: false negatives (`READUNCOMMITTED` hint, `SNAPSHOT`, isolation set then reset),
   false positives (the words in a comment, already blanked by Sanitize?)?
8. Consistency. Where does this spec contradict itself, the 2 September design without listing
   it in §12, `AGENTS.md`, or the current `SKILL.md` (for example: what a bundled query with a
   `Heavy: yes` line or an invalid header did before and does now; whether `-summary` text is
   checked; what `-save-query` writes for parameters)?
