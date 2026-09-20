# frontend-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification identifies the surface and success, loading, empty, and error states; each can be triggered through identifiable data or a test scenario.
- The primary interaction works with a keyboard, observable focus, and an accessible name; attach inspection or repeatable-test evidence. Missing evidence remains `unverified`.
- Claimed builds and journey tests pass on the defined target, or the criterion is revised and reapproved; textual justification does not turn failure into success.
- For surfaces with a performance budget, the chosen measurement, such as LCP/INP/CLS, runs in a dated environment and is compared with the baseline; irrelevant scope is excluded or criteria adjusted before approval.

During `validate`, the agent maps selected `AC-FE-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping. The extension does not require `tasks.md`.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The build succeeds, but the browser shows an empty list because a dependency service is unavailable.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
