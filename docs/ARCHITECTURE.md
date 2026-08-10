# Maestro architecture

This document describes the shipped Maestro 1.0.0 architecture. It is a map
for contributors, not a product roadmap.

## System overview

Maestro has one orchestration core and three frontends:

```text
                    ┌───────────────────────────────┐
                    │       cmd/maestro             │
                    │ CLI · plain REPL · TUI        │
                    └───────────────┬───────────────┘
                                    │ Command
                    ┌───────────────▼───────────────┐
                    │ internal/orchestrator         │
                    │ lifecycle · policy · state    │
                    └───────┬───────────┬───────────┘
                            │           │ StreamEvent
               task route   │           └──────────────► frontend
                    ┌───────▼───────────────────────┐
                    │ agentcore + agent adapters    │
                    │ native loop or vendor CLI     │
                    └───────┬───────────────────────┘
                            │ role-scoped tools
               ┌────────────┼────────────┬─────────────┐
               ▼            ▼            ▼             ▼
          workspace       Git/spec     MCP/Skills    providers
          read/write      persistence  ecosystem     and vault
```

The TUI does not own lifecycle rules. It renders state, turns user actions into
typed commands, and consumes the same event stream used by tests and other
frontends. Headless commands therefore pass through the same phase validation,
workspace checks, and persistence boundaries.

## Package map

| Package | Responsibility |
| --- | --- |
| `cmd/maestro` | Process setup, global flags, CLI dispatch, TUI/REPL startup, version output |
| `internal/orchestrator` | Lifecycle state machine, command authority, role routing, review and archive gates |
| `internal/agentcore` | Provider-neutral messages, streaming events, tools, gates, budgets, native loops and sub-agents |
| `internal/agent` | Subscription CLI process adapters and normalized streams |
| `internal/tui` | Bubble Tea application, cards, dialogs, Settings, pickers, responsive rendering and IDE integration |
| `internal/editor` | Buffers, editing modes, selection, search, Git gutter and proposal review |
| `internal/spec` | Spec model, validation, atomic trio writes, listing and archive storage |
| `internal/session` | Durable lifecycle, conversation, review, workspace and title records |
| `internal/git` | Repository identity, status/diff parsing, worktrees, checkpoints, commits and merge safety |
| `internal/projectprofile` | Static repository evidence and deterministic `MAESTRO.md` generation |
| `internal/mcp` | Bounded stdio, Streamable HTTP and SSE MCP clients |
| `internal/skills` | Agent Skills discovery, validation, identity and private enablement state |
| `internal/learn` | Bounded source snapshots and structured learning artifacts |
| `internal/config` | Layered `maestrorc` parser for providers, models, MCP, permissions, LSP and options |
| `internal/settings` | Private UI, route, reasoning, engine, theme and editor preferences |
| `internal/vault` | Atomic encrypted provider credential storage |
| `internal/security` | Deterministic source-security checks used during Review |

## Lifecycle state machine

The durable phase is part of the session record. Transitions are explicit and
invalid moves return errors:

```text
CHAT ──/propose──► PROPOSE ──/accept──► SPEC ──/build──► BUILD
  ▲                    │                                      │
  │                 /cancel                                /review
  │                    │                                      ▼
  │                    └────────────────────────────────── REVIEW
  │                                                           │  ▲
  │                                                /fix ───────┘  │
  │                                                           │
  │                                          /docs             │ /archive
  │                                             ▼              ▼
  └────────────────────────────────────────── DOCS ───────► ARCHIVE
```

A failed Review returns the work to Build so `/fix` can consume the persisted
findings. Docs and Archive require current non-failing review evidence. Archive
returns the session to Chat only after its transaction is complete.

### Authority boundaries

The runtime places a trusted operation header before untrusted user,
repository, Skill, and tool content:

- `CHAT` permits discovery and clarification, not spec creation or file writes.
- `PROPOSE_AUTHORIZED` is created only by the `/propose` command.
- Build, Review, Docs, Learn, and Skill runs use separate operation contracts
  with role-specific data and permissions.

Text that resembles an operation header inside a prompt or repository file has
no authority. Spec persistence is owned by Maestro's deterministic command
path, never by an LLM tool call.

## Specs and proposals

An accepted change is represented by:

```text
specs/<id>/
├── spec.md       goal, requirements, decisions, risks and success criteria
├── design.md     implementation design
└── tasks.md      ordered, reviewable work
```

Proposal generation produces structured data that is schema-validated before
these files can exist. `/accept` atomically materializes the trio and records a
hash contract in the session. Build may advance task checkboxes; the normative
content remains immutable. Review validates the current files against the
accepted contract before it records success.

Interactive file changes—including `MAESTRO.md`, Learn notes, tool writes, and
ADRs—are staged in Maestro's private proposal store. The TUI shows the diff and
applies it only after explicit acceptance. `MAESTRO.md` is one atomic contract,
so it cannot be accepted hunk by hunk.

## Agent runtime

### Common stream

Native providers and subscription adapters emit the same `StreamEvent`
vocabulary: text deltas, reasoning, tool calls and results, usage, cost,
sub-agent status, human-action items, errors, and completion. The orchestrator
assigns ordered event metadata before publishing the stream.

This boundary lets the TUI render one stable transcript regardless of the
selected provider. Terminal-facing text is projected through bounded,
control-safe rendering; machine-readable payloads are not rewritten.

### Native engine

The native engine runs the provider-neutral agent loop in process. Build,
Review, and Docs can spawn child loops with a derived context, a role prompt,
the accepted spec or diff, scoped tools, and a shared cancellation tree. Child
agents return a validated `AgentResult`; partial text is not treated as proof
of success.

The primary role scopes are:

| Role | Built-in workspace authority | MCP |
| --- | --- | --- |
| Chat/orchestrator | `read`, `grep`, interactive `ask` | Approval-gated |
| Development | `read`, `grep`, `write`, `bash` under normal permissions | Approval-gated |
| Review | Read-only inspection | Not exposed |
| Docs | Standard workspace tools under normal permissions | Approval-gated when configured |
| Learn | No tools | Not exposed |

Configured permission rules and the human gate are applied after role
scoping. A broader global preference cannot add a tool that the role does not
receive.

### Subscription engine

Subscription routes launch an installed vendor CLI in the active worktree and
translate its output to the common stream. Maestro passes the task on standard
input where supported, bounds diagnostics, propagates cancellation, and does
not inherit user-level MCP configuration for Codex runs.

The vendor CLI remains a separate security boundary. Maestro therefore refuses
subscription-backed Learn execution, where it cannot prove that source access
is limited to the embedded snapshot.

## Models and configuration

Providers and models are resolved through a registry built from:

1. explicit `maestrorc` declarations;
2. supported environment-variable detection;
3. a cached models.dev catalog with an embedded core fallback;
4. model discovery for configured providers that expose a model endpoint.

Canonical model identities are provider-qualified. An unqualified model is
accepted only when it resolves uniquely. Unknown or disabled provider prefixes
fail closed. The wire request receives the provider's raw model ID, while the
qualified identity remains in settings and UI state.

Task routing is durable for Chat, Build, Review, and Docs. Each route records
an engine, provider or subscription agent, model, and supported reasoning
effort. Settings validation rejects unsupported effort values before writing.

Configuration precedence is:

```text
user maestrorc < ./maestrorc < ./.maestrorc
```

Settings are stored separately as private JSON because they describe the local
user experience. Provider secrets are stored in the encrypted vault and are
not written back to `maestrorc`.

## Sessions and workspace identity

Session records contain the lifecycle phase, accepted spec identity,
conversation, pending approvals, review result, task-route selection, title,
and the exact worktree/ref that owns the session. Writes use record locks,
optimistic revisions, atomic replacement, and private file permissions.

Linked Git worktrees share a canonical repository namespace but keep an exact
workspace identity. Loading a session validates its repository, ref, branch,
worktree, and active spec before replacing live state. A missing historical
base branch is never guessed.

`/git` manages persistent registered workspaces. Plain `/accept` always creates
an automatically named, isolated managed worktree and preserves a dirty source
checkout. If the project has no Git history, acceptance initializes the
repository and creates a baseline commit first. MCP stdio processes are stopped
and rebound whenever the active workspace changes.

## Review and archive integrity

Review captures the Git ref, HEAD, and complete worktree fingerprint, then runs
format checks on changed files, vet, tests, spec/task alignment, security
checks, comprehension/TDD checks, and the configured read-only reviewer. It
rechecks the fingerprint and Git identity before persisting a verdict. A
concurrent change invalidates the result.

Archive requires a current non-failing verdict for the same Git state. It
refuses a pre-populated index, previews the exact paths and commit message,
archives the spec inside the same transaction, and optionally merges into the
recorded base branch. Recovery state is retained when publication or cleanup
cannot complete safely.

## MCP and Skills

MCP server metadata, schemas, annotations, and output are untrusted. Discovery
is bounded; published names are namespaced; collisions after sanitization
disable the conflicting tools. Every remote call passes through Maestro's
permission gate regardless of the server's read-only annotation.

Skill startup retains bounded metadata and a hash, not instruction bodies.
`/skills run` reopens the exact discovered file and rejects any identity change.
The body is placed below a runtime-owned read-only contract. It cannot convert
its metadata or linked resources into additional authority.

## Learn and Coach

Coach state is deterministic and private to the project. Opening an activity
does not call a model. The user chooses whether to prepare and send the coaching
prompt, and progress advances only through an explicit completion command.

File Learn opens a regular, UTF-8, size-bounded source file through a confined
workspace path, fingerprints it, and maps exact line regions. Structured model
output is validated against that snapshot. The private native runner exposes
zero tools and never connects MCP; only validated Markdown reaches the
transcript or proposal store.

## TUI and editor

The TUI is a Bubble Tea update/view application. Long-running operations return
commands and messages rather than blocking the update loop. One lifecycle run
may be active at a time; cancellation waits for termination before another run
starts.

Rendering is responsive rather than tied to a fixed canvas. Full, compact, and
minimum-size layouts share semantic color tokens, and focus/state remain
legible without color. The Markdown renderer tracks exact fence type and width
so streamed or nested code cannot escape into prose styling.

The editor owns buffers and editing primitives; the TUI owns navigation,
selection actions, proposal cards, and activity state. Opening a source through
the file tree or agent-follow path passes through canonical workspace
confinement and regular-file checks.

## Persistence summary

| Data | Location | Mutability |
| --- | --- | --- |
| Project contract | `<repo>/MAESTRO.md` | Reviewed proposal |
| Active specs | `<repo>/specs/<id>/` | Lifecycle-controlled |
| Archived specs | `<repo>/specs/archive/<id>/` | Archive transaction |
| Generated ADRs | `<repo>/docs-archive/adr/` | Reviewed in TUI; direct in headless mode |
| Sessions | Maestro user data under `sessions/` | Private, atomic records |
| Settings | Platform user config under `maestro/settings.json` | Private, atomic JSON |
| Credentials | `~/.maestro/vault.json` plus local key | Encrypted, private files |
| Proposals | `~/.maestro/proposals/<session>/` | Private staging |
| Memory/checkpoints/Coach | Maestro user data, keyed by repository | Private, atomic state |

The AES vault key is stored beside its ciphertext with private permissions. It
protects against casual disclosure and accidental plaintext storage, but it is
not an operating-system keychain; a user account compromise can expose both.

## Release architecture

The release build is a static Go executable (`CGO_ENABLED=0`) for macOS, Linux,
and Windows on AMD64 and ARM64. GoReleaser produces compressed archives and one
SHA-256 checksum manifest. The npm package is a version-pinned downloader and
launcher for those binaries; it does not contain a second Maestro runtime.

The local release gate checks formatting, module consistency and checksums,
`go vet`, `staticcheck`, race-enabled tests, a trimmed build, vulnerability
reachability, npm tests, and the exact npm package contents. See
[`PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md).
