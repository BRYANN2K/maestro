# build-in-public — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- Every public artifact identifies audience, objective, status, version/date, owner, scope, and limits; a public link without metadata is insufficient.
- A sharing check verifies authorization, minimization, and handling of secrets, personal data, confidential information, and security details according to scope, retaining results with the published version.
- Roadmaps and forecasts distinguish exploration, design, preview, delivered, and withdrawn states, explicitly explaining that intentions or indicative dates are not guarantees where they may be mistaken for them.
- Feedback channels, triage owners, and critical-feedback statuses are accessible; public requests are not product decisions without recorded evidence and decisions.
- Product, evidence, or risk changes trigger dated updates, corrections, or retirement, linking prior versions when safe and relevant or explaining withdrawal when confidentiality prevents it.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map them to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically. Unverified criteria cannot pass check without evidence or an approved contract revision.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The draft was reviewed but contains a secret or presents a prototype as available; it cannot be declared ready.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
