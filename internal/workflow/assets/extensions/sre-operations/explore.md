# sre-operations — explore

Connect user-perceived reliability, indicators, objectives, alerts, and responses to degradation.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: define and test an actionable alert for a service with a rising error rate. Out-of-scope example: edit a document without affecting a service, signal, or operational procedure.

This extension connects a change with an operated service's actual behavior: service-level objectives, measurement, alerts, capacity, incidents, on-call work, recovery, and toil reduction. Select it during `stip-explore` when affecting availability, latency, throughput, durability, dependencies, observability, capacity, incident procedures, or operational commitments. Not every change becomes an SRE project; core local tests and a maintenance note may suffice when no operated service is affected.

Google's SRE book distinguishes SLI (measurement), SLO (target), and SLA (agreement with consequences), starting from what matters to users ([Service Level Objectives](https://sre.google/sre-book/service-level-objectives/)). It explains monitoring and alerting signals ([Monitoring distributed systems](https://sre.google/sre-book/monitoring-distributed-systems/)). A Google error-budget policy illustrates an organizational choice to freeze changes after exceeding the budget, with exceptions ([Error budget policy](https://sre.google/workbook/error-budget-policy/)). These practices are not universal obligations, and their freeze policy is not a native core v1 feature.

## Recognize and reuse existing work

For a new project, identify users and critical journeys, candidate SLIs, internal SLOs, dependencies, instrumentation, dashboards, actionable alerts, runbooks, capacity, backups, restoration, and incident ownership. For an existing service, examine availability/latency history, errors, saturation, incidents and postmortems, noisy alerts, on-call arrangements, toil, recent changes, failure tests, and recovery procedures.

Use **established**, **inferred**, **incomplete**, **missing**, and **not-applicable**. A dashboard does not prove an SLI measures user experience; a declared SLO does not establish its window or calculation quality; an alert needs someone able to act. A useful SLO does not require a commercial SLA, and an SLA's existence does not establish attainment.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP selects one or two critical journeys, an understandable measurement, a realistic target, an alert signal, and a documented action. For a noncritical internal tool, an availability check and short runbook may suffice. Go deeper according to criticality and frequency: SLOs by user class, error budgets, multiple-signal alerts, capacity/autoscaling, failure tests, restoration, postmortems, automated toil reduction, and on-call exercises. Agree thresholds and windows with service owners rather than copying a Google example.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`cloud-engineering` provides architecture, capacity, and recovery; `devops-delivery` and `release-management` connect changes and artifacts; `quality-engineering` tests behavior; security and privacy address their risks; `customer-support` reports user impact. Avoid implicit 100% availability targets, paging on every metric, averages hiding tail latency, and SLOs with no operational consequences. Do not automatically freeze releases or move HEAD: these require a separate policy and core change. Offline scripts without a service or dependent user do not trigger this extension.

Google SRE pages document team practices and examples, not a binding standard. The process, MVP thresholds, and criteria here are our integration synthesis.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
