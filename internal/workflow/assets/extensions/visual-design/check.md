# visual-design — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- A reader identifies the title, primary action, and system state at declared sizes; observation uses rendered output, not source files alone.
- Text, controls, and essential information meet declared contrast thresholds, with dated tool or manual-review results.
- State meaning remains understandable without color alone; focus, error, success, and disabled states are observable by keyboard or pointer as appropriate.
- The surface remains readable with the longest content and supported zoom/text sizes; screenshots across contexts are retained.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map selected candidates to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically. Scores or unrendered mockups do not validate criteria.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **An attractive mockup is delivered, but real text overflows and a state relies on color alone.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
