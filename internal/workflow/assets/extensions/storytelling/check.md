# storytelling — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The contract contains intent, audience, expected action, and a promise whose factual elements point to evidence or limitations; unsupported promises remain unverified.
- The hierarchy distinguishes central message, supporting messages, evidence, and caveats; a target-segment reader can paraphrase the problem, value, and next action without writer assistance.
- Affected landing/entry surfaces and onboarding carry a consistent promise connected to an actual product action; text existence alone is insufficient.
- After product, data, or scope changes, the owner reassesses messages, dates the version, and removes or corrects inaccurate claims.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map them to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically. Unproven criteria remain unverified and cannot pass check without evidence or an approved contract revision.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The narrative is consistent across pages but promises a capability absent from the delivered product.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
