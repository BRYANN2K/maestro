# build-in-public — explore

Prepare useful public updates about project progress, evidence, lessons, and limitations.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: prepare a post showing an actually built feature and development lessons. Out-of-scope example: build a private feature without a request for public communication.

This extension organizes voluntary external visibility during building: goals, lessons, decisions, authorized prototypes, roadmaps, changelogs, retrospectives, or feedback requests. It turns internal artifacts into traceable public communication with status, limits, ownership, and correction paths. It does not imply automatic publication, deployment, or mandatory marketing. The core retains publication authorization and the team decides what can be shared.

Select it during `stip-explore` when explicitly targeting external audiences, public feedback, accessible roadmaps, communities, open-source projects, progress notes, or transparent decisions. GOV.UK suggests public discovery lessons through posts or open show-and-tells where confidentiality permits ([How the discovery phase works](https://www.gov.uk/service-manual/agile-delivery/how-the-discovery-phase-works)). That contextual suggestion does not replace confidentiality review. Internal changelogs, private documentation, deployment without communication, or team discussion do not trigger it. Publication/audiences added after approval require explore, validate, and revised approval; no silent activation.

GitHub illustrates a public roadmap organized by phase, area, product, deployment model, and shipped status, explicitly stating that forecasts are neither commitments nor date guarantees ([GitHub public roadmap](https://github.com/github/roadmap)). Transparency should add information without turning intentions into commercial promises. This domain also covers public corrections: what was said, when, by whom, with which limits, and how errors will be corrected.

## Recognize and reuse existing work

For a new project, identify audiences and visibility needs, channels, feedback goals, authorized decisions, shareable data, contractual/legal/security/privacy constraints, and capacity to maintain updates. For an existing project, inventory roadmaps, issues, changelogs, posts, metrics, screenshots, open repositories, comments, date commitments, and pending responses. Verify version identification and consistency between public artifacts and actually accessible products.

Classify elements as **established** (verifiable fact, artifact, or public commitment), **inferred** (interpretation from signals), **incomplete** (publication without status, scope, evidence, or owner), **missing** (transparency intent without useful artifacts/channels), or **not-applicable** (no external audience or authorized content). Public pages do not establish safe data handling or fulfilled commitments. Community requests do not automatically become product priorities.

Buffer describes chosen transparency around selected metrics to support trust and accountability ([Open](https://buffer.com/open)). Its engineering account also explains security and privacy boundaries on opening code/information ([Transparency in Engineering](https://buffer.com/resources/open-source-code/)). These are Buffer experiences, not obligations to disclose salaries, revenue, code, or metrics elsewhere. Its build-in-public guide treats transparency as a continuum requiring explicit boundaries on what will not be shared ([How Transparency on the Internet Can Support Your Goals](https://buffer.com/resources/internet-transparency/)).

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP is a bounded public artifact with objective, audience, status, version, scope, owner, evidence, limits, date, and feedback path. Short roadmaps can label exploratory/directional items; changelogs can cover only already available capabilities. Go deeper for sensitive/regulated products, large open-source contributor bases, security exposure, customer commitments, or active communities: threat review, data minimization, consent, moderation, response SLAs, archiving, public corrections, and quality metrics become relevant. No MVP permits disclosure of secrets, personal data, unpatched vulnerabilities, or commercial commitments without explicit authorization and appropriate handling.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

Storytelling explains learning; content design writes; product strategy sets goals/priorities; security/privacy and human legal advice where necessary bound sharing; user research checks representativeness of feedback; release management confirms delivery status. Avoid roadmap promises, unauthorized personal information in screenshots, comment counts as evidence, replying to everything publicly, omitted corrections, or vulnerability disclosure for appearances. No public-communication intent means no activation, even if a private repository contains material for a future authorized update. GitHub, GOV.UK, and Buffer describe contextual practices; proportionate authorization review and `AC-BIP-*` candidates are our Stip integration.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
