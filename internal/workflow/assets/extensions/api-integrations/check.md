# api-integrations — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- Every consumed or exposed operation has an identifiable method, path, schema, statuses, version, and owner. Inferred or unconfirmed fields remain `incomplete` or `unverified` and do not pass until the contract is corrected or revised and reapproved.
- An authorization renewal/expiration scenario and an error scenario are executed without exposing secrets; documentation alone does not pass the criterion.
- Recoverable and non-recoverable calls have a timeout/retry policy; replaying a mutation produces at most the intended effect. If idempotency is not applicable, decide and justify that exclusion in the scope and contract before criterion approval, with appropriate verification.
- The stated contract or compatibility test is run against the target version, with dated responses, limits, and discrepancies. A `failed` or `unverified` result requires correction or revision and renewed approval.

During `validate`, the agent maps selected `AC-API-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping. This extension does not make `tasks.md` mandatory.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The mock passes, but no test verifies that a repeated webhook does not duplicate the effect.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation can be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; any contract change requires revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
