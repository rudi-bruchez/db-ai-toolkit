# db-ai-toolkit

A practical toolkit of prompts, agents, and workflows for using AI with SQL
databases. Focused on SQL Server and PostgreSQL, it helps with query design,
diagnostics, performance tuning, and automation using modern AI tools like
Claude and code assistants.

This repository is a **Claude Code plugin marketplace**: it publishes several
self-contained plugins plus the Go tools they use.

## Plugins

| Plugin | What it covers |
|--------|----------------|
| [`sqlserver-toolkit`](plugins/sqlserver-toolkit/) | SQL Server diagnostics, tuning, query design. Includes the `errorlog-diagnostics` and `live-query` skills, with the `errorlog-parse` and `sqlq` tools. |
| [`postgres-toolkit`](plugins/postgres-toolkit/) | PostgreSQL diagnostics, tuning, query design (scaffold). |
| [`cross-db-toolkit`](plugins/cross-db-toolkit/) | Engine-agnostic SQL analysis, rewriting, workload triage (scaffold). |

## Repository layout

```text
AGENTS.md                         # entry point for coding agents (Claude Code, Codex, ...)
.claude-plugin/marketplace.json   # marketplace catalog
plugins/                          # one self-contained plugin per domain
tools/                            # Go module: CLI tools used by plugins
registered-servers/               # SSMS registered servers -> sqlq profiles, Windows only
scripts/build-tools.ps1           # build tools -> plugins/*/bin/
docs/                             # conventions & authoring style
```

See [docs/conventions.md](docs/conventions.md) for the structural rules
(imposed by the Claude Code plugin system) and [docs/writing-style.md](docs/writing-style.md)
for authoring skills, commands and agents.

## Getting started

### Use the plugins in Claude Code

Add this repository as a marketplace, then install a plugin:

```text
/plugin marketplace add <this-repo-url-or-path>
/plugin install sqlserver-toolkit@db-ai-toolkit
```

For local development you can also point Claude Code at a plugin directly with
`claude --plugin-dir plugins/sqlserver-toolkit`.

### Build the Go tools

The SQL Server plugin ships two Go tools: `errorlog-parse`, a token-saving
ERRORLOG preprocessor, and `sqlq`, a guarded read-only query runner. Build them
(requires the [Go toolchain](https://go.dev/dl/)):

```powershell
./scripts/build-tools.ps1     # Windows
./scripts/build-tools.sh      # macOS, Linux
```

This compiles `tools/cmd/*` into `plugins/sqlserver-toolkit/bin/`, which Claude
Code adds to the Bash tool PATH when the plugin is enabled. If `errorlog-parse`
is not built, its skill still works by reading the raw log (at higher token
cost). `sqlq` has no such fallback: the `live-query` skill needs it, because the
read-only guardrails live inside it.

## Development

```bash
cd tools
go test ./...     # run the Go tests
go vet ./...
```

## License

MIT — see [LICENSE](LICENSE).
