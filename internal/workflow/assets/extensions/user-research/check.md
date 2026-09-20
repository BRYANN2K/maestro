# user-research — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- Every priority question links to a decision and method, with retrievable notes/data showing whether it was answered, partly answered, or left open.
- The report states who participated, inclusion criteria, access needs, and limitations on generalization.
- At least one realistic task is observed on the prototype or product; the report records behavior or observable outcomes, not just stated preferences.
- After sharing, the specification, priorities, or research plan show the decision and next question; reports without traceable effects are insufficient.
- Personal data and recordings are stored according to stated consent; delivered synthesis does not re-identify participants.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map them to unique `AC-n` IDs in the shared contract; the engine does not do this automatically. An `unverified` result remains unpassed without evidence or an approved revised contract.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **An interview guide is ready, but no session has occurred; understanding of users remains unverified.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
