# cli-tooling — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- `--help` or equivalent describes invocation, arguments, defaults, errors, and examples; help is generated or checked with a dated command.
- Success, invalid input, and refused operations produce contract-compliant exit codes and stdout/stderr streams; an explanation cannot make `failed` or `unverified` pass.
- The promised machine-readable format works in a pipe without decorative text and matches its versioned schema/fixture. Any exclusion is decided before approval.
- CI/non-interactive and interactive invocations are replayed with expected results and no secrets in output; the report is dated.

During `validate`, the agent maps selected `AC-CLI-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The command works in a terminal but prints a banner to stdout, breaking JSON in a pipeline.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation can be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
