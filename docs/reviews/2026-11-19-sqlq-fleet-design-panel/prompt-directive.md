# Review of a design document: no code exists yet

You are reviewing a DESIGN SPECIFICATION. Nothing has been built from it. Your job is to find
what is wrong with it before the Go code and the skill text it describes are written. A defect
found now costs an edit; the same defect found after implementation costs the implementation,
and some would never be found at all, because they would make an agent read a fleet result as
complete when instances are missing, or retry something that must not be retried.

## Reading order

1. `docs/superpowers/specs/2026-11-19-sqlq-fleet-design.md`: the document under review (written
   in French).
2. `docs/spec-drafts/TASKS03.md`: the field notes it comes from (features 1 to 3).
3. `AGENTS.md`: the binding rules for anything that talks to SQL Server through `sqlq`.
4. The tool the design changes: `tools/cmd/sqlq/main.go` (`run`, `execute`, `sqlError`,
   `unavailableCause`, `emit`, `fail`) and `tools/internal/sqlq/` (`result.go`, `secret.go`,
   `dsn.go`, `profile.go`, `catalog.go`, `registry.go`), with their tests.
5. The driver it relies on, as vendored by the Go module cache: `github.com/microsoft/go-mssqldb`
   at the version in `tools/go.mod` (`go env GOMODCACHE` gives the directory), and the standard
   library's `database/sql`.
6. The skill that will change: `plugins/sqlserver-toolkit/skills/live-query/SKILL.md`.

Judge the design against those documents and against reality, not against your own taste in
architecture. If you want to recommend a different approach, first show that the design violates
a stated rule or a verifiable fact.

## What you can and cannot run

There is NO SQL Server instance available to you. Do not connect to any database, do not run
`sqlq` against a profile, and do not look for connection profiles or credentials (nothing under
`~/.config/db-ai-toolkit/`, no `.env` file, no `RegSrvr*.xml`, no `credentials.json`).

You CAN run Go, and that is the evidence wanted. A throwaway program or test inside the
`tools/` module of YOUR working copy (for example `tools/cmd/zzreview/`) can import the real
`go-mssqldb` driver and point it at a fake server you start on `127.0.0.1:0` with
`net.Listen`: one that closes the connection on accept, one that accepts and never answers,
one that answers garbage. That tells you what error values the driver and `database/sql`
actually return, whether `errors.Is` and `errors.As` see them, how many times `database/sql`
itself retries before returning, and how long each takes. Delete that program before you finish.
Build `sqlq` from your working copy if you need it (`cd tools && go build ./cmd/sqlq`), and run
it only with a profiles file you write yourself that points at such a fake server.

Do not use sudo. Do not create, modify or delete anything outside your working copy except the
one report file named below; in particular nothing in `/home/rudi/Sources/Repos/db-ai-toolkit`
(the user's live checkout, which is not your working copy). Kill every fake server and
background process you start before you finish.

Execute the document's own artefacts verbatim: the literal flag, default, delay, category rule,
JSON shape, exit code and selection pattern as they appear in the document. Do not simplify
them, do not fill in parameters the document leaves out, and do not substitute an equivalent
you wrote to isolate the behaviour. A rule the document leaves underspecified is part of what
you are testing: say what the two plausible readings would do.

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
opinions. Do not re-list the test section of the spec as "missing tests" unless a specific test
would fail to catch a specific defect you found.

Write your report to the absolute path given at the end of this prompt and write nothing else
anywhere outside your working copy.

## What to look at

Each of these is a question with an answer. Answer it with output.

1. The load-bearing claim of section 4: the errors of a connection that fails while opening can
   be classified by type (errors.Is / errors.As over the chain), not by message text, into
   connect_transient, connect_timeout and connect_failed. Build the fake servers and show,
   for each, the exact error value and type that `db.Conn` returns through this driver. If
   classification by type is impossible for a case, say which and what the text is. If it is
   wrong, say so with the output that proves it.
2. The retry boundary. The spec retries only "during db.Conn" and claims nothing is ever retried
   after the batch is sent. Does `database/sql` already retry on `driver.ErrBadConn` inside
   `db.Conn`, `ExecContext` or `QueryContext`, and does this driver return `ErrBadConn` in a
   case where the batch may already have reached the server? Count the TCP connections your
   fake server receives for one call.
3. Timeouts. The spec splits today's single context into one connect context per attempt (the
   profile's `connection timeout`) and a batch context (`-timeout`). What does the driver do
   with `connection timeout` when the profile does not set it (`ConnectTimeoutSec` 0), and what
   is the worst-case wall time of a call with `-connect-retries 2` and the spec's delays? Is it
   compatible with an agent tool call limit of a few minutes?
4. Warnings. Trace every path in `run` after the resolver is created. Is there one where a
   warning would still be lost or still reach stderr under the spec's design? Are the resolver's
   warnings free of secrets as section 3.3 claims?
5. Multi-profile output (section 5.4): construct inputs where the rule for `columns_differ`, the
   `profile` / `sqlq_profile` key, the top-level `rowcount`, or the exit code 5 gives an agent a
   wrong picture of the fleet. In particular: a profile whose first result set is empty, a
   profile that returns no result set at all, a profile that fails after returning rows.
6. Concurrency (section 5.3): `-parallel` runs profiles concurrently through `execute`. Is the
   resolver (DPAPI, the warn callback, the credentials file), the registry write and the
   catalogue load safe to share across goroutines as written today?
7. Selection (section 5.1): `path.Match` on profile names. Show what `'prd/*'`, `'*'` and
   `-all` select on names with several slashes, upper case, and a trailing CR or space; and
   whether the `-list-profiles` preview can differ from what the run then selects.
8. Agent rules (sections 4.3, 5.6): can an agent following them still touch a `prod` profile
   without the user's yes, or loop on failures?
