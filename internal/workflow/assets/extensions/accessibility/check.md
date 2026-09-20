# accessibility — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The declared critical task works using only a keyboard with visible focus, predictable order, and observable activation/state feedback; automated reports alone are insufficient.
- Nontext information, fields, controls, errors, and dynamic changes have alternatives or accessible names verified in the intended environment.
- A matrix links every selected WCAG/contractual requirement to a state, test method, date, and result; untested requirements remain unverified.
- Critical journeys/components are replayed with declared browsers/technologies, and blocking defects have verified fixes. Default acceptance of a defect leaves the criterion unverified until an explicitly approved contract revision.
- In-scope statements/audits identify coverage, limits, known issues, contact paths, remediation plans, and a version.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map them to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically. Unverified criteria cannot pass check without evidence or an approved contract revision.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The automated scan passes, but keyboard focus leaves the dialog and prevents task completion.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
