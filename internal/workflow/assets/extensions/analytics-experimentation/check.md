# analytics-experimentation — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification connects hypothesis, population, primary metric, baseline, and expected decision; each definition is traceable in data or marked `incomplete` before approval.
- Claimed exposure and assignment are observed in a dated report with guardrails and a pause rule. The planned SRM check passes or its discrepancy is diagnosed before any effect conclusion. A narrative cannot make `failed` or `unverified` pass.
- Analysis uses the declared method/window, compares critical metrics with agreed thresholds, describes uncertainty, segments, and missing data, and does not turn repeated interim inspection into an implicit conclusion.
- A post-experiment decision and post-launch observation are retained; before/after analysis is explicitly noncausal unless supported by additional design. Any non-applicable test is excluded before criterion approval.

During `validate`, the agent maps selected `AC-AN-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **A dashboard shows an increase, but assignment is imbalanced and the analysis window has not ended.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
