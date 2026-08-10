# Production readiness

Audit date: 2026-08-10

Target release: Maestro 1.0.0

## Decision

The codebase is a release candidate for the first public release. Publication
must use one reviewed, clean commit for which every gate below passes. A local
build from a dirty worktree is useful evidence, but it is not a releasable
artifact.

Maestro's user interface is a terminal application. The frontend in this audit
is the Bubble Tea conversation TUI and its integrated code workspace; there is
no browser application in the release.

## Release contract

One Git tag, one GitHub release, one npm package, and the embedded binary
version must all be `1.0.0`.

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
  and diagnostics, and propagate failure and cancellation.

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

### MCP, Skills, and Learn

- MCP discovery and responses are cancellable and bounded. Tools are
  namespaced, collisions fail closed, and every call passes through the
  configured permission gate.
- MCP clients are closed and rebound when the active workspace changes. MCP is
  not exposed to Review, Skills, Learn, or subscription routes.
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
- Editor selection, paste, diff, file navigation, and Git-path rendering are
  Unicode-safe and workspace-confined.
- Compact and minimum-size layouts have regression coverage, and color is not
  the only carrier of status or focus.

## Publication checklist

1. Confirm the worktree and index contain only intended release files.
2. Review all new public assets, documentation, license and third-party
   notices.
3. Run the complete release gate and capture its output for the release record.
4. Confirm ownership and publishing access for `BRYANN2K/maestro` and
   `@bryann2k/maestro`.
5. Create the reviewed release commit, then create and push `v1.0.0` from that
   exact commit.
6. Verify every GitHub asset and checksum, then publish the matching npm
   package.
7. Install through npm, a direct archive, and `go install` in clean fixtures;
   each command must report `1.0.0`.

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
