---
name: index-advisor
description: Propose and measure indexes for the databases of a SQL Server instance from the workload that actually ran, with the bundled godta tool. Use when the user asks which indexes a database needs, whether an index would help, or for an index tuning pass over an instance. godta reads the Query Store (or the plan cache), generates rowstore nonclustered candidates without duplicating the existing indexes, measures each under DBCC AUTOPILOT with hypothetical indexes, and writes a report and a script per database.
---

# Index advice with godta

`godta` works in phases. Each phase is one command, reads and writes JSON files in a project
directory (`--project`), and prints one JSON object on stdout. Claude runs the commands, shows
the user what the write phases will do, and reads the results; the DBA decides what to create.

`godta` opens its own connections from the `sqlq` profiles. It is the one exception to "all
access goes through `sqlq`" in `AGENTS.md`, because its measurement needs statements the
`sqlq` guard refuses. The exception covers `godta` alone: never `sqlcmd`, never an ad-hoc
connection string.

## Before the first command

1. Check the tool: `godta help`. If the command is not found, the binary has not been built:
   tell the user to run `./scripts/build-tools.sh` (or `./scripts/build-tools.ps1`) from the
   repository root.
2. Pick the profile as `live-query` does (`sqlq -list-profiles`). On a profile marked
   `"environment": "prod"`, name the profile to the user and wait for an explicit yes before
   the first `godta` command that connects, `collect` included. `collect` writes nothing, but it
   reads the Query Store, the plan cache and the catalog of every selected database.
3. Choose a project directory outside the repository, one per instance and per analysis.
   Select the databases with `--include` and `--exclude` (repeatable, `path.Match` patterns,
   case-insensitive); without `--include`, every user database is collected.

## The phases

```bash
godta collect    --project <dir> --profile <name> --include <db> [--window 24h] [--until <RFC3339>] [--top 50]
godta candidates --project <dir> [--suggestions <file>] [--sqlferret <sqlferret project>]
godta evaluate   --project <dir> --profile <name> --target server --dry-run [--suggestions <file>] [--max-combined 4] [--lock-timeout 2s]
godta evaluate   --project <dir> --profile <name> --target server --allow-write --plan <plan-evaluate.json> [--compile-timeout 60s]
godta report     --project <dir>
godta cleanup    --project <dir> --profile <name> --dry-run
godta cleanup    --project <dir> --profile <name> --allow-write --plan <plan-cleanup.json>
```

1. `collect` reads, per database, the workload of the window (top N queries by executions
   and by cost, the writes), the tables they touch with their indexes and statistics, and the
   missing index suggestions. Read only.
2. `candidates` generates the candidates and confronts them with the existing indexes
   (covered, widen, replace, discuss, new). Offline.
3. `evaluate --target server --dry-run` writes the execution plan of each database,
   `plan-evaluate.json`, and executes nothing.
4. `evaluate --target server --allow-write --plan <file>` executes that plan and nothing
   else: it creates the hypothetical indexes, compiles the queries with and without them, and
   drops them. It refuses a plan whose checksum, server, database or table schemas changed
   since the `--dry-run`. With several databases, pass one `--plan` per database.
5. `report` scores the measures and writes, per database, `rapport.md`, `indexes.sql` and
   `result.json`, and `synthese.md` at the root of the project. Offline.
6. `cleanup --dry-run`, then `cleanup --allow-write --plan <file>`, removes what an
   interrupted run left (journal first, then the name pattern of a run). Run it after any
   `evaluate` that did not end with `ok: true`.

`evaluate --target lab` and `clone` are not available yet.

## The confirmation before a write

`evaluate --target server` and `cleanup` write on the server. Before either, on every profile,
`lab` included:

1. Run the `--dry-run`.
2. Show the user the plan file it wrote (the `plan` field of the output, or the files of each
   database): its `steps`, which are the exact statements, and its `side_effects` (the shared
   lock while each hypothetical index is created, the `Sch-M` lock at each drop, the reset of
   the missing index suggestions of the table, the statistics the engine creates).
3. Wait for a yes to that plan. A profile that permits writes is not consent.
4. Run the same command with `--allow-write --plan <file>`.

Never pass `--allow-write` on your own initiative, never edit a plan file, never write one by
hand. Exit code `3` is a refused write: report it and stop, do not switch the profile or look
for another way to send the statements.

## Suggestions of your own

After a first `report`, read `result.json` and write your own candidates into a JSON file:

```json
{"suggestions": [
  {"table": "dbo.Orders",
   "keys": [{"column": "CustomerID"}, {"column": "OrderDate", "desc": true}],
   "include": ["Status"],
   "reason": "query 1234 seeks on CustomerID then sorts on OrderDate"}
]}
```

- `table` is `schema.name`; `keys` are ordered, `desc` is optional; `include` is optional;
  `reason` says why, and the report shows it next to the recommendation.
- A suggestion is a structure, never SQL. A field the format does not know (such as `sql`)
  makes godta refuse the whole file; a table or column that does not exist rejects that
  suggestion alone, with the reason in the `warnings` of the output.
- The file applies to every selected database: select the one it is for with `--include`.
- Pass it to `evaluate --target server --dry-run --suggestions <file>`: the suggestions enter
  the plan of that `--dry-run`, which you show to the user as above, then evaluate and
  `report` again. They are measured exactly like the algorithm's candidates.

## Reading the results

`result.json` (one per database, next to `collect.json`) holds everything `rapport.md` says:

- `recommendations`: every candidate, with its `section` (`recommended`, `discuss`,
  `rejected`, `not_evaluated`), its `action` against the existing indexes (`new`, `widen`,
  `replace`), its `definition`, its `origin` (`algorithm`, `missing_index`, `sqlferret`,
  `ai`), `gain`, `maintenance` and `size_mb` (null when not evaluated), the `queries` it
  serves with their cost before and after, and its `notes`.
- `formulas`: the scoring rule, in words.
- `source` and `period`: a workload from the plan cache has counters cumulated since each plan
  was compiled, not over the window; say so, and never present them as "the last 24 hours".
- `warnings`, `evaluation_warnings`, `auto_created_stats`, `hypotheticals` (leftovers of the
  DTA or of an earlier run), `redundancies` (observations on the existing indexes, without a
  gain and without a statement).

Costs are the optimizer's estimates, from sampled statistics; a real index gets full-scan
statistics. Present a gain as an estimate, and quote the `query_id`s it rests on.

## What not to do

- Never run `indexes.sql`, and never rewrite it or produce a variant of it. It is the DBA's
  script, to be read with `rapport.md` and run by the DBA when they decide to. If the user
  wants a change, change the suggestions and run the phases again.
- Never `DROP` an index on the strength of the report alone: code that did not run during the
  window (month-end jobs, reports) is invisible to godta, and the script says so next to every
  `DROP`.
- Do not work around a refusal (exit code `3`) or retry a rejected login (error 18456).

## Output and exit codes

```json
{"command": "evaluate", "ok": true,
 "databases": [{"server": "SRV01", "name": "ERP", "status": "planned",
                "files": [".../plan-evaluate.json", ".../candidates-evaluate.json"], "warnings": []}],
 "plan": ".../plan-evaluate.json"}
```

On failure `ok` is false and `error` holds `{"kind", "message"}`; a database that failed has
its own `error` and the others go on. Exit codes: `0` success, `1` usage, `2` a database failed
after the connection, `3` write refused, `4` could not connect.
