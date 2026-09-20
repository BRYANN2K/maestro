# cli-tooling — explore

Build a stable command-line interface for people and automation.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: add a JSON command that reports errors and interruptions cleanly. Out-of-scope example: change a graphical component without affecting commands or automation.

This extension turns a technical operation into a stable, readable, scriptable CLI: arguments, subcommands, help, exit codes, stdin/stdout/stderr, configuration, authentication, formats, errors, and compatibility. Select it during `stip-explore` when adding or changing a command, subcommand, flag, script-consumed output, or administration procedure, or when an internal API becomes a terminal tool for people or automation.

It does not apply to disposable private scripts without a promised interface or libraries without terminal invocation. It does not impose POSIX, Rust/clap, or GitHub CLI when the project already has coherent conventions. POSIX describes utility syntax and argument conventions ([POSIX Utility Conventions](https://pubs.opengroup.org/onlinepubs/9699919799/basedefs/V1_chap12.html)); access was partially limited during research. GNU also discusses options and POSIX compatibility ([GNU Coding Standards](https://www.gnu.org/prep/standards/standards.html)), with the same access limitation. These are compatibility references, not universal answers for every CLI.

## Recognize and reuse existing work

For a new project, identify command names, subcommands, help conventions, exit codes, machine/human formats, environment variables, configuration files, authentication, examples, and installation. For an existing project, run `--help`; test stdin, redirected output, absent TTY, network errors, interruption, and the previous version; inspect scripts consuming the output. Classify findings as **established** (reproducible command and result), **inferred** (unexecuted intent), **incomplete** (a platform or mode missing), **missing** (documented search found no trace), or **not-applicable** (no terminal contract). Manual success does not establish stable JSON; a documented flag does not prove its exit code.

clap documents parsing with help, suggestions, colors, completion, version information, tests, and errors ([clap documentation](https://docs.rs/clap/latest/clap/)). GitHub CLI distinguishes terminal/script usage and authentication ([GitHub CLI manual](https://cli.github.com/manual/)); its formatting page covers default output, `--json`, `--jq`, and `--template` ([gh help formatting](https://cli.github.com/manual/gh_help_formatting)). The useful invariant is separating human output from the machine contract; exact flags remain project choices.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes a happy-path invocation, `--help`, invalid-input validation, a nonzero failure code, errors on stderr, and an executable example. Go deeper for public, CI-used, destructive, authenticated, or tool-consumed commands: version machine schemas; test stdin/TTY, pagination, retries, interruptions, previous-version compatibility, redaction, and completion. Do not add long options or JSON merely because another CLI has them.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`api-integrations` defines remote calls and authentication; `backend-engineering` owns permissions and effects; `desktop-engineering` may provide shell/protocol integration; `database-engineering` may expose migrations. Avoid mixing logs with machine data, diagnostics on stdout, assumed TTYs in CI, removing destructive-action confirmation, or breaking scripts by adding a sentence. POSIX/GNU and clap are contextual references, not automatic interface stability. An unshared script without a contract does not trigger this extension.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
