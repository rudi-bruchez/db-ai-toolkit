# SQL Server Toolkit

Claude Code plugin: diagnostics, performance tuning and query design for
Microsoft SQL Server.

## Components

| Type | Name | Purpose |
|------|------|---------|
| Skill | `errorlog-diagnostics` | Triage SQL Server ERRORLOG files from a folder |
| Command | `/diagnose-errorlog <folder>` | Entry point that runs the skill on a path |
| Tool | `errorlog-parse` (Go, in `bin/`) | Strips ERRORLOG noise before the skill reads it |

## The `errorlog-parse` preprocessor

Raw ERRORLOG files are dominated by routine noise (backups, successful logins,
startup and recovery messages). `errorlog-parse` reads the raw log and emits a
compact digest — errors, high severities, failed logins, I/O stalls, corruption
and dump signatures — so the skill analyzes a fraction of the original size.

The binary lives in `bin/`, which Claude Code puts on the Bash tool's PATH when
the plugin is enabled, so the skill calls it as a bare command.

### Building the binary

The binary is **not committed**. Build it once (requires the Go toolchain):

```powershell
# from the repository root
./scripts/build-tools.ps1
```

This compiles `tools/cmd/errorlog-parse` into `plugins/sqlserver-toolkit/bin/`.

If the binary is absent (not built, or unsupported platform), the skill degrades
gracefully and reads the raw ERRORLOG directly — correct results, just more
tokens. For distribution to machines without Go, ship prebuilt binaries via a
release (`./scripts/build-tools.ps1 -All` cross-compiles the matrix).

### Using it directly

```bash
errorlog-parse "C:\Program Files\Microsoft SQL Server\MSSQL16.MSSQLSERVER\MSSQL\Log"
errorlog-parse -severity 17 ERRORLOG
type ERRORLOG | errorlog-parse -
```

Output: a `#`-prefixed header (total / kept / dropped counts and dropped
categories) followed by the kept entries verbatim.
