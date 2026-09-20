# frontend-engineering — explore

Build rendered web behavior, including states, data, accessibility, and performance.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: implement web search with loading, errors, keyboard navigation, and asynchronous results. Out-of-scope example: change only infrastructure without affecting frontend behavior.

This extension turns interface intent into deliverable, testable, maintainable browser behavior: screen structure, states, navigation, data, errors, performance, and compatibility. Select it during `stip-explore` when changing web surfaces, interactions, client rendering, navigation journeys, or frontend build dependencies. Backend contract changes requiring screen adaptations can also trigger it, coordinated with `backend-engineering` or `api-integrations`.

It does not apply to server-only fixes, visual decisions without implementation, or documents promising no interface behavior. It prescribes neither a framework, component library, nor design maturity level. WCAG 2.2 defines testable success criteria, four accessibility principles, and three conformance levels, combining automated evaluation with human judgment ([WCAG 2.2](https://www.w3.org/TR/wcag/)). The thresholds and sequence here are a project-adapted integration contract, not automatic WCAG conformance.

## Recognize and reuse existing work

For a new project, identify entry points, intended routes, styling systems, reusable components, simulated/real data, target browsers, build commands, and available checks. For an existing project, read the specification and earlier criteria, inspect mounted code, verify loading/error/empty states, establish a performance baseline, and reproduce main journeys. Classify findings as **established** (verifiable artifact or command), **inferred** (indication without direct evidence), **incomplete** (partial trace), **missing** (search found no trace), or **not-applicable** (no affected surface). An existing page does not establish offline, keyboard, or error coverage; absent tests do not establish absent behavior.

The ARIA Authoring Practices Guide describes widget patterns, roles, states, properties, accessible names, and keyboard interactions ([WAI-ARIA APG](https://www.w3.org/WAI/ARIA/apg/)). Use it to verify interactive components rather than adding roles blindly. Google's real-experience performance indicators include LCP, INP, and CLS, with reference thresholds of 2.5 seconds, 200 milliseconds, and 0.1 ([Core Web Vitals](https://developers.google.com/search/docs/appearance/core-web-vitals)). MDN describes progressive enhancement as usable baseline content/functionality followed by capability-detected improvements ([Progressive enhancement](https://developer.mozilla.org/en-US/docs/Glossary/Progressive_Enhancement)). Distinguish available measurements, missing instrumentation, and out-of-scope requirements.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes the affected route/surface, success/loading/error/empty states, primary keyboard navigation, a behavior test, and a build check. Go deeper for critical, public, multilingual, dynamic, mobile-used, accessibility-constrained, or performance-sensitive screens: measure Core Web Vitals on a representative baseline, test announcements and focus with tools and people, cover supported browsers, and verify fallback paths. Do not turn an internal label or color change into an audit campaign without corresponding risk.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`api-integrations` supplies data/error contracts; `backend-engineering` defines availability, pagination, and limits; mobile/desktop can reuse invariants with different checks. Avoid treating screenshots as specifications, spinners without error strategies, ARIA without keyboard models, or Lighthouse scores as actual experience. Core Web Vitals does not guarantee every device; APG supplies patterns rather than ready-made components. Text-only changes do not trigger this extension unless they affect accessible names or content contracts.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
