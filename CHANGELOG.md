# Changelog

All notable changes to Maestro are documented here. Maestro follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### OpenTUI workspace

- Made the English OpenTUI / React workspace the default terminal interface,
  with changes, specifications, conversation and five workflow phases.
- Added native model/account dialogs, masked API-key entry, endpoint discovery,
  multiline composition, file previews and saved-session navigation.
- Bound contract approval to the exact reviewed digest; stale approvals fail.
- Added authenticated host/UI transport, cancellation and terminal restoration.
- Included the compiled UI in source builds, release bundles and npm cache
  verification. The previous editor remains under `maestro tui --classic`.

### Runtime and context safety

- Added complete per-turn context preflight for native providers, including
  system/history content, reasoning and signed-thinking blocks, tool
  calls/results, provider options, tool schemas, and reserved output tokens.
- Bounded retained provider/tool output, tool-call payload accumulation,
  built-in reads/search/command output, review inputs, Git inventories, and
  model/MCP catalogs; oversized semantic evidence now fails explicitly instead
  of being silently truncated.
- Made provider event delivery cancellation-aware and changed streamed text,
  reasoning, and tool-argument accumulation to avoid repeated full-buffer
  copies. Deterministic tool ordering keeps equivalent request prefixes stable.
- Made native provider cost admission reserve each turn immediately before
  dispatch. Daily reservations are atomic across concurrent Maestro processes;
  proven pre-dispatch failures release them, incomplete post-dispatch turns
  retain a bounded lease, and valid completions settle exact cost to the
  admission day. Accounting uncertainty now fails later admission closed.
- Added Anthropic prompt-cache breakpoints within the provider's four-marker
  limit, with `MAESTRO_NO_CACHE=1` for incompatible endpoints.

### Catalogs and integrations

- Made TUI model-catalog startup cache-first and asynchronous, with periodic
  refresh, newest-generation publication, bounded local model discovery,
  retry cooldowns, and transport cleanup on close. Headless loading remains
  synchronous.
- Bounded MCP configuration and native tool catalogs, connected/discovered
  servers through a four-worker pool, shared concurrent discovery, and rejected
  stale catalogs after reconnect, close, workspace changes, or
  `tools/list_changed` notifications.

### Review and process isolation

- Built complete worktree review patches in a private Git index and object
  directory without mutating the user's index/object database, and refused
  content filters, dirty submodules, special files, and oversized evidence.
- Reused bounded identity-checked review inputs and ran `gofmt` against private
  stable copies, closing path-replacement and blocking-special-file races.
- Propagated cancellation to subprocess trees for vendor CLIs, built-in shell
  tools, review commands, and stdio MCP servers. Windows uses kill-on-close Job
  Objects with suspended process startup; Unix uses isolated process groups
  plus escaped-descendant cleanup where required.

### Terminal interface

- Moved file trees, Git gutters, provider/model probes and refreshes, picker
  listings, editor hydration/file opens, and hunk staging off the Bubble Tea
  update loop. Loading/error/retry states remain interactive, and stale results
  cannot overwrite a newer session, workspace, or request.
- Made `Ctrl+Q`, overlay close, and cancellation tear down outstanding TUI
  workers; permission dialogs default to Reject and proposal application adds a
  target-specific confirmation focused on Cancel.
- Added non-TTY startup refusal, `NO_COLOR` coverage for cursor-color control,
  `MAESTRO_GLYPHS` Unicode/ASCII selection, and a Unix PTY test for terminal
  mode restoration on `Ctrl+Q`.

## [1.0.0] — 2026-08-11

Maestro's first public release: a spec-driven AI development environment for
the terminal.

### Release refinements

- Made plain `/accept` create and select a collision-free managed worktree
  automatically, preserving pre-existing checkout changes.
- Made greenfield `/bootstrap` initialize a missing Git repository on `main`;
  `/accept` retains the fallback and creates a private-state/secret-aware
  baseline commit when the repository has no committed history.
- Added an in-TUI confirmation for `/archive` and `/archive --merge`, avoiding
  the hidden stdin prompt that could leave the interface waiting indefinitely.
- Kept the project identity visible beside long session/branch names and made
  `/git` lead with the distinguishing directory instead of a truncated path.
- Filtered delayed OSC color and CSI cursor-position replies before they can
  corrupt a slash command, including commands with arguments.
- Gave Coach, session, Git, and checkpoint pickers enough room for their full
  decision labels at the standard 120-column release size.
- Made Learn retry one malformed structured response, then hydrate every code
  excerpt from the trusted source snapshot instead of model-generated text.
- Replaced the retired OpenCode Go fallback model, corrected the dedicated Go
  endpoint, and refreshed its current model limits and pricing metadata.
- Migrated Maestro-owned legacy skill-state directories to private `0700`
  permissions after validating every path component and rejecting symlinks.

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
- Kept the IDE responsive during active model streams by batching display
  deltas and moving file-tree and Git-gutter scans off the TUI event loop.
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
