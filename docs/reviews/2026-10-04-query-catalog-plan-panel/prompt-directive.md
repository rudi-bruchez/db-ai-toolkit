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

## The claims to attack

Each question has an answer. Give it, with the evidence.

1. Load-bearing: task 5's `AnalyseOverrides` and `Rewrite` make the server use exactly the
   value passed with `-param`, or refuse. Run the task's code verbatim, then run it against
   each of the 45 single-line simple-type `DECLARE` lines of the guard-passing tsql-scripts
   files. Can you construct an input it accepts where the rewritten batch does not compile, or
   runs with a different value (assignment forms the token rules miss, `@p` used as a target
   inside a CTE or `OUTPUT`, `SELECT @p = ...` after a `UNION`, `SET @p=1` without spaces,
   a variable name containing `$` or `#`, nested comments, a `DECLARE` inside `BEGIN ... END`
   at depth 0)? If this is wrong, every parameterised answer is suspect: prove it with output.
2. Task 2's message loop. Read `sqlexp` v0.1.0 and the go-mssqldb v1.11.0 code that enqueues
   messages. Does the loop, as written, return every result set, keep rows read before an
   error, report a timeout as an error, keep the showplan in `plan`, and never hang or drop
   the last set? What does the driver do with `rows.Err()` and `MsgError` when an error
   follows a result set?
3. Task 1's `Refusals`: is the line it reports always the line of the keyword it names? Can
   `FindWrites` name one keyword while the first write-keyword token in `Lex` order is another?
4. Task 7's catalogue: do the testdata files, as specified, produce exactly the entries and
   order `TestCatalogListsExactlyTheFilesPresent` expects? Can `.git` be committed under
   testdata? Is the hash of a tsql-scripts entry computed on the bytes executed, as the design
   requires?
5. Tasks 9 and 10: does `run` still produce the existing behaviour for `-query` and `-file`
   (messages, exit codes, `-allow-write`)? Is any name, option field or helper used before the
   task that defines it? Does `-save-query` ever leave a file, or a registry entry, for SQL that
   did not run, or overwrite an existing file?
6. Test counts and filters: for each task, does the stated `-run` filter select exactly the
   stated number of tests, at the moment the task runs and after later tasks add theirs?
7. Coverage: which requirement of the design (§5 to §16) has no task, or a task whose test
   would pass with the requirement unimplemented?
