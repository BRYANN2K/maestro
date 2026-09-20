# database-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification connects each schema change to a versioned migration, a constraint, or a not-applicable decision made before approval; a manual schema alone is insufficient.
- A concurrency, duplication, or transaction test verifies the selected invariant on the target engine; justification cannot make `failed` or `unverified` pass.
- The critical query has an estimated plan (`EXPLAIN`) and, if actual measurement is committed to, a dated `EXPLAIN ANALYZE` on controlled data and environment, with a threshold and management of write effects. If performance is out of scope, record that exclusion before including the criterion.
- Claimed migration, permission, and restoration checks run in the defined environment, with observations and limitations retained.

During `validate`, the agent maps selected `AC-DB-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The migration passes on an empty database but has not been exercised against representative existing data.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation can be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
