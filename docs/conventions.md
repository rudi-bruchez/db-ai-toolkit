# Conventions

## Repository layout

This repo is a **Claude Code plugin marketplace**. Its shape is dictated by the
[plugin reference](https://code.claude.com/docs/en/plugins-reference) and
[marketplace reference](https://code.claude.com/docs/en/plugin-marketplaces),
not by preference.

```
db-ai-toolkit/
├── .claude-plugin/marketplace.json   # catalog: lists the plugins below
├── plugins/
│   ├── sqlserver-toolkit/            # one self-contained plugin per domain
│   │   ├── .claude-plugin/plugin.json
│   │   ├── commands/                 # flat .md slash commands
│   │   ├── agents/                   # subagent definitions
│   │   ├── skills/<name>/SKILL.md    # skills (+ optional references/, scripts/)
│   │   └── bin/                      # compiled tool binaries (on PATH when enabled)
│   ├── postgres-toolkit/
│   └── cross-db-toolkit/
├── tools/                            # Go module: source of truth for CLI tools
│   ├── cmd/<tool>/main.go
│   └── internal/<pkg>/
├── scripts/build-tools.ps1           # compile tools/ -> plugins/*/bin/
└── docs/
```

## Hard rules from the plugin system

- **Manifests are JSON**: `.claude-plugin/plugin.json`, `.claude-plugin/marketplace.json`.
  There is no `plugin.yaml`.
- **Plugins must be self-contained.** After install, a plugin is copied to a
  cache and **cannot reference files outside its own directory** (`../shared`
  fails). So there is no top-level `shared/` pulled in at runtime. Genuinely
  common content is either duplicated per plugin or symlinked *within* the
  marketplace (symlinks to marketplace siblings are dereferenced on install).
- **Reference bundled files** with `${CLAUDE_PLUGIN_ROOT}`.
- **Executables** placed in a plugin's `bin/` are added to the Bash tool PATH
  while the plugin is enabled, and are callable as bare commands.

## Component vocabulary

- **skill** — a reusable, task-specialized procedure (`SKILL.md` + supporting
  files). Claude invokes it by context or the user via its name.
- **command** — a flat `.md` entry point (slash command) that kicks off a skill
  or a specific prompt.
- **agent** — a subagent with its own system prompt for longer, multi-step work.
  Persona/tone lives *inside* the agent's prompt; there is no separate
  `personas/` layer.

## Go tools

- One Go module at `tools/` (idiomatic, testable, `go test ./...`).
- Each command under `tools/cmd/<name>/`; reusable logic under `tools/internal/`.
- `scripts/build-tools.ps1` compiles and copies binaries into the consuming
  plugin's `bin/`. Binaries are gitignored (built locally / via release).
- Tools are **token preprocessors**: they reduce what a skill must read. Skills
  must degrade gracefully when a tool binary is absent.
