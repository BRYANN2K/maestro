# quality-engineering — explore

Choose checks that reduce actual risks and link their results to acceptance criteria.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the whole catalog. Relevant example: define a test strategy for a migration with multiple failure modes. Out-of-scope example: add an implementation-mirroring check for an already inspected trivial text edit without a distinct risk.

This extension turns quality expectations into observable properties, testable risks, and verification evidence. It covers product/service quality, functional and nonfunctional tests, static review, test data/environments, defects, and regression. Ordinary change testing remains in the core. Select this extension during `stip-explore` for cross-cutting quality strategy, nonfunctional properties, risk management, compatibility criteria, or verification campaigns beyond routine core checks. Documentation-only changes without behavioral impact do not require it unless documentation itself is a contractual deliverable.

ISO/IEC 25010:2023 provides nine product-quality characteristics for specification, measurement, and evaluation ([ISO/IEC 25010:2023](https://www.iso.org/standard/78176.html)). Only the public notice/summary was consulted, not the paid full text. ISO/IEC/IEEE 29119-2 describes generic testing processes for different lifecycles ([ISO/IEC/IEEE 29119-2:2021](https://www.iso.org/standard/79428.html)); again, only its public summary was consulted. These are reference standards, not universal obligations or project certification. This extension does not replace security, accessibility, UX, or support; it verifies qualities actually selected in the change contract.

## Recognize and reuse existing work

For a new project, identify expected outcomes, planned journeys/interfaces, API contracts, data constraints, available environments, and test tools. For an existing project, inspect tests, CI suites, defect reports, production metrics, acceptance tests, compatibility contracts, and regression procedures.

Classify findings as **established** (observed evidence linked to a version), **inferred** (plausible indication without direct verification), **incomplete** (partial evidence), **missing** (no evidence after a defined search), or **not-applicable** (out-of-scope risk or quality, justified). A `tests/` directory proves neither useful coverage nor successful execution. Compare existing systems with a pre-change reference; establish a minimal first baseline for new projects instead of inferring absence of defects.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP selects principal risks, defines a small set of acceptance criteria, runs suitable tests in an identifiable environment, and records results. Review and targeted scenarios may suffice for a low-risk local edit. Deepen testing according to user impact, criticality, data, and integrations: contract/compatibility tests, baseline load tests, resilience, mutation or fuzzing, accessibility with the relevant extension, and defect-trend analysis. ISO 29119 describes processes without requiring every artifact for every project.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

Security, privacy, cloud, and SRE contribute risks and signals; `quality-engineering` connects them to tests without choosing their domain thresholds. `release-management` may require artifact verification; `devops-delivery` may provide a pipeline, but core v1 assumes neither CI nor trunk-based development. Avoid coverage as an isolated goal, production-data-dependent tests, incomparable benchmarks, and green tests that do not exercise requested behavior. Changing an unexposed internal label without a quality contract does not require this extension; a core documentation check suffices.

The cited standards provide models and processes, not quality or compliance guarantees. Strategy, thresholds, steps, and statuses are our integration synthesis for the Stip contract.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
