<h1 align="center">Maestro</h1>

<p align="center"><strong>Code in Concert.</strong></p>

<p align="center">Turn an idea into a reviewed change—without losing control of the code.</p>

Maestro is an open-source, spec-driven AI development environment for the
terminal. Its orchestrator turns a conversation into a reviewable spec,
delegates implementation to focused agents, checks the result, drafts the
documentation, and archives the completed work.

```text
idea → spec → build → review → docs → archive
```

The default interface is a responsive TUI with streaming tool cards, explicit
approval prompts, project-aware sessions, task-specific model routes, and an
integrated code workspace. The same lifecycle is available through headless
commands for scripts and CI.

## Install

### npm (recommended)

The npm package is a small launcher for Maestro's prebuilt release
binary. It requires Node.js 18 or newer; it does **not** require a Go toolchain.

```sh
npx @bryann2k/maestro@1.0.0
```

The launcher selects the matching macOS, Linux, or Windows binary for the
current architecture and caches that exact Maestro version locally. Arguments
are forwarded unchanged:

```sh
npx @bryann2k/maestro@1.0.0 --dir ./my-project
npx @bryann2k/maestro@1.0.0 version
```

### GitHub release binary

Download the archive for your operating system and architecture from the
[Maestro 1.0.0 release](https://github.com/BRYANN2K/maestro/releases/tag/v1.0.0),
verify it against `checksums.txt`, and place `maestro` (or `maestro.exe`) on
your `PATH`.

Release assets follow this pattern:

```text
maestro_1.0.0_darwin_arm64.tar.gz
maestro_1.0.0_darwin_amd64.tar.gz
maestro_1.0.0_linux_arm64.tar.gz
maestro_1.0.0_linux_amd64.tar.gz
maestro_1.0.0_windows_arm64.zip
maestro_1.0.0_windows_amd64.zip
```

### Build with Go

With Go 1.26.5 or newer:

```sh
go install github.com/bryann2k/maestro/cmd/maestro@v1.0.0
```

## Quick start

Run Maestro from the repository you want it to understand:

```sh
cd my-project
maestro
```

On first use:

1. Open `/providers` and connect an API provider or an existing CLI
   subscription.
2. Use `/bootstrap` for a new project or `/onboard` for an existing repository.
3. Discuss the change in read-only chat, then invoke `/propose` when the idea is
   ready to become a spec.
4. Review and `/accept` the proposal, choose a branch or worktree, then run
   `/build`.
5. Complete `/review`, `/docs`, and `/archive` when the evidence is ready.

Maestro never treats an approximate chat message as permission to create a
spec. Only `/propose` crosses that boundary.

## The development lifecycle

| Phase | What Maestro does | User control |
| --- | --- | --- |
| Chat | Explores the repository and clarifies intent | Read-only discovery |
| Propose | Creates a structured `spec.md`, `design.md`, and `tasks.md` draft | Explicit `/propose` |
| Accept | Validates the spec and selects the Git workspace | Explicit acceptance |
| Build | Delegates implementation and tests to a development agent | Tool permissions and cancellation |
| Review | Runs deterministic checks, security analysis, and a read-only review agent | Findings can return through `/fix` |
| Docs | Proposes an architecture decision record | Preview before write |
| Archive | Commits and archives an approved, reviewed change | Confirmation; merge is opt-in |

The equivalent headless flow is:

```sh
maestro propose -m "Add a PostgreSQL API"
maestro accept --worktree
maestro build
maestro review
maestro docs
maestro archive --yes --merge
```

## Start or adopt a project

`/bootstrap` asks a short sequence of questions about a new project's purpose,
users, outcomes, stack, constraints, and verification. `/onboard` first performs
a bounded static analysis of an existing repository, then asks the user to
confirm or correct the evidence.

Both flows preview the same root-level `MAESTRO.md` contract. Nothing is written
until the proposal is accepted. Repository discovery does not run installers,
builds, tests, hooks, generators, MCP servers, or network requests. See
[`docs/PROJECT_BOOTSTRAP.md`](docs/PROJECT_BOOTSTRAP.md).

## Sessions and Git workspaces

Each project has durable sessions with a concise generated title, lifecycle
phase, selected spec, pending approvals, review evidence, and exact Git
workspace identity.

```text
/rename API security review     rename the current session
/resume                        browse and restore saved sessions
/git                           select or create a worktree
```

Headless equivalents include `maestro rename <title>`, `maestro resume [id]`,
`maestro git list`, `maestro git create <branch>`, and
`maestro git select <path>`.

## Models, providers, and reasoning

Maestro routes Chat, Build, Review, and Docs independently. Each task can use a
different model and reasoning effort from the model workspace (`Ctrl+L`) or
Settings.

- **Native engine:** Maestro runs its own agent loop and in-process sub-agents.
  This is the default and the only engine that can expose Maestro-managed MCP
  tools.
- **Subscription engine:** Maestro reuses an authenticated vendor CLI such as
  Codex, Claude, Cursor, OpenCode, Grok, or Kimi. The vendor process is still
  constrained by Maestro's role and workspace envelope, but its capabilities
  depend on that installed CLI.
- **Local and compatible providers:** OpenAI-compatible endpoints and local
  services such as Ollama, LM Studio, llama.cpp, and LiteLLM run through the
  native engine.

Use `/model` for a quick model choice, `/providers` to configure connections,
and `/settings` for routes, reasoning, permissions, integrations, skills,
appearance, and editor behavior. Provider credentials are stored in Maestro's
private vault, not in `maestrorc`.

## MCP integrations

Maestro supports configured MCP servers over stdio, Streamable HTTP, and SSE.
`/mcp` shows connection state and exposed tools. Every MCP tool is namespaced,
treated as untrusted, and approval-gated. Name collisions fail closed.

MCP tools are available only to eligible roles on the native engine; Review
remains read-only and Skills/Learn do not inherit MCP authority. Switching
workspaces closes and recreates MCP clients with the new working directory.

```text
/mcp list
/mcp tools all
/mcp reconnect github
```

## Agent Skills

Maestro discovers standard `SKILL.md` metadata from project and user skill
roots. Skills are never selected automatically: the user must inspect or run a
qualified skill ID explicitly.

```text
/skills list
/skills show project:security-review
/skills disable project:security-review --scope=project
/skills run project:security-review
```

Running a Skill is a read-only task. Skill instructions and `allowed-tools`
metadata cannot grant additional file authority, writes, shell, Git, network,
MCP, or secret access. See
[`docs/SKILLS.md`](docs/SKILLS.md) for discovery limits and collision rules.

## Learn and Coach

Coach is an optional, project-local learning layer for developers who want to
move from approximate prompting to evidence-based AI development. It offers one
short exercise at natural lifecycle breakpoints and never blocks delivery.

```text
/learn guided
/learn challenge
/learn next
/learn done
/learn later
/learn status
/learn off
```

`/learn <path> [--deep]` explains a bounded source snapshot and stages a
reviewable learning note. Source explanation uses a native/API route with zero
tools and no MCP; subscription routes fail closed because Maestro cannot prove
their filesystem confinement. The teaching model is documented in
[`docs/COACH_DESIGN.md`](docs/COACH_DESIGN.md).

## Integrated code workspace

Use `/ide` to move between conversation and code without leaving the terminal.
The workspace includes a file tree, multiple buffers, syntax highlighting, Git
gutter, Markdown preview, selection actions, and proposal review. Select code
and choose **Ask Maestro**, **Explain**, **Modify with Maestro**, or **Comment**;
the selected source is added as bounded context.

The editor opens in standard mode. Vim behavior is opt-in under
`Settings → Editor mode`. `/follow` controls live navigation when an agent reads
or changes a source location.

## Focus-first output

Chat, Coach, and Learn put the result or next action first, keep instructions
bounded, and make `Done`, `State`, `Blocked`, `Cause`, `Fix`, and `Next`
explicit. This presentation is a readability feature, not a diagnosis, and it
never rewrites code or machine-readable output. See
[`docs/ADHD_OUTPUT.md`](docs/ADHD_OUTPUT.md).

## Security model

- Chat and Review receive read-only built-in repository tools; any external MCP
  action still passes through the configured permission gate.
- File proposals are staged and previewed before they are applied.
- Git operations validate repository and workspace identity and fail closed on
  ambiguous or dirty state.
- Sessions, checkpoints, Skill state, and credentials use private, atomic
  local storage.
- Provider and MCP output, repository instructions, Skill bodies, paths, and
  terminal text are treated as untrusted input.
- Cancellation propagates through active agents and subprocesses.
- Review evidence is persisted and bound to the exact Git state it evaluated.

Read the full boundary and known limitations in
[`docs/PRODUCTION_READINESS.md`](docs/PRODUCTION_READINESS.md).

## Configuration

Maestro merges a user `maestrorc` with `./maestrorc` and `./.maestrorc`; the
hidden project file has the highest priority. Use the CLI or Settings for
credentials rather than writing API keys into configuration files.

```text
provider add local --type ollama --base-url "http://localhost:11434"
model add local/qwen3-coder --name "Qwen 3 Coder" --context-window 32768 --can-reason

modelRoles:
  default: local/qwen3-coder --reasoning-effort medium

mcp add docs --type stdio --command "my-docs-mcp"
permissions deny bash
```

Run `maestro help` for the complete CLI surface and use `/help` for the
canonical TUI command list.

## Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [Project bootstrap and onboarding](docs/PROJECT_BOOTSTRAP.md)
- [Coach design](docs/COACH_DESIGN.md)
- [Agent Skills](docs/SKILLS.md)
- [Focus-first output](docs/ADHD_OUTPUT.md)
- [Production readiness](docs/PRODUCTION_READINESS.md)
- [Changelog](CHANGELOG.md)

## Development

```sh
make test           # unit and integration tests
make lint           # gofmt, goimports, vet, and staticcheck
make check          # lint plus race-enabled tests
make release-check  # complete local release gate
make build          # bin/maestro
```

Maestro is released under the [MIT License](LICENSE).
