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
godta report     --project <dir> [--target server|lab]
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
   interrupted run left (journal first, then the name pattern of a run). Run it whenever a
   database reports objects left: its status contains `left for cleanup`, and a warning
   names each object. `clone`, `evaluate` and `cleanup` itself report them this way, with
   `ok: false` and exit code `2`, even when the measures were taken. Run it as well after
   any write phase that did not end with `ok: true`, and after one that was killed.

An execution plan runs once. Running the same `--plan` again, after a crash or after a run
that ended, is refused with exit code `3` before any statement: run `cleanup`, then a new
`--dry-run`, and show the new plan.

Ctrl-C (SIGINT) or SIGTERM stops the measuring, still drops what the run created, and the
run then reports what it could not drop.

## The lab mode

The lab mode measures on another instance, so that no hypothetical index is ever created on
the source. `clone` makes a `DBCC CLONEDATABASE` copy of each database (schema and statistics,
no data), backs it up and brings the file back through the SQL connection; `evaluate --target
lab` restores it on the lab and measures the candidates there. The source still sees the
writes of `clone`, so `clone` follows the same confirmation as any write phase.

```bash
godta clone    --project <dir> --profile <name> --dry-run [--backup-dir <dir>] [--max-bytes 2GiB] [--chunk 256MiB]
godta clone    --project <dir> --profile <name> --allow-write --plan <plan-clone.json> --host-dir <dir>
godta evaluate --project <dir> --target lab --lab-profile <name> --host-dir <dir> --container-dir <dir> [--data-dir <dir>] [--suggestions <file>] --allow-write
godta report   --project <dir> --target lab
```

1. Run `collect` and `candidates` first: `clone` creates on the source, for each candidate,
   the statistics of its keys, so that the clone carries them, then drops them.
2. `clone --dry-run` writes `plan-clone.json` per database. Its `side_effects` list the
   statistics created and dropped (the drop makes the cached plans of the table recompile),
   the clone database, the backup file, the number of reads of the file, and a size estimate
   from `msdb.dbo.backupset`: an estimate only, the real size is checked against
   `max_bytes` before anything is written on the host. `--backup-dir`, `--max-bytes` and
   `--chunk` go into the plan; they are refused at the execution.
3. `clone --allow-write --plan <file> --host-dir <dir>` executes the plan and writes the
   backup file into the host directory, the one the lab container mounts. The clone and
   the file are removed from the source at the end.
4. `evaluate --target lab` needs a lab profile that declares `"environment": "lab"` and is
   `readwrite`, and `--allow-write`; otherwise exit code `3`, before any connection. It
   refuses a lab that is the source instance (`@@SERVERNAME` or `MachineName` equal) or of
   another major version (a different build is a warning). The first run restores the file
   (`--container-dir` is the host directory as the container sees it) and prepares the
   clone; the next runs find it prepared and check that it is still a clone. The result is
   `evaluate-lab.json`; there is no execution plan, since nothing is written on the source.
5. `report --target lab` scores the lab evaluation. Without `--target`, `report` takes the
   server's when `evaluate-server.json` exists, else the lab's.

goDTA neither starts nor stops the lab container, and leaves the restored clone on the lab
for the next runs: dropping it is the DBA's call.

A suggestion passed to `evaluate --target lab --suggestions <file>` after the clone has no
statistics on the lab. Its measures carry the note `unreliable estimate: statistics not
transferred`, and the report never recommends it. Say so to the user, and offer the two ways
out: `clone` again with the suggestion in `candidates` (`candidates --suggestions <file>`),
or evaluate it on the server.

## The confirmation before a write

`clone`, `evaluate --target server` and `cleanup` write on the server. Before any of them, on
every profile, `lab` included:

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
