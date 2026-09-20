# product-strategy — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification connects the change to a problem without prescribing a mandatory solution; an outside reader can identify audience, context, and exclusions.
- Every critical hypothesis has a source, experiment, or explicit `unverified` status, with retrievable evidence at the stated path/URL; `unverified` cannot pass the criterion.
- At least two options, or a documented rationale for one option, are compared against the same outcome and risk.
- After apply, verification executes the promised measurement/test and reports a dated observation; file existence alone is insufficient.

Candidate IDs belong to this extension reference. During `stip-validate`, explicitly map them to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically. An `unverified` criterion cannot pass check without evidence or an approved contract revision.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **A roadmap and promise exist, but no observation supports the need or decision criteria.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
