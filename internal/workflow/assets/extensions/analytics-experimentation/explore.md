# analytics-experimentation — explore

Connect a hypothesis, instrumentation, and analysis method to a measurable decision.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: instrument and prepare a conversion experiment with a primary metric and guardrails. Out-of-scope example: correct a typo without a measurement question or product hypothesis.

This extension turns product or behavioral hypotheses into interpretable measurement: instrumentation, metrics, segments, baselines, experiments, decisions, and follow-up. Select it during `stip-explore` when a change promises measurable behavioral improvement, modifies an event/dashboard, introduces A/B testing, requires usage-informed decisions, or changes an analytical instrument another team depends on.

It does not apply to mechanical fixes without a measurable outcome or decorative experiments without hypotheses and decisions. It does not turn all usage into surveillance or choose statistics without context. Google's adaptable HEART framework connects product goals, experience signals, and metrics ([Measuring the User Experience on a Large Scale](https://research.google/pubs/measuring-the-user-experience-on-a-large-scale-user-centered-metrics-for-web-applications/)). Microsoft describes ExP as supporting hypothesis validation, impact measurement, and safe iteration ([Experimentation Platform (ExP)](https://www.microsoft.com/en-us/research/group/experimentation-platform-exp/)). These are team frameworks, not universal recipes.

## Recognize and reuse existing work

For a new project, identify goals, populations, events, metric definitions, ownership, baselines, consent, segments, duration, assignment methods, and expected decisions. For an existing project, verify emitted events, identifier quality, missing windows, dashboard/raw-data consistency, previous experiments, and decisions. Classify findings as **established** (reproducible event/measurement), **inferred** (hypothesis), **incomplete** (missing segment, period, or instrument), **missing** (explicit search found no trace), or **not-applicable** (no usage-based decision). A dashboard does not establish metric validity; an overall increase does not prove causality.

Optimizely describes sequential testing, always-valid p-values, false discovery rate control, and CUPED as its Stats Engine method ([Stats Engine deep dive](https://certification.optimizely.com/docs/concepts/experimentation/stats-engine-deep-dive/)). Use it as an example of explicit statistical choices, not a mandatory standard. Microsoft describes checking Sample Ratio Mismatch on assignment counts before effect analysis, using a chi-squared test and a conservative p < 0.0005 threshold in that article ([Diagnosing Sample Ratio Mismatch](https://www.microsoft.com/en-us/research/articles/diagnosing-sample-ratio-mismatch-in-a-b-testing/)). It also addresses repeated measurements, peeking, multiple testing, and predetermined duration ([Patterns of Trustworthy Experimentation: During-Experiment Stage](https://www.microsoft.com/en-us/research/articles/patterns-of-trustworthy-experimentation-during-experiment-stage/)). These documented platform choices become local requirements only if adopted by the contract.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes a hypothesis, primary metric, baseline, tested instrumentation, population, decision rule, assignment check, and dated report. Go deeper for revenue, safety, health, multiple segments, low traffic, or irreversible exposure: preregister analysis and guardrails, set thresholds by metric class, simulate power/MDE, check SRM and assignment, address missing data/interference, and document peeking, multiple-comparison adjustments, and post-deployment follow-up. Do not call correlation a gain or before/after differences causal without suitable design or explicit qualification.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`product-strategy` selects outcomes and decisions; `user-research` supplies qualitative context; frontend/backend instrument; `data-engineering` supports windows, quality, and lineage; `privacy-engineering` bounds consent and retention. Avoid changing metrics/windows afterward, inspecting only the primary result, excessive segmentation, confusing significance with usefulness, or A/B tests without sufficient traffic. Stats Engine is an Optimizely implementation and HEART a Google framework; neither removes the need to justify project design. A technical counter without a user-related decision does not trigger this extension.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
