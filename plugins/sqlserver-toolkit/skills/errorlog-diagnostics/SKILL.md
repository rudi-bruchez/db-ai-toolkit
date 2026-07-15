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

   Optional: `-severity <n>` changes the severity floor (default 16). It prints
   a digest — a `#`-prefixed header with counts, then the kept entries verbatim.
   Read that digest instead of the raw files. This is where the token savings
   come from: on a busy instance the digest is often <5% of the raw size.

3. **Fallback if the tool is unavailable.** If `errorlog-parse` is not found
   (binary not built for this platform — see the plugin README), degrade
   gracefully: read the ERRORLOG file(s) directly with the Read tool and tell
   the user you are analyzing the raw log, which costs more tokens. Do **not**
   fail the task.

4. **Analyze the digest.** Classify findings by area and severity. Use
   `references/error-patterns.md` for severity meaning and known signatures.
   Look for:
   - Severity 17–25 errors (17–19 resource/system, 20–25 fatal).
   - `Login failed` bursts (brute force, app misconfig, expired credentials).
   - `Error: 823/824/825` and consistency/checksum/torn-page → **corruption**.
   - `I/O requests taking longer than 15 seconds` → storage latency.
   - Memory paged out, non-yielding scheduler, latch timeouts.
   - Deadlocks, stack dumps, assertions, AlwaysOn/AG failures.

5. **Report.** Produce, in this order:
   - **Summary**: one line on overall health + the digest's kept/dropped counts.
   - **Critical findings**: each with the timestamp(s), the raw entry, what it
     means, and a concrete next step. Corruption and fatal severities first.
   - **Watch list**: recurring but non-fatal patterns (I/O stalls, login noise).
   - **What was filtered out**: note the dropped categories so the user knows
     what the digest excluded and can re-run with a lower `-severity` if needed.

## Notes

- Never invent entries. Only report what appears in the digest or raw log.
- If the user asks "why did SQL Server restart / go down at <time>", scan around
  that timestamp specifically and quote the surrounding entries.
- Timestamps in ERRORLOG are the server's local time; call that out if the user
  is correlating with events in another timezone.
