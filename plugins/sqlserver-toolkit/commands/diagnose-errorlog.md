---
description: Triage a folder of SQL Server ERRORLOG files (errors, failed logins, I/O stalls, corruption).
argument-hint: <folder-or-file>
---

Diagnose the SQL Server ERRORLOG(s) at: $ARGUMENTS

Use the `errorlog-diagnostics` skill. Preprocess the log(s) with the bundled
`errorlog-parse` tool to strip noise before analysis, then produce a prioritized
triage report (critical findings first, then a watch list, then what was
filtered out). If `$ARGUMENTS` is empty, ask the user for the folder or file
path first.
