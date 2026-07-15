# Writing style for skills, commands and agents

Guidance for authoring the markdown content in this repo. The goal is content
that Claude follows reliably and that stays useful across SQL Server and
PostgreSQL versions.

## Skills (`SKILL.md`)

- **Frontmatter**: `name` (kebab-case, stable) and a `description` that says
  *when* to use the skill and *what input* it expects. Claude uses the
  description to decide whether to invoke it, so make the trigger explicit.
- **Body**: a numbered workflow, not prose. State the happy path and the
  fallback. Name the concrete tools/commands to run.
- Keep the always-loaded body small; push long tables and signatures into
  `references/*.md` loaded on demand.
- Tell Claude what **not** to do (e.g. "never invent log entries").

## Commands (`commands/*.md`)

- Frontmatter: `description` and `argument-hint`.
- Body is the prompt. Use `$ARGUMENTS` (or `$1`, `$2`) for user input and handle
  the empty case (ask for the missing path).

## Agents (`agents/*.md`)

- Frontmatter: `name`, `description`, optionally `model`, `effort`, `tools`,
  `disallowedTools`. Put the expert **persona and tone directly in the system
  prompt** — this repo has no separate personas layer.

## SQL and DBA content

- Be explicit about engine and version differences; never assume the reader is
  on the latest release.
- Prefer read-only diagnostics by default. Flag any statement that writes,
  changes configuration, or restarts a service, and require confirmation.
- Quote evidence (log lines, plan fragments) rather than paraphrasing when the
  exact text matters.
