# ai-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The specification identifies task, data, configuration, expected output, tools, and authority boundaries; unmeasured properties remain `incomplete`/`unverified` and cannot pass.
- A versioned dataset covers happy paths, ambiguity, and relevant failures, with labels/rubrics, agreed per-class quality thresholds, and a dated report showing each threshold met. A dataset, report without threshold attainment, prompt, or demo alone is insufficient.
- Every tool call validates arguments and authorization application-side, and a test shows refusal, timeout, or invalid output without prohibited effects.
- Costs, latency, traces, and fallback are observable in the target configuration; any non-applicable measurement is excluded before criterion approval.

During `validate`, the agent maps selected `AC-AI-*` candidates to unique `AC-n` IDs in the combined specification; the engine does not perform this mapping.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The dataset exists and evaluation runs, but the critical class misses its quality threshold.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
