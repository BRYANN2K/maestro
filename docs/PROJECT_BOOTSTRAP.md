# Project bootstrap and adoption

`/bootstrap` prepares the Maestro contract for a greenfield directory. Before
discovery, it verifies that the selected directory is a Git repository and runs
`git init --initial-branch=main` when needed. It does not stage files or create
a commit; `/accept` establishes the safe baseline required for its worktree.
`/adopt` statically analyses a brownfield repository and reconciles the same
contract. `/onboard` remains an alias for `/adopt`; `/resume` remains reserved
for loading a Maestro session.

There is no setup modal. The normal transcript is the setup surface:

- if the user discusses the project first, Maestro extracts only decisions the
  user confirmed and asks for the material gaps;
- if `/bootstrap` is the first command, Maestro asks focused questions about
  purpose, users, stack, non-goals, safety boundaries, and verification;
- `/adopt` combines those confirmed decisions with bounded repository evidence,
  then asks only for facts that static analysis cannot establish;
- if `/propose` is invoked without MAESTRO.md, Maestro chooses greenfield or
  brownfield setup, preserves the complete proposal request, and resumes it
  only after the user accepts MAESTRO.md.

Both commands propose, but do not immediately write, one root `MAESTRO.md`.
Git initialization is the only eager mutation made by an explicit greenfield
bootstrap. The proposal is an atomic contract: it is accepted or declined as a
whole. Its `content_fingerprint` binds the exact reviewed bytes, while
`evidence_fingerprint` records the bounded repository facts used to generate
the contract.
The user reviews the exact diff through Maestro's existing proposal flow.

## `MAESTRO.md` contract

The document is concise, deterministic and project-wide. It contains:

1. project purpose, users, outcomes and non-goals;
2. repository units, entry points and architecture boundaries;
3. stack, toolchain, versions, managers and lockfiles;
4. canonical setup, development, test, lint, format, typecheck, security,
   build and release commands with their working directories;
5. data, secret, migration, deployment and destructive-action boundaries;
6. verification contract and definition of done;
7. evidence, confidence, conflicts and explicit unknowns.

It never contains a session transcript, current branch or HEAD, timestamps,
secret values, personal endpoints, generated tutorials, or task-specific
instructions. Identical evidence and answers produce byte-identical output.

## Discovery boundary

After `/bootstrap` has ensured the local Git repository, discovery is static.
It may inspect Git metadata, manifests, task runners, lockfiles, CI
configuration and bounded documentation. It never runs install, build, test,
package lifecycle, generator, repository hook, shell profile, MCP server or
network request.

Before reading a file, Maestro verifies that the path is confined to the
canonical repository and that it is regular, non-symlink, bounded and textual.
Secret-like files, credentials, private registries, cloud/Kubernetes state and
Terraform state are denied. Repository instructions are evidence candidates,
not executable policy.

`/adopt` is re-entrant. A repeated scan with unchanged evidence is a no-op;
drift is presented as a diff. Existing conflicting human content is never
silently overwritten.

## Research basis

- GitHub repository instructions emphasize concise project summary, validated
  build/test commands, architecture and CI evidence:
  <https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/add-custom-instructions/add-repository-instructions>
- AGENTS.md provides the interoperable “README for agents” model:
  <https://github.com/agentsmd/agents.md>
- Git's stable, NUL-delimited worktree and status formats are the parsing
  boundary for arbitrary POSIX paths:
  <https://git-scm.com/docs/git-worktree> and
  <https://git-scm.com/docs/git-status>
- NIST SSDF structures project preparation, software protection, secure
  production and vulnerability response:
  <https://csrc.nist.gov/Projects/ssdf>
- OpenSSF's project security baseline and Scorecard inform the security
  evidence section:
  <https://baseline.openssf.org/> and <https://openssf.org/scorecard/>
