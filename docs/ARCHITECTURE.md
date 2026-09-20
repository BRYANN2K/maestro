# Maestro architecture

This document describes Maestro's current architecture, including the
Unreleased hardening changes after 1.0.0. It is a map for contributors, not a
product roadmap.

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
                    │ Maestro / oh-my-pi harness    │
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
| `internal/agent` | Retired CLI adapters retained for compatibility tests; unreachable from task execution |
| `ui/` | Default OpenTUI / React terminal workspace, dialogs, conversation and artifact rendering |
| `internal/cockpit` | Authenticated local UI transport and typed actions into the orchestrator |
| `internal/tui` | Classic Bubble Tea interface and shared slash-command parser |
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

## Default terminal frontend

`maestro` starts `internal/cockpit`, which launches the adjacent compiled
`maestro-ui`. OpenTUI owns terminal rendering; Go owns credentials, permissions,
workflow state and tools. A private token authenticates a loopback JSONL
connection. Actions are serialized; cancellation and prompt replies stay
available while work is in progress. Provider secrets are not inherited by
the UI child. [TUI.md](TUI.md) describes the public interface.

The default workspace follows Stipulate's `exploring → draft → approved →
applying → checked → documented → archived` contract. The older session
lifecycle below remains accessible through its slash commands and classic UI.

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

Direct providers emit the same `StreamEvent`
vocabulary: text deltas, reasoning, tool calls and results, usage, cost,
sub-agent status, human-action items, errors, and completion. The orchestrator
assigns ordered event metadata before publishing the stream.

This boundary lets the TUI render one stable transcript regardless of the
selected provider. Terminal-facing text is projected through bounded,
control-safe rendering; machine-readable payloads are not rewritten.

Provider sends are cancellation-aware, so a producer cannot remain blocked on
a full event channel after a budget, stream rule, output limit, or user cancel
stops the consumer. Text, reasoning, and signed-thinking fragments accumulate
linearly, while cumulative provider/tool output and streamed tool-call payloads
have hard byte and count ceilings.

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

Before every provider turn, the native loop serializes the complete normalized
request—including system/history content, reasoning and thinking blocks, tool
calls/results, sampling options, and tool schemas—into a deterministic,
conservative token estimate: one token per serialized byte plus 32 framing
tokens. It reserves the requested or model default output allowance, falling
back to 4,096 tokens when neither is known, and refuses a turn that cannot fit
a known context window. The same preflight runs again after tool results grow
the history. Unknown context limits leave this specific check disabled rather
than inventing model metadata. Tool schemas are sorted by name so otherwise
identical prefixes remain reproducible and cacheable.

One native run retains at most 8 MiB of cumulative streamed provider and tool
output by default. Provider-local tool-call assembly uses the same 8 MiB
payload ceiling and permits at most 4,096 calls before publication to the
shared loop. These checks happen before the crossing event is accumulated or
added to history, so limit failures do not preserve an oversized partial
payload.

### Budget admission and accounting

Budget state is rebuilt for each lifecycle run, so the run cost, wall-clock,
tool-count, and repeated-call limits do not inherit counters from earlier
runs. The configured daily total is durable and carries across runs and
Maestro processes for the same canonical project.

The outer native-run estimate is a read-only preview. Immediately before every
provider stream, including turns after tool results, the native loop prices the
planned input plus its reserved output and creates the sole authoritative
reservation. When a daily cap is enabled, a private ledger transaction holds a
cross-process record lock, removes expired leases, and admits the request only
when committed spend plus all live reservations plus the new estimate remains
below the cap. The bounded ledger is atomically replaced with private file
permissions; legacy spend-only records migrate on their next write. Its
current hard bounds are 1 MiB, the current and previous local-calendar day,
128 live reservations per day, and 2,048 recent settlement records per day.
A turn without a deadline receives a 16-minute lease; a turn deadline extends
the lease by 30 seconds, and admission rejects a lease longer than one hour.
Settlement records remain for one hour to make retries idempotent.

If stream setup proves that dispatch did not occur, Maestro releases the local
reservation and, when a daily ledger is configured, uses a detached, bounded
transaction to release its durable peer. Once dispatch may have occurred,
cancellation, provider error, output limit, or rule interruption without a
valid completion abandons the token but preserves its local and durable
liability until the lease expires. A valid completion atomically replaces the
estimate with the exact reported cost when settlement succeeds, even when that
actual cost reaches a cap, and assigns it to the local-calendar day on which
the request was admitted. The valid completion is still counted and forwarded
locally when durable settlement fails. Idempotent settlement records prevent a
retry from charging it twice. Settlement and safe release use detached,
two-second accounting contexts so concurrent run cancellation does not skip
them. A crashed process or a post-dispatch turn without a valid completion
keeps the estimate until lease expiry. An uncertain reserve, release, or
settlement aborts the current operation and fails later admission closed.
### Built-in Maestro harness

The Go host owns canonical history, permissions, tools and budgets. Its private
JSONL bridge runs the bundled fork of oh-my-pi's agent scheduler. Bundled
provider adapters connect directly to account-authenticated endpoints; existing
API-key adapters use the same host contract. Every production runner requires
the bundled runtime. External CLI execution routes are rejected.

Stipulate's pinned engine and coordinator own `.workflow/`: approved contracts,
DAG dependencies, file ownership, contribution review and evidence. Prime's
persistent Python RLM kernel lets the coordinator inspect large context and
invoke approved task delegation. It is an explicitly permitted code-execution
tool, not a sandbox. See [MAESTRO_HARNESS.md](MAESTRO_HARNESS.md) for provenance,
process boundaries, scopes and acceptance evidence.

## Models and configuration

Providers and models are resolved through a registry built from:

1. explicit `maestrorc` declarations;
2. supported environment-variable detection;
3. a cached models.dev catalog with an embedded core fallback;
4. model discovery for configured providers that expose a model endpoint.

The registry publishes immutable, deep-copied snapshots, so readers never
observe a partially rebuilt provider/model map. Interactive startup loads a
valid cache or the embedded core catalog without network I/O, then refreshes
models.dev in the background and swaps the complete registry only when the
newest refresh generation succeeds. Batch commands use synchronous loading.
Closing the orchestrator cancels refreshes, waits for their workers, and closes
provider-owned transports. The models.dev response and cache are capped at
16 MiB; a long-lived interactive orchestrator refreshes every 60 minutes by
default.

For discoverable OpenAI-compatible providers, `Models` returns the current
snapshot immediately and starts one five-second background request when
eligible. A failed request preserves the snapshot and observes a 15-second
retry cooldown; discovery responses are capped at 1 MiB, and closing the
provider cancels the request. Anthropic requests keep static system blocks
separate from rolling history and place no more than four explicit
prompt-cache breakpoints across the final system block, two recent messages,
and final tool definition. `MAESTRO_NO_CACHE=1` disables those directives for
incompatible proxies.

Canonical model identities are provider-qualified. An unqualified model is
accepted only when it resolves uniquely. Unknown or disabled provider prefixes
fail closed. The wire request receives the provider's raw model ID, while the
qualified identity remains in settings and UI state.

Task routing is durable for Chat, Build, Review, and Docs. Each route records
the Maestro engine, a provider-qualified model, and supported reasoning
effort. Retired CLI routes are preserved separately during settings migration. Settings validation rejects unsupported effort values before writing.

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

The complete HEAD-to-worktree patch is rendered through a private temporary
Git index and private object directory with the repository object store
used only as an alternate read source. It therefore does not mutate the user's
index or object database. The snapshot path disables filesystem monitors,
external diffs, and text conversion; it refuses repository content filters,
dirty submodules, special files, and oversized per-file, aggregate, or patch
inputs rather than executing repository-controlled helpers or returning
partial evidence.

Changed review sources are opened through a workspace-confined root and checked
for regular-file identity and size. Security and comprehension gates reuse one
bounded cache; formatting captures inputs through the same identity-safe reader
and gives `gofmt` private stable copies. Replacing a workspace path after
validation therefore cannot make the formatter open a FIFO or different file.
Deterministic review commands have bounded output, deadlines, and process-tree
cancellation.

The current review-evidence limits are 16 MiB per changed file, 32 MiB for the
changed-file set, 2 MiB for the rendered patch, and 30 seconds for
`WorktreeDiff`. Source gates accept at most 8,192 changed paths, 4 MiB per
cached source, and 32 MiB in aggregate. Vet and test commands retain at most
64 KiB of output and have two-minute and ten-minute deadlines respectively.
Exceeding a semantic evidence limit fails Review explicitly; it never converts
a partial snapshot into a verdict.

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

The registry retains at most 64 configured clients. Connection and catalog
discovery use four workers. One server may publish at most 128 tools over 32
pages, with 32 KiB per input or output schema and 128 KiB of aggregate schema;
exceeding a per-server limit rejects that complete catalog. The native loop
then admits at most 64 tools, 64 KiB of encoded catalog, and 128 KiB of retained
schema before one complete, deterministically ordered snapshot is published.
MCP calls reject requests above 256 KiB or responses above 1 MiB and retain at
most 64 KiB of tool output. Concurrent callers share one cancellable
`tools/list` operation. Closing, reconnecting, switching workspaces, or
receiving `tools/list_changed` invalidates in-flight discovery, so stale tool
schemas cannot become callable.

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

File-tree refreshes, Git gutters, session/workspace/Coach lists, provider and
model probes, model refreshes, `@file` completion, editor hydration, file opens,
and hunk staging run as cancellable effects. Each result carries a request,
workspace, session, and target identity; only the newest still-current result
may mutate UI state. Loading, empty, and bounded error states are explicit.
File/provider failures preserve the last valid snapshot and expose an `r` or
`Ctrl+R` retry. Escape closes and cancels transient work; `Ctrl+Q` also cancels
the active run, settings actions, and outstanding background workers.

Tool permission dialogs focus Reject by default, and “always allow” remains
scoped to the named tool. Applying a staged file proposal requires a separate
confirmation that focuses Cancel, shows the exact target and effect, and keeps
the diff review reachable without mutation.

Rendering is responsive rather than tied to a fixed canvas. Full, compact, and
minimum-size layouts share semantic color tokens, and focus/state remain
legible without color. The Markdown renderer tracks exact fence type and width
so streamed or nested code cannot escape into prose styling.

The launcher refuses interactive startup before emitting terminal control
sequences unless both stdin and stdout are terminals. Color follows terminal
capability; the presence of `NO_COLOR` also suppresses cursor-color control.
Glyph capability is independent: `MAESTRO_GLYPHS=ascii` projects decorative
Unicode to single-cell ASCII, `MAESTRO_GLYPHS=unicode` forces the rich set, and
`TERM=dumb` or a strict `C`/`POSIX` locale selects ASCII automatically. A Unix
PTY integration test exercises the production startup/`Ctrl+Q` boundary and
asserts restoration of alternate-screen, cursor, focus, paste, and mouse
modes; it is not Windows ConPTY evidence.

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
| Daily budget ledger | `<SessionsDir>/<project>/budget.ledger` | Private, atomic cross-process reservations and settlements |
| Settings | Platform user config under `maestro/settings.json` | Private, atomic JSON |
| Credentials | `~/.maestro/vault.json` plus local key | Encrypted, private files |
| Proposals | `~/.maestro/proposals/<session>/` | Private staging |
| Memory/checkpoints/Coach | Maestro user data, keyed by repository | Private, atomic state |

The launcher resolves `SessionsDir` from `MAESTRO_SESSIONS_DIR` when set and
otherwise uses `~/.maestro/sessions`.

The AES vault key is stored beside its ciphertext with private permissions. It
protects against casual disclosure and accidental plaintext storage, but it is
not an operating-system keychain; a user account compromise can expose both.

## Release architecture

The release bundle contains a static Go frontend (`CGO_ENABLED=0`), a Bun-compiled
Maestro runtime and its platform-specific native library. Complete bundles target
macOS/Linux AMD64 and ARM64 and Windows AMD64. Python 3.11+ remains a runtime
prerequisite for Stipulate and RLM. GoReleaser produces compressed archives and
SHA-256 checksums. The npm launcher verifies and caches all three components;
it never substitutes an external agent CLI for a missing runtime.

The local release gate checks formatting, module consistency and checksums,
`go vet`, `staticcheck`, race-enabled tests, a trimmed build, vulnerability
reachability, npm tests, and the exact npm package contents. See
[`PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md).
