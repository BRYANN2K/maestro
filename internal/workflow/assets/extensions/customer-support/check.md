# customer-support — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- A representative user can find the intended channel, submit a request, and receive a response matching the actually delivered version.
- A critical request is categorized, routed, and escalated to an owner within the explicitly chosen deadline; the test observes the ticket, not just configuration.
- The troubleshooting article or message resolves the happy-path scenario and clearly states when to seek human help; a link's existence alone does not establish usefulness.
- First-response, resolution, and satisfaction measures are calculated by channel/reason over a known window and reviewed alongside volume.
- A recurring complaint or request produces a linked action—correction, clarification, or not-applicable decision—and closure is verified.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The FAQ describes the symptom, but its steps enable neither recovery nor escalation for the reproduced case.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
