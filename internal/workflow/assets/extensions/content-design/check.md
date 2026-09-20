# content-design — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- At each targeted task step, content states the action, object, conditions, and next step; a person from the segment can paraphrase the message without writer assistance.
- Labels, headings, instructions, and errors use established vocabulary and exactly match rendered controls/states; source-file presence does not validate this criterion.
- At least one observation or comprehension test covers critical content, including error/recovery, and the person reaches the defined paraphrasing or action level. Recording misunderstanding does not pass until resolved or the contract is revised and reapproved.
- Delivered content has a source of truth, owner, version, and revision trigger; identified duplication or stale links have documented decisions.
- In-scope language, accessibility, and translation requirements are verified in declared surfaces/contexts, with remaining limits explicit.

Candidate IDs belong to this reference. During `stip-validate`, explicitly map them to unique `AC-n` IDs in `spec.md`; the engine does not do this automatically. An unverified criterion cannot pass check without evidence or an approved contract revision.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The text follows the style guide, but readers still do not understand how to correct their error.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
