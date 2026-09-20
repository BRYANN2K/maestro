# Built-in Maestro harness

This source tree runs a single Maestro harness. It does not invoke Codex,
Claude, Cursor, OpenCode or other agent CLIs. `openai-codex` is the name of an
OpenAI account transport in the bundled provider library, not an external
Codex process. Existing retired routes are preserved separately when settings
are migrated. Existing specifications and session files are retained.
If a saved session's checkout now uses another branch, TUI startup opens a fresh
session on the current checkout and explains the change. The old record remains
unchanged. Explicit resume and noninteractive commands still require its original
workspace identity; approvals and review evidence never transfer to the new branch.

## Build and start

Prerequisites: Go 1.26.5+, Bun 1.3.14, Python 3.11+ available as `python3`.

```sh
make build
./bin/maestro --dir /path/to/project
```

The complete installation contains `maestro`, `maestro-ui`, `maestro-runtime` and the matching
`pi_natives.*.node` library. Keep them together. Bun and Go are build dependencies;
Python remains required for Stipulate and RLM. A missing harness fails explicitly.
`MAESTRO_RUNTIME` can point to an absolute trusted runtime executable for tests.
Maestro never searches the project or PATH for an alternative agent harness.

## Providers

Open `/providers`, connect an account or add an API provider, then select the
model in `/model` or the model workspace (Ctrl+L). Built-in account adapters are
OpenAI account access, Anthropic, Google Gemini CLI account access and GitHub
Copilot. Login and token refresh execute in the bundled runtime; credentials are
stored in Maestro's private vault. No installed vendor CLI is required.

Custom OpenAI-compatible endpoints enable model discovery by default. The host
queries `/models` using the configured key and additional headers. Catalog refresh
is bounded and preserves the previous snapshot on failure. For providers without
usable discovery, declare models explicitly. A model listing alone does not prove
context size, pricing, tool support or reasoning levels; supply correct metadata
when the endpoint does not advertise it. Account model and effort metadata comes
from the pinned oh-my-pi catalog. Unsupported explicit variants fail before launch.

## Stipulate contracts

The Python engine and domain extensions come from the user's Stipulate project.
The coordinator is ported from its OpenCode plugin; it uses Maestro session and
provider adapters. Trusted engine code is embedded and extracted privately, never
loaded from an executable script in the project.

```text
/workflow bootstrap
/workflow explore my-change --title "My change"
/workflow plan my-change --file /absolute/path/plan.json
/workflow validate my-change
/workflow approve my-change --by Bryan --ack-user-approval
/workflow start my-change
/workflow delegate my-change implement
/workflow contribution my-change implement accepted "Reviewed changes and evidence"
/workflow check my-change --results /absolute/path/results.json
/workflow docs my-change --summary "Updated usage" --paths README.md
/workflow archive my-change --message "feat: implement approved change" --paths src README.md
```

These are examples of explicit user actions. Review proposal.md, spec.md and the
execution plan before approval. The coordinator can prepare these artifacts through
its gated `stipulate` tool; plan JSON can be passed as `stdin` with `plan --file -`.
Approval and archive are excluded from this tool. Shell and RLM permissions remain
code-execution authority and must not be mistaken for a tamper-proof approval sandbox.

A minimal execution plan looks like:

```json
{
  "version": 1,
  "tasks": [{
    "id": "implement",
    "role": "backend",
    "phase": "apply",
    "criteria": ["AC-1"],
    "depends_on": [],
    "write_paths": ["src"],
    "objective": "Implement the behavior specified by AC-1."
  }]
}
```

Approval binds the current contract version. The imported engine enforces DAG,
criteria and path rules; the coordinator reserves ownership before dispatch and
keeps uncertain launches reserved. A worker returning successfully produces a
pending contribution, not an accepted change. Review and evidence are distinct.

Workers run synchronously in the selected checkout with read/search and scoped
write tools. They cannot invoke shell, MCP, RLM, lifecycle transitions or nested
workers. The coordinator runs tests through its approved tools and records evidence.
Parallel writers and detached worker execution are not enabled in this adapter.
Use the Git workspace selector before starting work when an isolated worktree is
needed; Stipulate does not automatically create one for each task.

Install shipped domain extensions with `/workflow install-extensions <id> ...`;
list availability with `/workflow extensions`. The 29 upstream domain bundles
are included. Existing `clients.opencode` profile settings are accepted as a
migration fallback; new configuration belongs under `clients.maestro`.

`/workflow profiles` displays settings, effective profiles, catalog and the
`settingsRevision`. To save, pass a JSON request file containing `scope`
(`project`, `local` or `personal`), that exact `revision`, and a `settings` object.
The coordinator refuses stale revisions. Scope precedence is personal, project,
then local; files are `~/.config/maestro/stipulate.json` (or XDG_CONFIG_HOME),
`.workflow/config.json` and `.workflow/local.json` respectively. Role, phase and
phase-role overrides retain the upstream resolution rules. Fast mode is not
advertised by the Maestro host and explicit unsupported requests are rejected.

Once `.workflow/config.json` exists, the old `/propose` → `/archive` command family
refuses to operate on a second contract authority. Old specs remain readable.

## RLM

The Prime Agent Python kernel runs a persistent namespace within one native agent
run. It supports successive cells, top-level await, bounded output, timeout,
interrupt and cleanup. This is a shell-equivalent tool requiring explicit tool
permission; it is not an OS sandbox. The host gateway exposes approved Stipulate
task delegation to the coordinator. Arbitrary recursive spawning is unavailable.
Workers do not receive the kernel. State is private to the run and is not restored
from untrusted pickle files across sessions.

## Terminal workspace

The default workspace uses OpenTUI / React with an English phase ribbon,
a changes rail, contract review, and conversation. The palette is ink, ivory
and lavender. `Ctrl+K` opens workflow actions; `Ctrl+P` opens account/API
connections; `Ctrl+L` chooses a model. Inspection never starts execution.
See [TUI.md](TUI.md) for the complete interface guide. The earlier editor is
available through `maestro tui --classic`, using the same native harness.

## Provenance and qualification

Exact sources and licenses are recorded in `runtime/upstream.json`,
`RUNTIME_LICENSES/`, `runtime/LICENSE.oh-my-pi`, and the embedded asset licenses:

| Component | Upstream commit | Integration |
| --- | --- | --- |
| can1357/oh-my-pi | `10b867cb2eeb7809b883a88dfebe1919ff0c2764` | Forked agent scheduler, pinned 18.2.6 provider/catalog/native packages |
| PrimeIntellect-ai/prime-agent | `e311d6495124cf0bdc629c813fc97a39a9a3054d` | Embedded Python RLM kernel and lifecycle helpers |
| BRYANN2K/stipulate-skills | `318902a0c92197b5d95efeb10e61f6bb49d44993` | Workflow engine, 29 extension bundles, adapted plugin coordinator |

The embedded Stipulate approval command adds an optional `--expected-digest`
check under the existing workflow lock, so the UI cannot approve a changed
contract. This is a local adaptation of the pinned source above.

The Go host owns canonical history, permission checks, stream budgets and actual
tool execution. The forked scheduler communicates through bounded private JSONL
pipes. Account-specific signed/encrypted provider state is retained for subsequent
turns with the same provider and model. Refreshed credentials must be durably saved
before inference proceeds.

`make runtime-test` compiles and exercises the real harness against local provider
fixtures, including denied tools, tool-result history, streaming, approval before
provider dispatch, contribution acceptance and worker ownership. It also runs the
imported coordinator tests. `make check` includes Go race/quality checks, npm
launcher tests and runtime integration. A separate PTY review covers three sizes
and color profiles, artifact navigation and terminal restoration.

Local fixture success does not qualify live account login, provider inference or
billing. Those require a real account and were not exercised for this change.
Cross-compiling the five bundles verifies packaging, not execution on foreign
operating systems. The new bundle has not been published.
