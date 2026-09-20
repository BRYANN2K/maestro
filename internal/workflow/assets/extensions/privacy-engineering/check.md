# privacy-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The system collects and retains only fields necessary for the stated purpose; payload and storage tests confirm no unnecessary fields.
- Initial settings do not expose data beyond intended purposes and recipients; the new-account scenario is verified.
- An authorized user or operator can exercise the specified behavior—access, correction, or deletion—and independent verification confirms the effect on affected copies.
- Logs, traces, exports, and test environments contain no unnecessary personal data; exceptions are justified and controlled.
- Processing notices or documentation reflect actual flows, with identified owners, retention periods, and providers; a page's existence does not establish accuracy.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The primary row is deleted, but a copy containing personal data remains accessible in logs.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
