# devops-delivery — explore

Make the build and delivery path reproducible, observable, and recoverable.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the whole catalog. Relevant example: create a pipeline that produces a traceable artifact and supports controlled rollback. Out-of-scope example: rewrite text without affecting build, pipeline, or delivery.

This extension examines how a change becomes a verifiable artifact and, when explicitly authorized, a controlled delivery. It covers continuous integration, automation, small batches, environments, change controls, flow observability, provenance, and rollback. Select it during `stip-explore` when changing a pipeline, workflow, integration/deployment mechanism, artifact, delivery practice, or flow measure. The core already handles local apply/check; ordinary application changes do not automatically trigger this extension.

DORA describes delivery-performance capabilities including continuous integration and delivery ([DevOps capabilities](https://docs.cloud.google.com/architecture/devops?authuser=9)). Its metrics page, updated January 5, 2026, describes five measures: change lead time, deployment frequency, failed deployment recovery time, change fail rate, and deployment rework rate ([DORA metrics](https://dora.dev/guides/dora-metrics/)). These are research findings and management vocabulary, not universal obligations or a prescribed branching strategy. GitHub Environments illustrates platform-specific gates and secrets ([GitHub deployment environments](https://docs.github.com/en/actions/concepts/workflows-and-actions/deployment-environments)). SLSA 1.2 specifies progressive provenance levels ([SLSA specification v1.2](https://slsa.dev/spec/v1.2/)). Core v1 does not natively provide CI, trunk-based development, deployment, or DORA metrics.

## Recognize and reuse existing work

For a new project, identify the commit-to-artifact path, build/test tools, environments, pipeline secrets, promotion rules, rollback strategy, provenance metadata, and events for the five selected DORA metrics. For an existing project, examine actual workflows, run history, queues, flaky checks, manual changes, environment differences, downloaded artifacts, and delivery incidents.

Use **established**, **inferred**, **incomplete**, **missing**, and **not-applicable**. A pipeline file does not establish that it ran the current commit; a green badge does not prove artifact contents; DORA measures computed from different definitions are not comparable. Document limited access to histories or environments. Establish a minimal baseline for a new project; for an existing project, compare actual and declared procedures and make the five selected metrics or their non-applicability explicit.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP links a commit to a reproducible build, runs essential checks, produces an identified artifact, and documents manual promotion where present. For a local project without deployment, a reproducible command and hash may suffice. Go deeper according to risk and volume: separate environments, approval gates, reliable parallel tests, controlled caching, SLSA provenance, signatures, progressive rollout, consistently defined DORA metrics, automated rollback, and recovery exercises. SLSA levels are chosen objectives; CI presence does not establish achievement.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`quality-engineering` provides tests; `security-engineering` covers supply-chain controls and secrets; `release-management` defines the artifact contract; `cloud-engineering` provides environments; SRE supplies post-delivery signals; support covers user incidents. Do not equate green CI with overall quality, high frequency with value, or automatic deployment with authorization. Do not introduce trunk-based development, publication, or branching changes into the core without a separate adaptation. An internal documentation note without flow impact is not-applicable.

DORA, GitHub, and SLSA descriptions are contextualized to their sources; the process and criteria are our integration synthesis, not certification or a performance promise.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
