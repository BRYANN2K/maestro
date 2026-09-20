# mobile-engineering — explore

Build and verify a mobile experience across its lifecycle, network, permissions, and distribution.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: add mobile synchronization after suspension and network recovery. Out-of-scope example: fix a web page without affecting native mobile behavior or a mobile package.

This extension turns a mobile journey into reliable behavior on actual devices: lifecycle, restorable state, intermittent networking, permissions, local storage, notifications, gestures, accessibility, security, and distribution. Select it during `stip-explore` when affecting iOS or Android apps, native components, cross-platform apps, permissions, or lifecycle-sensitive features, including existing APIs that must work offline or in interrupted screens.

It does not apply to a web service not packaged for mobile or a backend rule without device behavior. It does not choose iOS versus Android, frameworks, or distribution strategy for the project. Android architecture guidance recommends clear responsibilities, persistent models, and a single source of truth, accounting for processes killed under resource constraints ([Guide to app architecture](https://developer.android.com/topic/architecture)). Android quality guidance covers post-interruption state, accessibility, crashes/ANRs, privacy, networking, and representative form-factor tests ([Core app quality guidelines](https://developer.android.com/docs/quality-guidelines/core-app-quality)).

## Recognize and reuse existing work

For a new project, identify target platforms/versions, state modules, persistence, permissions, backend, build/signing commands, device tests, and distribution rules. For an existing project, install the actual build, interrupt/resume journeys, disconnect networking, deny a permission, change size/rotation, and verify behavior after sleep or termination. Classify findings as **established** (reproducible test, build, or trace), **inferred** (behavioral indication), **incomplete** (missing platform or lifecycle), **missing** (search found no trace), or **not-applicable** (capability absent from the product). Emulator success does not establish restoration after termination; a declared permission does not establish understandable consent.

OWASP MASVS groups mobile controls around storage, cryptography, authentication, networking, platform, code, resilience, and privacy ([MASVS](https://mas.owasp.org/MASVS/)). These groups structure verification, with testing profiles elsewhere; they are not a complete audit by default. Apple's Human Interface Guidelines page was consulted, but detailed content was JavaScript-gated in the research environment ([Human Interface Guidelines](https://developer.apple.com/design/human-interface-guidelines?lang=en)). Preserve that limitation rather than inventing Apple rules.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP covers an explicitly targeted platform, the happy path, minimal restoration, network error, necessary permission, a primary UI test, and an installable build. Go deeper for health/payments, private data, critical notifications, multiple versions, or offline mode: test iOS/Android matrices, low memory, interrupted networking, accessibility, encryption/storage, migration, and progressive rollout. A resized browser alone does not establish mobile compatibility.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`backend-engineering` and `api-integrations` define synchronization, authentication, and recovery; `database-engineering` handles storage; `frontend-engineering` may share state invariants; `desktop-engineering` covers native desktop installation. Avoid plaintext tokens, requesting all permissions at first launch, assuming memory survives process termination, or claiming compatibility from one device. MASVS structures verification; Android recommendations do not replace iOS testing. Server-only changes without client impact do not trigger this extension.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
