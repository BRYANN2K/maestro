# seo-discoverability — check

The points below are **candidates**, not a mandatory checklist or already approved criteria. During explore/validate, the agent selects and reformulates useful candidates as actual, unique `AC-n` criteria in `spec.md`. During check, verify only that contract; do not add requirements on the fly.

## Observable properties to adapt

- A selected public URL returns expected content without unintended robots/noindex blocking; the test observes the actual response.
- Title, primary heading, text, and links represent the stated intent and appear in crawler-accessible rendering.
- If selected, the sitemap contains only relevant absolute canonical URLs and respects limits; generation is verified.
- Structured data matches visible content and passes an appropriate validator/test; no rich-result promise is inferred.
- Redirects, canonicals, or page removal preserve contractual behavior for consumed URLs and are tested against a real sample.

## Reconcile evidence

For each `AC-n`, compare observations with the expected outcome, recording the command or protocol, environment/version, covered data, and limits. A file's existence, a test merely being written, or a tool finishing is not sufficient evidence of behavior. Distinguish simulated tests, real observations, and objectives requiring an operational observation period.

Counterexample to report: **Metadata exists, but the public page remains blocked by robots or noindex.** Link the discrepancy to the applicable criterion; correct it or report `failed` / `unverified`. A failure observation may be useful without satisfying a success criterion. Do not change criteria or thresholds to make them pass; contract changes require revision and renewed approval.

The core requires a report with the current subject digest and every exact criterion ID. The engine checks structure, statuses, and digests; it does not certify the truth of observations or domain relevance. Read the [evaluation cases](evaluation.json) to exercise selection and adoption judgment without presenting them as domain tests that have actually run.
