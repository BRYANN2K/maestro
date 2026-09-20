# cloud-engineering — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- The infrastructure plan produces the expected resources in a controlled environment, with a second run causing no unexpected change: idempotency or its equivalent.
- Unauthorized access paths are denied and workload identities have only necessary permissions, verified against active configuration.
- A representative failure or contract-defined restore is executed, with duration and data loss compared against the chosen RTO/RPO.
- Health signals, useful logs, and alert thresholds detect the agreed behavior; a dashboard alone is not evidence.
- The resource, owner, budget, and retirement procedure are identifiable. Any authorized deployment is distinguished from local closure through `archive`.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **The IaC plan passes, but no restore is tested although the contract requires an RTO/RPO.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation can be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; any contract change requires revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
