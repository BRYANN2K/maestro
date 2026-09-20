# release-management — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The chosen version matches the verified public API change, with a rationale consumers can understand.
- Notes describe added, changed, removed, fixed, or security-related behavior and migration where needed.
- The downloaded or tested artifact matches the expected digest and is linked to its commit/build; being listed in a release is insufficient.
- An immutability or signature check passes for the chosen channel, or its absence is recorded as a risk and prevents stronger claims.
- A breaking change is rejected by the compatibility test or accompanied by an explicitly revised and reapproved version/migration.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The archive is created, but the artifact does not install on a platform claimed as supported.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
