# data-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification identifies source, owner, grain, window/partition, output dataset, and consumers; unconfirmed information remains `incomplete`/`unverified` and cannot pass.
- Rerunning the same window produces expected output without duplicates or divergent effects, or an exclusion is agreed before approval for an explicitly non-replayable stream.
- Selected quality tests, such as schema, completeness, uniqueness, or freshness, run on a dated partition and their report is retained.
- The run and dataset are traceable with ownership, version, and required lineage; `failed`/`unverified` requires correction or revision and renewed approval.

During `validate`, the agent maps selected `AC-DATA-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The pipeline finishes without errors, but rows disappear during replay.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation can be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
