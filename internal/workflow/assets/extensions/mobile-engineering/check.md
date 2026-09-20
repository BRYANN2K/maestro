# mobile-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification names platforms, versions, and evidence devices; a journey resumes after the defined interruption with expected data.
- Success, loading, network errors, and permission denial are triggered in the target build; an unrun test remains `unverified` and cannot pass through justification.
- Relevant storage, network, authentication, and privacy controls have verification or are excluded before approval; citing MASVS without a test is not evidence.
- Claimed builds and tests produce dated reports on selected platforms, with observable crash/ANR behavior and a withdrawal procedure.

During `validate`, the agent maps selected `AC-MOB-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The simulator shows the screen, but resuming after suspension loses an operation in progress.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
