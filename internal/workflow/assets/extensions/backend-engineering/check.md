# backend-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- An end-to-end scenario shows inputs, validation, authorization, dependencies, and outputs; error states and sensitive data are explicit.
- Reported tests verify success and at least one relevant failure: duplication, timeout, permissions, or concurrency according to risk. A justification cannot turn a `failed` or `unverified` test into a pass.
- If the contract requires signal correlation, a request or task can be traced end to end. Missing instrumentation fails the criterion and must be corrected or the contract revised and reapproved before `check`. An isolated log is insufficient.
- Migration and deployment are exercised in the defined environment with a rollback procedure or an approved irreversibility decision; observations are dated.

During `validate`, the agent translates selected `AC-BE-*` candidates into unique `AC-n` IDs in the combined specification; the engine does not perform this mapping. A `tasks.md` file is not mandatory.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The happy path succeeds, but a retry credits the same account twice in the fixture.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; any contract change requires revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
