# visual-design — explore

Define appropriate hierarchy, readability, colors, typography, spacing, and visual states.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the whole catalog. Relevant example: fix unreadable hierarchy and table states on small screens. Out-of-scope example: change a server worker without affecting visual surfaces.

This extension makes experiences readable, hierarchical, and consistent through typography, color, spacing, grids, images, icons, and visual states. It asks what a person needs to see, distinguish, or understand in context. Select it during `stip-explore` when changing hierarchy, readability, contrast, density, responsiveness, identity, or interface states.

It does not replace UX journeys/interactions, content language, design-system governance of reusable primitives/components, or accessibility verification of perception and operation. It does not require a full identity for a local correction or server change without visual impact. Isolated pages can use documented visual decisions without a complete design system.

## Recognize and reuse existing work

For a new project, identify platforms, light/dark themes, fonts, palettes, grids, images, icons, focus/error/success states, brand constraints, and rendering environments. For an existing project, capture shipped screens at supported sizes/densities, inspect actual tokens/variables, identify inconsistencies, and check contrast/readability incidents.

Classify artifacts as **established** (verifiable value, usage, and context), **inferred** (convention assumed from repetition), **incomplete** (partially covered rule/state), **missing** (explicit search found no rule/evidence), or **not-applicable**. Repeated color does not prove meaning: GOV.UK assigns functional colors by context and checks WCAG 2.2 AA contrast ([Colour](https://design-system.service.gov.uk/styles/colour/)). One screenshot does not establish dark mode, zoom, or responsive behavior.

Guidance is contextual. Apple recommends testing readability across sizes and not relying on color alone ([Typography](https://developer.apple.com/design/human-interface-guidelines/typography), [Color](https://developer.apple.com/design/human-interface-guidelines/color)). GOV.UK uses a tested type scale and small-first layout ([Type scale](https://design-system.service.gov.uk/styles/type-scale), [Layout](https://design-system.service.gov.uk/styles/layout)). These are different systems; adopt verifiable principles rather than copying values without a platform decision.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes hierarchy, an existing type pairing/scale, minimal semantic palette, responsive layout, main states, and contrast/zoom verification on the affected surface. Go deeper for rules shared across surfaces/teams, strategic identity, dense or internationalized content, changing themes, or costly regression: semantic tokens, component documentation, automated visual tests, multiple-resolution assets, and governance become relevant. Do not create tokens for one-off values without expected reuse.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

UX defines journeys/states; `content-design` tests text length and meaning; accessibility supplies WCAG, keyboard, and assistive-technology checks; `design-system` determines reuse; storytelling may structure a presentation. Avoid palettes chosen before meaning, semantic colors reused out of context, large-screen-only tests, invisible states, and branding at the expense of task completion. Log values or endpoints without user rendering do not trigger this extension. Apple and GOV.UK guidance stays platform-specific; activation and criteria here are our integration.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
