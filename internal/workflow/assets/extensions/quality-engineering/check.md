# quality-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- Every selected requirement has an explicit oracle and test result linked to the verified commit or artifact.
- Critical scenarios pass in an identified environment; failed or unverified results block progress until correction or explicit contract revision and renewed approval.
- API changes demonstrate expected compatibility with a representative consumer or document the break and migration.
- A selected nonfunctional property, such as p95 latency, is measured under a defined load and window and compared with the contract threshold.
- A detected regression is reproduced and classified, then corrected before passing the criterion or addressed through an explicitly revised and reapproved criterion. The number of test files is not evidence.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **All executed tests pass, but the only test covering the critical risk was disabled.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
