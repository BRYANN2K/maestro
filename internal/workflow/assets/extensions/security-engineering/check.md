# security-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- Requests without appropriate authentication or authorization fail with expected codes and behavior on the actually exposed interface.
- Malformed or hostile input causes neither unintended execution nor data disclosure in responses or logs; the negative test is repeatable.
- Secrets are absent from diffs, artifacts, and verification logs; their source and rotation procedure are identified.
- Affected dependencies and images have versions/digests; blocking vulnerabilities are fixed before passing, or the contract is explicitly revised and reapproved with residual risk and dated follow-up.
- A deliverable artifact is linked to its commit and executed controls. Verification covers the actually tested artifact; failed or unverified results block progress until correction or explicit revision and renewed approval.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The authorized path passes, but another user can access the same resource by changing its identifier.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
