# design-system — explore

Evolve shared primitives and components with their usage guidance, contracts, and migrations.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: unify a form component reused across journeys while preserving consumers. Out-of-scope example: produce a one-off sketch without demonstrated reuse needs.

This extension determines when visual or interaction rules should become reusable, governed, and testable across surfaces. It covers inventory, tokens, components, patterns, documentation, versioning, contribution, and extension strategies. Select it during `stip-explore` when reusing or modifying an existing library, creating a primitive for multiple teams, or uncovering divergence likely to multiply debt.

It does not automatically apply to isolated pages, disposable exploratory mockups, or brand preferences without reuse needs. It does not replace UX, `visual-design`, `content-design`, or accessibility: a component must solve a need, remain understandable, and pass relevant domain checks. GOV.UK requires proposals to be useful and unique, then implementations to be usable, consistent, and versatile ([Contribution criteria](https://design-system.service.gov.uk/community/contribution-criteria/)). Adapt those system-specific criteria explicitly to the project.

## Recognize and reuse existing work

For a new project, identify installed design systems, tokens, components, patterns, naming conventions, runtime versions, research evidence, accessibility coverage, licenses, and owners. For an existing project, inventory actual usages, forks, overrides, duplicate components, code/design divergence, incidents, and update costs. GOV.UK recommends starting with existing work and examples before proposing contributions ([Develop a component or pattern](https://design-system.service.gov.uk/community/develop-a-component-or-pattern/)).

Classify rules as **established** (verifiable component/version/usage and owner), **inferred** (repeated convention without explicit governance), **incomplete** (missing design, code, content, or tests), **missing** (documented search found no artifact), or **not-applicable**. A CSS class or component may be unsupported; an open discussion does not prove testing. New projects may reuse third-party systems; existing projects should assess change cost and version trajectory before forking.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes inventory, a reuse decision, a documented local component/pattern, and tests on the affected surface. Do not build full governance or catalogs for single uses. Go deeper for shared, sensitive, frequent, multi-team-versioned, or costly-to-fix components: semantic tokens, design/code/content documentation, automated/manual tests, compatibility, contribution processes, changelogs, and deprecation become relevant. USWDS describes tokens as a discrete vocabulary reducing arbitrary choices and improving design/development communication ([Design tokens](https://designsystem.digital.gov/design-tokens/)).

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

UX and `visual-design` supply needs and form; accessibility supplies required tests; `content-design` checks words/examples; `user-research` establishes usefulness; `build-in-public` can document contributions only when publication is decided. Avoid catalogs without users, confusing consistency with uniformity, forks without update plans, replacing components for local preferences, or presenting community resources as official. A single page without planned reuse does not trigger this extension, even with multiple styles. GOV.UK and USWDS criteria describe their own systems; activation, statuses, and criteria here are a contract synthesis.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
