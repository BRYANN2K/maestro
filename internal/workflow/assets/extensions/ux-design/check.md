# ux-design — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- An observational study with a person from the target segment executes the main scenario and separately records context, interventions, observed success/failure, and blockers. The study documents behavior without turning failure into success.
- In usability validation, a person from the target segment reaches the defined outcome without designer intervention; failure remains failed or unverified and does not satisfy the criterion even with a documented workaround.
- The prototype or product exposes relevant loading, empty, error, success, and backtracking states and supports recovery.
- Interaction decisions link to observations or constraints; post-apply checking replays the main scenario in declared contexts with dated observations and verifiable results.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map selected candidates to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically. A criterion without an executed journey remains unverified.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The prototype exists and testing recorded abandonment; this does not satisfy the task-success criterion.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
