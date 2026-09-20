# ux-design — explore

Design journeys, interactions, states, and recovery for a user task.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: add a cancellation journey with confirmation, in-progress state, and error recovery. Out-of-scope example: migrate a cloud resource without changing user interactions or tasks.

This extension designs and verifies the experience needed for someone to reach their goal in the actual service context. It covers journeys, interactions, states, errors, navigation, in-flow content, and prototypes; it includes interfaces without being limited to appearance. Select it during `stip-explore` when changing tasks, journeys, interactions, user decisions, channels, or error handling. It can start early: Design Council's Double Diamond is iterative, with overlapping discovery, definition, development, and delivery and tests returning to earlier stages ([Framework for Innovation](https://www.designcouncil.org.uk/resources/framework-for-innovation/)).

It does not replace `user-research` for questions/participants, `visual-design` for appearance, `content-design` for language, or accessibility for inclusive requirements/tests. Server-only changes without task or observable-behavior impact do not trigger it. Cloud-only changes do not trigger UX/design. The sequence does not impose a complete design system or linear chain: sketching, research, and prototyping can proceed in parallel according to risk.

## Recognize and reuse existing work

For a new project, identify current/alternative journeys, goals, exit points, off-screen channels, visible business rules, existing components, prototypes, usability issues, and technology constraints. For an existing project, compare the used version with prototypes/specifications, inspect analytics/errors, verify loading, empty, success, failure, and recovery states, and review recent research.

Classify elements as **established** (verifiable behavior/source), **inferred** (hypothesis from an indication), **incomplete** (partially described flow/state), **missing** (documented search found no usable evidence), or **not-applicable**. A screenshot establishes appearance, not usable journeys. New products may lack flows; mark them missing after investigation rather than designing arbitrarily. Old mockups do not establish current product behavior.

GOV.UK recommends prototyping before committing to code and choosing fidelity for the question ([Making prototypes](https://www.gov.uk/service-manual/design/making-prototypes)). Alpha guidance focuses prototypes on riskiest assumptions, avoids building a public service, and accepts throwing away trial code ([How the alpha phase works](https://www.gov.uk/service-manual/agile-delivery/how-the-alpha-phase-works)). These are contextual prescriptions translated here into proportionate evidence.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes a main scenario, start-to-outcome flow, error/wait/success states, a low- or medium-fidelity prototype, and verification with representative users. Go deeper for multiple channels, permissions, payments, sensitive data, complex navigation, mobile usage, high frequency, or expensive recovery: realistic prototypes, secondary journeys, responsive behavior, microinteractions, assistive-technology tests, and performance criteria may be necessary. Do not create exhaustive wireframes for an endpoint without an interface.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`user-research` supplies participants/questions; `product-strategy` supplies outcomes/priorities; visual design and design systems supply visual language/reuse; content design writes labels/errors; accessibility verifies keyboard, zoom, assistive technology, and inclusion; build-in-public applies only when public communication is decided. Avoid solutions before journey understanding, happy-path-only tests, mockups as feasibility proof, and imposed component libraries on isolated pages. Purely internal changes observable only through API tests do not trigger this extension. Design Council and GOV.UK are practice references, not mandates to copy phases or tools.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
