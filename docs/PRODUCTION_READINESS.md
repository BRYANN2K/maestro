# Production readiness

Released baseline: Maestro 1.0.0

Unreleased hardening review: 2026-08-23

## Decision

Version 1.0.0 is the released baseline. This audit covers the current
Unreleased hardening changes and does not assign their next release version.
Publication must use one reviewed, clean commit for which every gate below
passes. A local build from a dirty worktree is useful evidence, but it is not a
releasable artifact.

Maestro's user interface is a terminal application. The default frontend is now the OpenTUI / React workspace documented in
[TUI.md](TUI.md). The earlier Bubble Tea audit below applies to
`maestro tui --classic`; its integrated editor remains available. There is no
browser application in the release.

## Release contract

One Git tag, one GitHub release, one npm package, and the embedded binary must
use the same reviewed version. The examples below retain `1.0.0` because they
describe the released baseline; choose the next version before creating a new
tag or package.

The supported prebuilt matrix is:

| Operating system | Architectures | Archive |
| --- | --- | --- |
| macOS | AMD64, ARM64 | `.tar.gz` |
| Linux | AMD64, ARM64 | `.tar.gz` |
| Windows | AMD64, ARM64 | `.zip` |

Every binary is built with `CGO_ENABLED=0` and `-trimpath`. GoReleaser emits
`checksums.txt`; the npm launcher downloads the matching release asset and
verifies its SHA-256 digest before installation. `go install` is a fallback for
users who intentionally build with a local Go toolchain.

## Required release gate

Run from the exact commit that will be tagged:

```sh
make release-check
```

That aggregate gate must prove:

| Check | Required result |
| --- | --- |
| Formatting | `gofmt` and `goimports` report no changes |
| Modules | `go mod tidy -diff` is empty and `go mod verify` passes |
| Static analysis | `go vet ./...` and `staticcheck ./...` pass |
| Tests | `go test ./... -race -count=1` passes |
| Build | A trimmed local binary builds successfully |
| Vulnerabilities | `govulncheck ./...` reports no reachable vulnerability |
| npm launcher | All Node tests pass |
| npm package | Dry run contains only the declared launcher, README, license, and package metadata |

Before tagging, also run a GoReleaser configuration check and a snapshot build
of all six OS/architecture targets. Launch the freshly built local binary in a
pseudo-terminal and exercise startup, Settings, model selection, one project
flow, one session restore, Skills, MCP status, Learn, IDE, compact rendering,
and clean exit.

The classic production-boundary PTY test is Unix-only. It launches the
`runClassicTUI` path, waits for actual mode-entry bytes, sends `Ctrl+Q`, and requires a
bounded exit plus restoration of alternate-screen, cursor visibility/color,
focus-reporting, bracketed-paste, and configured mouse state. It does not prove
Windows ConPTY behavior. The current CI cross-build job compiles Windows
AMD64/ARM64 binaries on Linux but does not execute Go tests on Windows; before
release, run the Go suite and a fresh-binary TUI smoke test on a native Windows
runner.

The new `TestOpenTUIProductionStartupAndQuit` exercises the production Go host
and compiled UI at 160×48 and 80×24, including Connections and terminal teardown.
`make ui-test` builds the companions before running it. A successful cross-build
is not evidence of execution on Windows or Linux.

## Release automation

The `Release` GitHub Actions workflow publishes tagged commits to GitHub
Releases and npm. npm authentication uses trusted publishing with short-lived
OIDC credentials; no long-lived `NPM_TOKEN` is passed to `npm publish`.

Configure the npm package's trusted publisher once with these exact values:

| Setting | Value |
| --- | --- |
| Provider | GitHub Actions |
| Organization or user | `BRYANN2K` |
| Repository | `maestro` |
| Workflow filename | `release.yml` |
| Environment | Leave empty |
| Allowed action | `npm publish` |

The workflow is idempotent. A rerun verifies the seven expected GitHub assets
and compares the published npm tarball's SHA-1 with a package rebuilt from the
tag before it skips either publication. For example, reconcile the existing
1.0.0 tag from the default branch with:

```sh
gh workflow run Release --ref main -f tag=v1.0.0
```

## Hardened boundaries

### Credentials and configuration

- Provider API keys are stored in Maestro's encrypted vault rather than being
  written to `maestrorc` by provider commands.
- Settings, Skill state, sessions, and vault data use private directories,
  atomic replacement, and private file modes on platforms that support POSIX
  permissions.
- Provider IDs, types, base URLs, model identities, and reasoning values are
  validated before configuration or vault mutation.
- Custom models remain provider-qualified. Ambiguous bare model IDs and
  unknown or disabled provider prefixes fail closed.

### Agent and tool authority

- Chat can inspect and discuss a workspace but cannot persist a spec. Only
  `/propose` creates proposal authority.
- Tool sets are role-scoped before permission rules are applied. Review has
  only read and search tools.
- Write and command execution require the configured permission path. A deny
  rule cannot be bypassed by a non-interactive flag.
- Native child agents share cancellation with their parent and must return a
  validated completion result. Stream termination or partial prose is not
  treated as success.
- Subscription processes run in the selected worktree, receive bounded input
  and diagnostics, and propagate failure and cancellation to their subprocess
  tree. Built-in shell tools and review commands use the same tree-cancellation
  boundary.
- Native provider turns preflight the complete normalized request against a
  known model context window, including a reserved output allowance. The check
  repeats after tool results grow history; provider/tool output and streamed
  tool-call payloads have hard retention limits.
- Native provider cost admission reserves each turn immediately before
  dispatch. With a daily cap enabled, one private project ledger atomically
  compares committed spend plus all live reservations across Maestro
  processes. A proven pre-dispatch failure releases its estimate; any
  cancellation or incomplete stream after dispatch retains the bounded lease.
  A valid completion is counted locally even if persistence fails; successful
  settlement records its exact actual cost on the admission day. Uncertain
  accounting aborts the operation and fails later admission closed.
  Subscription routes record valid completion costs without claiming native
  pre-dispatch reservation.

### Git and lifecycle integrity

- Status and diff parsing use Git's machine-readable, NUL-delimited formats
  and handle spaces, Unicode, tabs, newlines, renames, and option-like paths.
- Session restore validates canonical repository and worktree identity before
  replacing live state.
- Accepted specs carry a durable hash contract. Review validates the contract
  and binds its verdict to the exact ref, HEAD, and worktree fingerprint.
- Docs and Archive require a current, non-failing review for that same Git
  state.
- Archive refuses a pre-populated index, stages only its reviewed transaction,
  and never guesses an unknown merge target.
- Rewind captures and verifies worktree, index, untracked-file, spec, and
  conversation state and retains a recovery checkpoint.
- Worktree review evidence uses a private index and object directory, refuses
  content filters, special/oversized inputs, dirty submodules, external diffs,
  text conversion, and filesystem monitors, and never returns a silently
  truncated patch.
- Review source gates reuse bounded, identity-checked file snapshots; `gofmt`
  receives private stable copies instead of reopening workspace paths.

### MCP, Skills, and Learn

- MCP discovery and responses are cancellable and bounded. Tools are
  namespaced, collisions fail closed, and every call passes through the
  configured permission gate.
- MCP clients are closed and rebound when the active workspace changes. MCP is
  not exposed to Review, Skills, Learn, or subscription routes.
- MCP retains at most 64 configured clients, performs connection/discovery with
  four workers, and publishes a complete catalog only after per-server and
  global native-loop budgets pass. Close, reconnect, and list-change events
  prevent stale catalog publication.
- Skill metadata is discovered without injecting the body. Running a Skill is
  explicit, integrity-checked, and read-only; its body and metadata cannot add
  authority.
- Learn accepts only confined, bounded regular-text source snapshots. Its
  private native runner has no tools or MCP, and only schema-validated Markdown
  reaches the transcript. Subscription Learn routes are refused.

### Terminal and concurrency safety

- User, provider, tool, MCP, path, model, and Skill text is projected into a
  bounded terminal-safe representation before display.
- Markdown fence state survives streaming, including tilde fences and wider
  backtick delimiters, so code cannot be styled as trusted prose.
- TUI cancellation waits for the active lifecycle operation to terminate before
  another run starts.
- Slow TUI file, Git, provider, model, session/workspace, and completion work
  runs in cancellable effects. Request and workspace/session identities make
  late results inert; visible error states preserve the last valid snapshot and
  expose a retry where available.
- Tool permissions focus Reject, proposal application focuses Cancel and shows
  the exact target, and quitting cancels active/background work before terminal
  teardown.
- Editor selection, paste, diff, file navigation, and Git-path rendering are
  Unicode-safe and workspace-confined.
- Compact and minimum-size layouts have regression coverage, and color is not
  the only carrier of status or focus.
- Non-TTY interactive launch fails before terminal initialization. `NO_COLOR`
  suppresses frame and cursor color; `MAESTRO_GLYPHS` or terminal/locale
  capability selects Unicode versus single-cell ASCII glyphs.

## Publication checklist

1. Confirm the worktree and index contain only intended release files.
2. Review all new public assets, documentation, license and third-party
   notices.
3. Run the complete release gate and capture its output for the release record.
4. Confirm ownership and publishing access for `BRYANN2K/maestro` and
   `@bryann2k/maestro`.
5. Choose the release version, update every versioned surface, create the
   reviewed release commit, then create and push the matching tag from that
   exact commit.
6. Verify every GitHub asset and checksum, then publish the matching npm
   package.
7. Install through npm, a direct archive, and `go install` in clean fixtures;
   each command must report the chosen version.

No commit, tag, push, GitHub release, or npm publication is implied by this
document.

## Known limitations

- The AES vault key is stored beside its ciphertext. Private permissions and
  corruption handling are hardened, but an operating-system keychain would
  provide stronger protection for long-lived high-value credentials.
- Live requests to every supported third-party provider cannot be part of a
  credential-free release gate. Provider protocol tests use controlled local
  servers; users should begin with a low-cost, non-sensitive task.
- Checksums are included, but keyless artifact signing, SBOM publication, and
  provenance attestations are not part of the first release.
- The npm launcher depends on GitHub Releases being reachable on first install
  of a version. A previously verified cached binary remains local.
- A native cost reservation is only as accurate as provider pricing and the
  planned token estimate. If a dispatched turn ends without a valid completion,
  Maestro cannot know its actual cost and conservatively holds the estimate
  until lease expiry; an underestimated provider charge can still exceed a
  configured cap. Subscription CLIs expose no reliable pre-dispatch estimate,
  so only their valid completion costs enter durable accounting.
- Unix PTY coverage does not exercise Windows Terminal/ConPTY. Job Object
  cancellation has Windows-tagged runtime tests, but terminal teardown has no
  ConPTY integration test and this repository's current CI only cross-compiles
  the Go binary for Windows. Native Windows runtime evidence remains a release
  requirement.
