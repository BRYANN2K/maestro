# desktop-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification lists affected platforms, windows/webviews, permissions, and IPC channels; unproven capabilities remain `incomplete` or are excluded before approval.
- An installed journey verifies launch, a native action, permission denial, and closure/resumption; justification cannot make `unverified` or `failed` pass.
- Webviews and native operations enforce the intended least privilege, with a dated configuration audit or IPC-test observation.
- The tested packaged artifact is identified by version/hash, with a discoverable support, capability-removal, or rollback procedure.

During `validate`, the agent maps selected `AC-DESK-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The binary compiles, but the real journey still fails and leaves an unrecoverable draft.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
