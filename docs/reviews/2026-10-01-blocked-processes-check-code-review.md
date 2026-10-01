# External code review — blocked process check (branch `feat/blocked-processes-check`)

Date: 2026-10-01. Reviewers: `codex exec` (read-only sandbox, effort high) and `agy -p`
(effort high), with one identical prompt. The questions were bugs, simplicity, technical debt and
idiomatic Go. Scope: the query, `bundled_queries_test.go` and the `SKILL.md` row. Both readers
ran on clones and reached no database.

Codex could not run `go test` because its sandbox blocks the Go build cache. Every finding below
was concluded by reading and then checked here.

## Accepted

| finding | reviewer | outcome |
|---|---|---|
| The header's availability-group rule omits a condition from spec §6: no `ag_replicas` observed on a confirmed replica may name a replica outside the confirmed list. Without it, coverage of A and B can be declared complete while the output names a replica C | codex, Important | fixed in the header |
| `t.Skipf` when the glob finds no query: if the library moves, the test skips and stays green while checking nothing | codex, Minor (pre-existing) | now `t.Fatalf`; checked by pointing the path at a missing directory, which fails |
| `refusals()` duplicates the list of checks in `cmd/sqlq/main.go`, so it will drift | agy, Important | already a Todoist follow-up (extract a shared `Refusals()`); not fixed on this branch |

## Rejected

| finding | reviewer | why |
|---|---|---|
| "The test checks the helper, not sqlq" | agy, Important | `refusals()` calls the real guard functions (`FindWrites`, `FindContextChanges`, `FindBatchSeparators`). The real risk is that its list of checks drifts from `main.go`, which is the follow-up above |
| The final `TOP (21)` is redundant | agy, Minor | The `shown` and `sentinel` CTEs are evaluated separately, and the views are not read atomically, so in theory both can return rows. The repo's output rule also requires a `TOP` on every query |
| A `map` for the test cases makes the subtest order random | agy, Minor | The cases are independent, and random order does no harm. The code stays as written |

Neither reviewer found a bug in the verdict logic of the query.
