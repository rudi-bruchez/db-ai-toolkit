# Harm review — blocked process check (branch `feat/blocked-processes-check`)

Date: 2026-10-01. Scope: the bundled query, its `SKILL.md` row, the bundled-query test, the
validation document, and the spec and plan they implement.

Threat model: a DBA or an agent who knows nothing of this branch reads the `SKILL.md` row and
runs the check on a production instance. It then acts on `instance_state`: it stops looking when
the answer is OK, or it changes the server when the answer is NOT_OK. Someone may also reuse the
validation procedure of the plan on another server.

Sources:
- **own**: my pass;
- **codex**: `codex exec`, read-only sandbox;
- **agy**: `agy -p`, effort high.

opencode and kimi are not installed on this machine. Both readers were run on a clone, with the
`sqlq` profiles, the server inventory and the repo's ignored `temp/` moved out for the duration.
Afterwards the working tree was clean and nothing had been written to the home directory.

Scanners:
- **gosec**: 8 hits, all in files this branch does not touch (`dpapi_windows.go`, `secret.go`,
  `profile.go`). Out of scope.
- **semgrep** (`p/default`): nothing on the changed files.
- **govulncheck**: did not run. The installed binary was built with Go 1.26 and the module
  needs 1.27. The branch adds no dependency.

## Findings, worst first

### H1 — SEVERE — a threshold of 1 to 4 seconds is reported OK (false OK)

**Found by:** own and codex, independently.

**Where:** `blocked-processes-check.sql`, the `thr` CTE. Every non-zero value counts as OK.

**What Microsoft says:**
- The policy rule *Increase or disable blocked process threshold* states: "If you configure the
  threshold to a value from 1 to 4, the system doesn't generate blocked process reports. Don't
  use values 1 to 4 in a production environment because they have no effect."
- The engine accepts those values: `sys.configurations` reports minimum 0 and maximum 86400 on
  the 2019 test instance (checked read-only).

**Scenario:** someone set the threshold to 2 "to catch everything". The check answers OK and the
agent reports that blocking is being captured, but nothing is ever recorded. This is the defect
class the spec exists to prevent. Validation tested 0 and 10, never the 1–4 boundary.

**Smallest fix (code):** OK requires a value of at least 5, both configured and in use. A value
of 1 to 4 gives NOT_OK, with its own reason for the in-use value and for a pending one. Add cases
`3/3`, `5/5`, `10→3 pending` and `3→10 pending`.

### H2 — MODERATE — "RECONFIGURE pending" invites a RECONFIGURE that applies every other staged option

**Found by:** own (query), codex (procedure), agy (pending mismatch).

**Where:**
- the query's reason `threshold set but not in use (RECONFIGURE pending)`. Nothing in the query
  reports other options where `value <> value_in_use`;
- `plan` task 4 step 6, which calls the restoring `RECONFIGURE` "possible sans risque" ("safe")
  on the strength of a check made at step 1.

**Scenario:** a DBA has staged `max server memory (MB)` for a maintenance window. The check says
"RECONFIGURE pending". The agent proposes `RECONFIGURE;`, the user approves the statement shown,
and the staged memory change takes effect during business hours.

**Evidence:** RECONFIGURE applies every eligible pending change (Microsoft, RECONFIGURE). The
step-1 check is a time-of-check/time-of-use guard: it does not cover a change staged during the
run (codex).

**Smallest fix:**
- in the query header, and in the reason if there is room: "before any RECONFIGURE, list
  `sys.configurations WHERE value <> value_in_use`";
- in the plan: re-check before every RECONFIGURE, and remove the word "safe";
- carry the rule into task B.

### H3 — MODERATE — OK claims more than configuration and binding

**Found by:** codex (dispatch latency), agy (file path and permissions).

**Where:**
- the `SKILL.md` row asks "will blocking be captured";
- the spec and the plan say "tout ce qui est nécessaire … est en place" (everything needed is in
  place).

**Scenario:** a conforming session uses `MAX_DISPATCH_LATENCY = INFINITE`, or its target path is
not writable. The check answers OK. Reports stay in memory or are never written, and the operator
stops looking.

**What is already covered:** the query header disclaims "that a report was ever produced or
written".

**What is not:** dispatch latency, the event-loss retention mode, and the fact that
`sys.dm_xe_session_object_columns` proves the target is bound to the session, not that it is
healthy. The `SKILL.md` row is more reassuring than the header.

**Smallest fix (documentation):**
- reword the `SKILL.md` row to "Is the blocked process trace configured and running";
- add dispatch latency and event loss to the header's "OK does NOT say" list;
- optionally, return `max_dispatch_latency`.

### H4 — MODERATE — only Extended Events are checked

**Found by:** agy.

**Scenario:** an instance captures the `Blocked process report` through a server-side SQL Trace
(`sys.traces`) or through an event notification. The check answers NOT_OK `no session`, the user
concludes nothing is captured, and task B could add a second, duplicate collection.

**Smallest fix (documentation, plus task B):**
- the header says that only XE sessions are checked;
- task B checks `sys.traces` and `sys.server_event_notifications` before creating anything.

### H5 — MODERATE — the procedure can leave a pre-existing trace stopped

**Found by:** codex.

**Where:** `plan` task 4 step 1 allows a pre-existing trace to be suspended with the user's
consent. Step 6 restores only the objects that were created and the two options. It does not
restart a suspended session.

**Status:** not triggered in this run, because there was no pre-existing candidate.

**Smallest fix:** forbid suspending an existing session in the procedure. If the procedure keeps
it, record the session's running state and restore it.

### H6 — MODERATE — `server_name` is `@@SERVERNAME`, which can be stale

**Found by:** codex, and the final code review.

**Where:** `@@SERVERNAME` does not follow a rename or a clone; `SERVERPROPERTY('ServerName')`
does (Microsoft, @@SERVERNAME). Usually this fails safe, as permanently incomplete coverage. A
stale name that matches the confirmed list would credit the wrong replica.

**Smallest fix:** also return `SERVERPROPERTY('ServerName')`, and treat a mismatch as ambiguous
identity.

### H7 — MINOR — leftover `.xel` files, with broad name patterns

**Found by:** codex.

**Where:** the validation document names `bpr_test*` as the files to delete. Event files can
contain sensitive statement text, and a pattern used as a deletion selector can hit another
run's files.

**Fix:** a unique per-run prefix, and an exact list of the files created.

### H8 — MINOR — the validation record reads like an instruction

**Found by:** agy.

**Where:** the validation document says "both options were set back to 0". That was this
instance's initial value, but someone reusing the record on a server running with a threshold of
20 could read it as the step to follow.

**Fix:** "set back to their step-1 values (0 on this instance)".

### H9 — MINOR — a stale line in the spec still names `sys.dm_xe_session_targets`

**Found by:** codex.

**Where:** spec line 109 contradicts §5 and the query. Someone following that row would add a
flush.

**Fix:** a one-line edit.

## Rejected or downgraded

- **agy F1, rated SEVERE: "destructive validation procedure on production".** The plan requires a
  dev profile, the user's consent on every write, and a stop if anything is pending. The real
  residue is H2, H5 and H8.
- **agy F3: a pending change between two non-zero values.** It only matters when the pending value
  is 1 to 4, which H1 covers.
- **codex: "work is not bounded to 20 candidates".** True, but the number of server-scoped XE
  sessions is small by nature. Not a harm.

## What the work gets right

- **The query is read-only and refused by nothing.** It does not read the target DMV that flushes.
- **No permission gap turns into "no session".** A missing permission gives UNKNOWN.
- **The verdict is computed before truncation.**
- **The validation procedure has real guards.** Writes are tracked in a registry, there is a stop
  on pending configuration, and the restoration was checked value by value.

## Is it responsible to ship as it stands?

Not before H1: it is a false OK on a value the engine accepts. In order:

1. **H1:** a code change plus four validation cases. About an hour, and it needs writes on the
   test instance.
2. **H2:** the header and reason text, and the plan wording. A few minutes.
3. **H3 and H4:** a `SKILL.md` row reword and header lines. A few minutes.

H5 to H9 can be follow-ups. H4's check of `sys.traces` belongs in task B.

## README warning, in one sentence

"`blocked-processes-check` reads configuration only: OK means a non-zero threshold and a running
Extended Events session with a file target were found, not that blocking reports are being
written. Check the threshold value and the target yourself before relying on it."
(Once H1 is fixed: "a threshold of at least 5 s".)

## Status after the fix pass (2026-10-01)

| finding | outcome |
|---|---|
| H1 | fixed in the query and the spec; below 5 s counts as off. New cases T7–T10 pass, and T1, T2, T5, T6 were rerun |
| H2 | header and spec say it; the plan re-checks pending options before every RECONFIGURE and no longer calls restoration safe |
| H3 | `SKILL.md` row and header reworded: configured and running, not written; dispatch latency and event loss named |
| H4 | header says only Extended Events are read; the `sys.traces` and event-notification check is handed to task B |
| H5 | the plan forbids stopping or altering a pre-existing trace |
| H6, H7 | follow-ups in Todoist |
| H8, H9 | fixed |
