# Changelog

All notable changes to Maestro are documented here. Maestro follows
[Semantic Versioning](https://semver.org/).

## [1.0.0] — 2026-08-10

Maestro's first public release: a spec-driven AI development environment for
the terminal.

### Orchestration

- Added the explicit `chat → propose → spec → build → review → docs → archive`
  lifecycle with durable, validated phase transitions.
- Added native in-process development, review, and documentation agents with
  shared cancellation, bounded tool execution, streaming events, and typed
  completion results.
- Added subscription-backed execution through authenticated Codex, Claude,
  Cursor, OpenCode, Grok, and Kimi CLIs.
- Added independent Chat, Build, Review, and Docs routes with per-task model
  and reasoning-effort selection.
- Added budget, time, tool-count, repeated-call, and stream-rule guardrails.

### Specs and Git

- Added structured proposal recipes for quick changes, features, bugs, and
  architecture work.
- Added review-before-write proposal cards and atomic acceptance of the
  generated `spec.md`, `design.md`, and `tasks.md` contract.
- Added branch and isolated-worktree workflows, deterministic Git identity
  checks, spec-mapped commits, checkpoints, and conversation/code rewind.
- Added fail-closed review evidence tied to the exact Git state that was
  checked, plus `/fix`, ADR proposal, and confirmed archive/merge flows.
- Added static security checks and read-only review-agent analysis.

### Terminal interface

- Added a responsive conversation-first TUI with streamed Markdown, expandable
  tool cards, activity and approval rails, model and session pickers, command
  completion, mouse support, themes, and compact layouts.
- Added an integrated code workspace with file navigation, multiple buffers,
  syntax highlighting, Git gutter, Markdown preview, selection-based Maestro
  actions, proposal diffs, and optional Vim behavior.
- Added a canonical slash-command catalog shared by completion, the command
  palette, and help.
- Added focus-first human output with bounded steps, explicit progress labels,
  and cause/fix error presentation.

### Project continuity

- Added conversational `/bootstrap` for new projects and `/adopt` (`/onboard`
  alias) for bounded static repository discovery; both produce the same
  reviewable `MAESTRO.md` contract.
- Added generated session titles, `/rename`, and `/resume` with durable
  per-project state.
- Added `/git` for selecting or creating registered Git workspaces.
- Added decision memory, rules import/export, and resumable checkpoints.

### Learning

- Added optional Guided and Challenge Coach modes with short, phase-aware
  exercises and private project progress.
- Added `/learn <path> [--deep]` for bounded source explanations and reviewable
  learning notes.
- Isolated Learn generation from tools and MCP; unsupported subscription routes
  fail closed rather than receiving unconfined filesystem authority.

### Ecosystem

- Added OpenAI, Anthropic, OpenAI-compatible, and local-provider support with a
  remote model catalog and embedded fallback metadata.
- Added provider, model, authentication, pricing, context-usage, and reasoning
  controls in the CLI and Settings workspace.
- Added MCP stdio, Streamable HTTP, and SSE clients with bounded discovery,
  namespaced tools, mandatory approval, collision rejection, cancellation, and
  workspace-aware lifecycle management.
- Added standard Agent Skills discovery, qualified identities, explicit
  inspection and enablement, integrity checks, and read-only execution.
- Added encrypted local credential storage with private file permissions and
  atomic updates.

### Distribution

- Added prebuilt macOS, Linux, and Windows archives for AMD64 and ARM64, SHA-256
  checksums, and automated GitHub release packaging.
- Added the `@bryann2k/maestro` npm launcher for version-pinned prebuilt
  binaries, plus direct release downloads and a `go install` fallback.
- Added Linux, macOS, and Windows CI coverage, race-enabled tests, static
  analysis, dependency verification, vulnerability scanning, and npm package
  validation.

[1.0.0]: https://github.com/BRYANN2K/maestro/releases/tag/v1.0.0
