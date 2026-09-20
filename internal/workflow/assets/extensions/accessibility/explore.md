# accessibility — explore

Make tasks and content perceivable, operable, understandable, and robust for the intended audiences.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: add an interactive dialog usable with keyboards and screen readers. Out-of-scope example: change a server process without affecting person-facing surfaces or outputs.

This extension makes changes usable by people with varied capabilities, devices, environments, and assistive technologies. It turns access needs into content, interaction, code, testing, and follow-up decisions rather than only final audits or compliance checkboxes. WCAG 2 organizes requirements around perceivable, operable, understandable, and robust principles with testable A/AA/AAA success criteria ([WCAG 2 Overview](https://www.w3.org/WAI/standards-guidelines/wcag/)). Applicable levels depend on legal context, audiences, and contracts; AA must not be presented as a general guarantee outside scope.

Select it during `stip-explore` when changing interfaces, content, journeys, keyboard interaction, assistive technology, shared components, documents, contractual obligations, or audiences at risk of exclusion, including fixes for observed defects. Server-only changes without accessible outputs or task effects do not trigger it. Cloud-only work does not automatically activate accessibility/design. Surfaces or access requirements added after approval require explore, validate, and revised approval; no silent activation.

ARIA Authoring Practices Guide provides patterns, examples, and keyboard conventions but is informative rather than normative, and neither a design system nor production-ready code ([APG introduction](https://www.w3.org/WAI/ARIA/apg/about/introduction/)). WCAG and local requirements remain conformance references; APG helps design and test behavior.

## Recognize and reuse existing work

For a new project, identify journeys, audiences, access modes, platform constraints, components, content, documents, and legal/contractual criteria. Identify critical tasks, nondigital alternatives, and plausible assistive technologies; plan research, remediation, and audit capacity. For an existing project, inspect accessibility statements, audits, tickets, automated/manual results, shared components, regressions, disabled-user feedback, and actually supported browser/technology combinations.

Classify findings as **established** (verifiable requirement, behavior, or evidence), **inferred** (risk/hypothesis from indications), **incomplete** (partially covered journey/test), **missing** (documented search found no usable evidence), or **not-applicable** (surface, population, or technology outside justified scope). Missing audits do not establish conformance, automated scores do not cover every barrier, and copied ARIA components prove neither keyboard nor assistive-technology compatibility. GOV.UK recommends considering accessibility before design and treating it as a whole-team responsibility ([Making your service accessible](https://www.gov.uk/service-manual/helping-people-to-use-your-service/making-your-service-accessible-an-introduction)).

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP covers the main task: keyboard-only use, visible focus/coherent order, names/labels, contrast/alternatives, relevant zoom/reflow, errors, and a test with the declared platform or assistive technology. Go deeper for public/regulated services, payments/health/rights, reusable components, multiple channels, or costly regressions: independent audits, journey samples, multiple browsers/screen readers, disabled-user recruitment, conformance reports, and ongoing monitoring. Automation can accelerate triage without replacing manual and human tests.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

UX defines tasks/states; content design makes instructions/labels/errors understandable; visual design/design systems handle contrast, focus, and components; user research recruits and listens to affected people; quality engineering automates checks without mistaking them for complete validation. Avoid unnecessary ARIA on usable native elements, hidden focus, happy-page-only tests, confusing WCAG conformance with experience, or claiming unearned certification. Backend-only jobs without accessible outputs or effects do not trigger it. W3C and local obligations are normative within their scope; APG, GOV.UK, and USWDS are contextual practices. Our matrix and criteria are the proposed Stip synthesis.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
