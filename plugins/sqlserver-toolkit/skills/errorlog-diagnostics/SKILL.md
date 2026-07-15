---
name: errorlog-diagnostics
description: Diagnose SQL Server health from ERRORLOG files in a folder. Use when the user points at a directory of SQL Server ERRORLOG files (ERRORLOG, ERRORLOG.1, ...) and wants a triage of errors, failed logins, I/O stalls, corruption, deadlocks or startup problems. Preprocesses the logs with the bundled errorlog-parse tool to save tokens before analysis.
---

# SQL Server ERRORLOG diagnostics

Analyze SQL Server ERRORLOG files and produce a prioritized triage report. Raw
ERRORLOG files are mostly routine noise (backups, successful logins, startup
messages). This skill uses a bundled preprocessor to strip that noise so only
meaningful entries reach the model.

## Workflow

1. **Locate the logs.** The user gives a folder (or a single file). SQL Server
   names its logs `ERRORLOG` (current) plus `ERRORLOG.1` … `ERRORLOG.6`
   (archives). Confirm the path with the user if it is ambiguous.

2. **Preprocess with `errorlog-parse` (preferred).** The bundled tool is on the
   PATH when this plugin is enabled. Run it via the Bash tool:

   ```
   errorlog-parse "<folder-or-file>"
   ```

   It accepts a directory, a single `ERRORLOG` file, a `.zip`, or a `.gz` —
   no manual unzip step needed. It auto-detects UTF-16 vs UTF-8/ANSI encoding
   and CRLF/LF line endings. Repeated entries within the severity/time window
   are collapsed into one line with a `[×N] <first>–<last>` prefix, so a burst
   of 128 identical login failures becomes one line instead of 128.

   The digest has up to four sections, in this order:
   - `=== INSTANCE ===` — product/edition/OS/RAM/collation/auth mode and the
     log's time window, parsed straight from the startup banner.
   - `=== ADVISORIES ===` — actionable call-outs, e.g. a trace-flag-3226
     recommendation when successful-backup messages dominate the log, or a
     boot-state warning (Instant File Initialization disabled, SPN
     registration failure).
   - `=== COUNTS ===` — `<total> entries → <kept> kept, <dropped> dropped` plus
     a per-category breakdown of what was dropped (e.g. `backup=105480,
     checkdb-ok=744`).
   - `=== EVENTS ===` — the kept entries, in order, aggregated as above.

   Read that digest instead of the raw files. This is where the token savings
   come from: on a busy instance the digest is often <5% of the raw size.

   Useful flags (see `errorlog-parse -h` for the full, current list):
   - `-severity <n>` — severity floor (default 16; lower to see more).
   - `-from <date>` / `-to <date>` — restrict to a time window
     (`2006-01-02` or `2006-01-02 15:04:05`).
   - `-format md` — render as Markdown (table for INSTANCE, headed sections)
     instead of the default plain text.
   - `-redact` — pseudonymize database names, logins and IPs to stable tokens
     (`DB_1`, `LOGIN_1`, `IP_1`, ...) before sharing the digest outside the
     environment; prints a `=== REDACTION ===` legend unless `-no-legend` is
     also given.
   - `-no-aggregate` — keep every entry instead of collapsing repeats.
   - `-no-summary` — omit the `=== INSTANCE ===` section.
   - `-rules <dir>` — load extra `*.rules` packs (custom noise/signal/advisory
     rules) in addition to the built-in English/French packs.

3. **Fallback if the tool is unavailable.** If `errorlog-parse` is not found
   (binary not built for this platform — see the plugin README), degrade
   gracefully: read the ERRORLOG file(s) directly with the Read tool and tell
   the user you are analyzing the raw log, which costs more tokens. Do **not**
   fail the task.

4. **Analyze the digest.** Classify findings by area and severity. Use
   `references/error-patterns.md` for severity meaning and known signatures.
   Start with `=== ADVISORIES ===` — those are pre-computed, high-confidence
   call-outs — then look for:
   - Severity 17–25 errors (17–19 resource/system, 20–25 fatal).
   - `Login failed` bursts (brute force, app misconfig, expired credentials).
   - `Error: 823/824/825` and consistency/checksum/torn-page → **corruption**.
   - `I/O requests taking longer than 15 seconds` → storage latency.
   - Memory paged out, non-yielding scheduler, latch timeouts.
   - Deadlocks, stack dumps, assertions, AlwaysOn/AG failures.

5. **Report.** Produce, in this order:
   - **Summary**: one line on overall health, drawn from `=== INSTANCE ===`
     plus the `=== COUNTS ===` kept/dropped totals.
   - **Advisories**: relay each `=== ADVISORIES ===` entry verbatim (message +
     link) since they are already actionable.
   - **Critical findings**: each with the timestamp(s), the raw entry, what it
     means, and a concrete next step. Corruption and fatal severities first.
   - **Watch list**: recurring but non-fatal patterns (I/O stalls, login noise).
   - **What was filtered out**: note the dropped categories from
     `=== COUNTS ===` so the user knows what the digest excluded and can
     re-run with a lower `-severity` if needed.

## Notes

- Never invent entries. Only report what appears in the digest or raw log.
- If the user asks "why did SQL Server restart / go down at <time>", scan around
  that timestamp specifically and quote the surrounding entries.
- Timestamps in ERRORLOG are the server's local time; call that out if the user
  is correlating with events in another timezone.
- If the digest (or a report built from it) is going to leave the user's
  environment — pasted into a ticket, shared with a vendor — prefer `-redact`
  and keep the `=== REDACTION ===` legend only on the user's side.
