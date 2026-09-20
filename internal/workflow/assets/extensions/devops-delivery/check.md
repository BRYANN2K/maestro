# devops-delivery — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The tested artifact's build is reproducible or its differences are explained; commit, dependencies, and digest are recorded.
- A required control actually fails on invalid input, and the workflow does not publish or promote the artifact after that failure.
- Pipeline permissions and secrets are minimal and tested against active configuration; logs contain no secrets.
- Provenance identifies at least the input, builder, procedure, and output when that level is selected; a claimed SLSA level is verified against its version.
- The five selected DORA measures—change lead time, deployment frequency, failed deployment recovery time, change fail rate, and deployment rework rate—use documented definitions and windows or are declared not-applicable with justification. They are not presented as a universal score.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The pipeline is green but rebuilds a different artifact during promotion.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
