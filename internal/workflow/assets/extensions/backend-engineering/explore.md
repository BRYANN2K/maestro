# backend-engineering — explore

Ensure consistent server behavior across success, failure, concurrency, and recovery.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: add a worker that applies a business rule without duplicate effects after a retry. Out-of-scope example: change only a screen's typography with no service impact.

This extension turns a service need into operable server behavior: contracts, validation, authorization, error states, persistence, concurrency, observability, deployment, and recovery. Select it during `stip-explore` when a change affects an API, worker, asynchronous task, server-side business rule, queue, database, or production service behavior. Even a small change may warrant it when it touches a security boundary, transaction, or availability objective.

It does not apply to purely visual or documentation changes without a service impact. It does not replace `database-engineering`, `api-integrations`, or operations: it makes their shared contracts and executable path explicit. The Twelve-Factor App describes portable SaaS services through separated configuration, backing services as resources, stateless processes, disposability, and logs as streams ([The Twelve-Factor App](https://www.12factor.net/)). OWASP ASVS provides a basis for verifying technical controls and secure development requirements ([OWASP ASVS](https://owasp.org/www-project-application-security-verification-standard/)). These references guide investigation; they do not require every factor or requirement for every change.

## Recognize and reuse existing work

For a new project, identify service entry points, inbound/outbound contracts, storage, migrations, external dependencies, secret management, logs/metrics/traces, and deployment procedures. For an existing project, read the approved specification and previous criteria, trace a request end to end, inspect timeouts/retries, replay migrations in a safe environment, and verify available alerts. Classify findings as **established** (demonstrated by code, test, metric, or command), **inferred** (an indication), **incomplete** (partial coverage), **missing** (a documented search found no artifact), or **not-applicable** (service, risk, or state outside the change). A log does not prove request correlation; a responding endpoint proves neither idempotency nor recovery.

OpenTelemetry offers a vendor-neutral framework for traces, metrics, and logs, including a Collector and specifications ([OpenTelemetry documentation](https://opentelemetry.io/docs/)). Google SRE recommends a small set of user-centered indicators such as latency, errors, throughput, availability, and correctness, distinguishing SLI, SLO, and SLA ([Service Level Objectives](https://sre.google/sre-book/service-level-objectives/)). In this contract, absent instrumentation is `missing` or `incomplete`, never presumed successful.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP covers the happy path, validation and authorization, a representative error, an integration test, migration where applicable, correlatable logs, and a recovery procedure. Go deeper for sensitive data, money, long-running tasks, multiple consumers, high load, availability commitments, or irreversible migration: examine relevant ASVS threats, latency/error budgets, saturation, failure recovery, version compatibility, and distributed traces. Twelve-Factor is a question framework, not sufficient justification for a rewrite.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`api-integrations` formalizes HTTP contracts and retries; `database-engineering` covers isolation, query plans, and migrations; `data-engineering` covers pipelines and quality; `frontend-engineering` verifies visible states. Do not confuse process availability with user success, hide timeouts behind unbounded retries, store secrets in versioned configuration, or add alerts without associated actions. An internal rename that does not change an execution path does not trigger this extension. ASVS and OpenTelemetry are reference frameworks; these local criteria must remain verifiable.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
