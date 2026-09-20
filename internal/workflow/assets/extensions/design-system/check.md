# design-system — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The inventory records the search for existing components and justifies reuse, extension, local creation, or forking with observable usage and risks.
- The component or pattern is rendered in at least two declared contexts, with realistic content examples and dependency versions.
- Verification covers component interaction, content, and accessibility in context; documentation alone is insufficient.
- Consumers can identify ownership, version, installation, limits, update strategy, and reporting channels.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map selected candidates to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The isolated component passes its tests, but an existing consumer breaks with the new API.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
