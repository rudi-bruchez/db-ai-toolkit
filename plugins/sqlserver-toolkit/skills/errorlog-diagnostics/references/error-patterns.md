# SQL Server ERRORLOG reference

Signatures and severities used by the `errorlog-diagnostics` skill. This file
is loaded on demand; it is not part of the always-on context.

## Severity levels

| Severity | Meaning | Action |
|---------|---------|--------|
| 0–9 | Informational | Usually ignore |
| 10 | Informational (adjusted from 0) | Ignore |
| 11–16 | User/programming errors (bad object, constraint, permission) | App-side |
| 17 | Insufficient resources (locks, disk, memory) | Investigate capacity |
| 18 | Non-fatal internal error | Monitor |
| 19 | Resource limit exceeded (rare, needs sysadmin) | Investigate |
| 20 | Fatal error in current process | Investigate |
| 21 | Fatal error affecting all processes in the database | Urgent |
| 22 | Fatal error, table integrity suspect | Corruption — urgent |
| 23 | Fatal error, database integrity suspect | Corruption — urgent |
| 24 | Fatal hardware/media error | Corruption / storage — urgent |
| 25 | Fatal system error | Urgent |

The preprocessor's default floor is severity 16.

## High-value signatures

| Signature | Meaning |
|-----------|---------|
| `Error: 823` | Hard I/O error (OS returned error on read/write) — storage or corruption |
| `Error: 824` | Logical consistency I/O error (checksum/torn page) — corruption |
| `Error: 825` | Read-retry succeeded after failures — failing storage, precursor to 823/824 |
| `Login failed for user` | Authentication failure; watch for bursts (brute force) or app outages |
| `I/O requests taking longer than 15 seconds` | Storage latency stall on the named file |
| `A significant part of sql server process memory has been paged out` | External memory pressure |
| `Non-yielding Scheduler` / `non-yielding` | Worker stuck; often precedes a dump |
| `Stack Dump` / `SqlDumpExceptionHandler` / `BugCheck` | Exception dump written |
| `SQL Server Assertion` | Internal invariant violated — open a case with the dump |
| `deadlock` | Deadlock; enable trace flag 1222 / XE for graphs |
| `AlwaysOn` / `availability replica` errors | AG connectivity, failover, or synchronization issue |
| `DBCC CHECKDB ... found N errors` | On-disk corruption confirmed |

## Routinely filtered (noise) categories

The preprocessor drops these when they carry no signal and no high severity
(names as they appear in the `=== COUNTS ===` breakdown):
`backup` (successful backup messages), `login-ok` (successful logins),
`checkdb-ok` (CHECKDB found 0 errors), `db-option` (database option changes),
`startup` (routine startup/recovery-complete lines).

To see dropped entries, re-run `errorlog-parse` with a lower `-severity` or
`-no-aggregate`, or read the raw file for the timestamp window of interest.

## Built-in advisories

The `=== ADVISORIES ===` section is driven by two small rule packs bundled
with the tool (`advice.rules` and `checks.rules`) and is not exhaustive —
treat it as a starting point, not a replacement for the manual scan above.

| Advisory | Trigger | Recommendation |
|----------|---------|-----------------|
| `backup` | ≥500 successful-backup messages in the log | Enable trace flag 3226 so successful backups stop being logged |
| `ifi-off` | "Instant File Initialization" reported disabled/désactivé at startup | Grant "Perform Volume Maintenance Tasks" to the service account |
| `spn-fail` | SPN registration failure at startup | Register the SPN (`setspn`) or check service account rights; Kerberos auth will fall back to NTLM until fixed |

## Redaction

`-redact` replaces database names (quoted, e.g. `database 'Sales'`, or the
`Database: name` form used in backup-summary lines), login names, and IPv4
addresses with stable tokens (`DB_1`, `LOGIN_1`, `IP_1`, ...), consistent
across the whole run. A `=== REDACTION ===` legend mapping tokens back to
real values is appended unless `-no-legend` is also passed — keep that legend
out of anything actually shared externally.
