# sre-operations — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The selected SLI is calculated from a defined source, population, and window and corresponds to observable user behavior.
- The service meets the SLO target over the specified window in an identified environment or dataset; excluded periods are justified.
- An alert fires on simulated or actual degradation and identifies a runbook action; decorative alerts do not pass.
- A representative failure or restoration produces a result compared with the RTO/RPO or selected target.
- Post-delivery measurements have an owner and deadline; their absence cannot be hidden by local `archive` closure.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The dashboard exists, but a representative failure triggers no actionable alert.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
