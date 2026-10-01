# Validation — `blocked-processes-check.sql`

Date: 2026-10-01. Query: `plugins/sqlserver-toolkit/skills/live-query/queries/blocked-processes-check.sql`.
Plan: `docs/superpowers/plans/2026-10-01-blocked-processes-check.md`, task 4.

## Instance

- SQL Server 2019 (RTM-CU32-GDR, 15.0.4490.9), Developer Edition, Windows Server 2019.
- Server collation `Latin1_General_CI_AS` (case-insensitive).
- HADR enabled; the instance is a replica of an availability group with two replicas.
- Test login: `sysadmin`. A temporary non-sysadmin login was created for P1 and dropped.

## Initial state (step 1)

| item | value | value_in_use |
|---|---|---|
| `show advanced options` | 0 | 0 |
| `blocked process threshold (s)` | 0 | 0 |

No pending configuration (`value <> value_in_use`: none). No `bpr_test_*` session. No session
capturing `blocked_process_report` (`candidate_count = 0`). None of the stop conditions held.

## Cases

All observations come from `sqlq -file` on the query, after the precondition was re-read.

| case | precondition re-read | expected | observed | verdict |
|---|---|---|---|---|
| V0 | initial state | no error; 20 columns in the documented order and types | 20 columns, order and types match; no error 319 (`WITH` right after `BEGIN` is accepted) | pass (see note 1) |
| T1 | threshold 0/0, no candidate | 1 row; `NOT_OK`; `threshold=0; no session`; `candidate_count = 0` | identical | pass |
| T2 | threshold value 10, in use 0 | `NOT_OK`; `threshold set but not in use (RECONFIGURE pending); no session` | identical | pass |
| T3 | 10/10; `bpr_test_a` event_file, OFF, stopped | instance and session `NOT_OK`; `startup_state=OFF; not running` | identical | pass |
| T4 | T3, started | `NOT_OK`; `startup_state=OFF` | identical; `file_target_running = 1` | pass |
| T5 | T4, `STARTUP_STATE = ON` | instance and session `OK`; `reasons` NULL; target defined and running; event in running session | identical | pass |
| T6 | T5, threshold value 0, in use 10 | `NOT_OK`; `threshold disable pending (next RECONFIGURE turns reports off)` | identical | pass |
| S1 | `ring_buffer` only | `NOT_OK`; `no file target`; `targets = ring_buffer`; `file_target_running = 0` | identical | pass |
| S2 | no target | `NOT_OK`; `no file target`; `targets` NULL | identical | pass |
| S3 | event_file, predicate `database_id = 1` | instance and session `UNKNOWN`; `event filtered by predicate`; predicate shown | identical; predicate `([sqlserver].[database_id]=(1))` | pass |
| S4 | `event_file` and `ring_buffer` | 1 row; `OK`; `targets = event_file, ring_buffer` | identical | pass |
| S5 | one conforming session, one OFF and stopped | 2 rows; instance `OK`; the `OK` row first, then `NOT_OK` | identical | pass |
| S6 | conforming session named `bpr_test_&<é` | name returned intact; `OK` | identical | pass |
| S7 | started session, then `DROP EVENT sqlserver.blocked_process_report` | 0 candidates; 1 row `no session` | identical | pass |
| S8 | 21 sessions OFF and stopped, one conforming `bpr_test_zz` (22 candidates counted beforehand) | `candidate_count = 22`; `details_incomplete = 1`; 20 rows; row 1 `bpr_test_zz` `OK`; unique names; instance `OK` | identical | pass |
| P1 | non-sysadmin login without `VIEW SERVER STATE` | 1 row, no error; `UNKNOWN`; `missing VIEW SERVER STATE` | identical; `has_permission = 0` | pass |
| A1 | instance in an availability group | `ag_replicas` lists the replicas; NULL with the P1 login | both replicas listed with the sysadmin login; NULL with the P1 login | pass |

Note 1. V0 also asked for `event_predicate` to be `nvarchar(3000)`. The `columns` field of
`sqlq` reports the type (`NVARCHAR`) but not its length, so the length was not verified. A
longer predicate would at worst be truncated in that column; the verdict does not depend on
its text.

The running `event_file` target is detected through `sys.dm_xe_session_object_columns`, not
`sys.dm_xe_session_targets`. T3 → T4 and S1 show that this detection follows the session's
state: 0 when stopped or absent, 1 when started.

## Not executed

| case | reason |
|---|---|
| C1 (collation) | The server collation is case-insensitive; two session names differing only by case cannot coexist. |
| P2, P3 | `VIEW SERVER PERFORMANCE STATE` exists from SQL Server 2022; the instance is 2019. |
| A1 on the other replica | Only one replica was checked. Each replica needs its own check, as the query header says. |

## Excluded by the spec

- `event not in running session`: no reproducible way to start a session without one of its
  events; the rule stays as a defensive cross-check.
- `MAX_DURATION` (SQL Server 2025, Managed Instance): not checked by the query.

## Not tested

- SQL Server 2012 to 2017, 2022 and 2025.
- Azure SQL Managed Instance. Azure SQL Database is out of scope: it has no server-scoped
  sessions.

## Restoration (step 6)

Every session and the test login created during the run were dropped from a registry kept
while they were created. Both options were set back to 0 with `RECONFIGURE`. The queries of
step 1 were run again and matched the initial state value by value: both options 0/0, no
pending configuration, no `bpr_test_*` session, no `bpr_test_*` principal. The query's verdict
was again `NOT_OK`, `threshold=0; no session`.

**Left for the user:** dropping a session does not delete its files. The `.xel` files named
`bpr_test_a*` and `bpr_test*` remain in the instance's error log directory and need to be
deleted by hand.
