# product-strategy — explore

Connect the problem, audience, value, alternatives, and tradeoffs to a product decision.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: choose the first MVP's problem and define its value proposition. Out-of-scope example: rename a private variable without changing behavior or a product hypothesis.

This extension helps decide whether a change is worth pursuing, for what outcome, and under which value hypotheses. It turns solution requests such as adding a dashboard into problems, expected outcomes, options, and traceable decisions. Select it during `stip-explore` when committing product/service direction, priorities, value models, scope, or success measures. It also helps with apparently local changes to existing products that affect promises, served users, or costs.

It does not apply to already specified mechanical corrections, technical migrations without user-outcome choices, or documentation with established content/audience. It connects user research, architecture, and budget governance to product decisions without replacing them. 18F distinguishes discovery, vision, strategy, roadmaps, and delivery, grounded in outcomes and evidence ([18F Product Guide](https://guides.18f.org/product/)). This describes 18F practice, not a universal standard.

## Recognize and reuse existing work

For a new project, identify initial intent, intended users, observed problems, existing alternatives, known constraints, and proposed success criteria. For an existing project, inspect previous specifications/criteria, roadmaps, abandoned decisions, usage data, operating costs, and test results. 18F recommends a shared understanding of current state with a multidisciplinary team ([Discover the current state](https://guides.18f.org/product/discover/)).

Classify findings as **established** (verifiable source, date, owner, and scope), **inferred** (unconfirmed deductions), **incomplete** (insufficient coverage, date, or scope), **missing** (documented search found no relevant trace), or **not-applicable** (risk or audience absent for this change). Undocumented claims are at best inferred. An empty folder alone does not establish missing practice. Record supporting paths, links, queries, or interviews. For evolution, compare the baseline with delivered behavior; old visions or roadmaps do not establish current value.

A sound problem statement stays independent of a solution. 18F recommends recording affected people, impact, and pain points, then revising the problem as research progresses ([Define the problem](https://guides.18f.org/product/define/problem/)). Strategyzer's value proposition model structures jobs, pains, and gains, while noting that a canvas alone is not a strategy and must evolve with customer evidence ([Value Proposition Canvas](https://www.strategyzer.com/library/the-value-proposition-canvas)).

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP needs a sourced problem, population, observable outcome, two or three priority hypotheses, selected option, and follow-up indicator. Go deeper for multiple segments, high costs, regulatory/technical dependencies, commercial promises, or substantial irreversibility: compare options, establish quantitative baselines, run a value experiment, and document decisions not to invest. Do not manufacture a full business case for a local correction without outcome impact.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

User research supplies needs evidence; UX and content test options; design systems and accessibility can turn implementation risks into strategic constraints; storytelling explains decisions without replacing evidence. Avoid roadmaps as delivery commitments: express outcomes and keep them revisable. Internal color changes, documentation typos, and technical renames without user impact do not trigger this extension. Sources are contextual guides; statuses, criteria, and explore-to-check integration are our synthesis, not organizational prescriptions.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
