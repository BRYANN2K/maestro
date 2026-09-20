# content-design — explore

Help people understand and complete their task through clear content in the right context.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: rewrite form errors and instructions so recovery is understandable. Out-of-scope example: change internal configuration without affecting reader-facing text.

This extension designs the information and words people need to understand, decide, and act: structure, hierarchy, labels, instructions, errors, help, onboarding, documentation, and editorial rules. Content is part of the service, with ownership, usage context, and comprehension evidence. 18F describes good content as understandable and helping users accomplish their goals ([About this guide](https://guides.18f.org/content-guide/)). This goes beyond post-design proofreading.

Select it during `stip-explore` when creating or changing messages, fields, actions, errors, confirmations, onboarding, help, documentation, multilingual content, or accessibility information. It also applies when text contradicts behavior or obstructs a task. It does not replace storytelling's intent/promise/narrative, UX journeys/states, user research, or accessibility verification. Backend-only changes without textual outputs do not trigger it, nor does marketing without audience, evidence, or authorization. Content surfaces discovered after approval require returning to explore/validate and approving the revised contract; extensions never reactivate silently.

## Recognize and reuse existing work

For a new project, identify tasks, audiences, triggers, user vocabulary, language constraints, channels, sources of truth, and owners. For an existing project, inventory UI text, errors, notifications, help, documentation, research, analytics, search logs, support tickets, glossaries, and translations. Compare text with actual behavior: existing labels do not establish understanding; help articles do not establish accuracy for the deployed version.

Classify elements as **established** (verifiable terms, needs, rules, or text), **inferred** (interpretation from signals), **incomplete** (missing context, action, state, or ownership), **missing** (identified need without useful content/evidence), or **not-applicable** (no affected content). Retain source, date, product, and translation status. GOV.UK recommends starting from needs and continuing research across phases, treating internal opinions as hypotheses to prove ([Learning about users and their needs](https://www.gov.uk/service-manual/user-research/start-by-learning-user-needs)).

18F recommends short instructions, action verbs, labels matching the interface, and concrete examples ([Technical and interface writing](https://guides.18f.org/content-guide/our-style/technical-and-interface-writing/)). This is team practice rather than language rules for every audience: retain research-supported vocabulary and domain obligations.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP covers the main task: explicit goal, understandable title/label, action, success state, recoverable error, minimal help, and ownership. Brief comprehension testing or observation may suffice at low risk. Go deeper for regulated, multilingual, public, transactional, frequent, costly-error, multichannel, or durable documentation contexts: content models, taxonomy, log research, assistive-technology users, localization, governance, legal review, and search/support metrics may help. Publication remains a project/core decision; this extension prepares and verifies content.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

Storytelling supplies intent/promise/hierarchy; product strategy supplies outcomes/priorities; UX supplies context/states/journeys; accessibility verifies names, alternatives, readability, and assistive technologies; visual design supplies perceptible hierarchy; user research verifies vocabulary/comprehension; build-in-public publishes only authorized material. Avoid grammatical fixes to incomprehensible journeys, stacked synonyms, buried errors, organization-centered rather than task-centered writing, ownerless duplication, or brand voice in critical messages. Jobs without observable content do not trigger this extension. 18F, GOV.UK, and NN/g are contextual practices; our loop, taxonomy, and `AC-CD-*` candidates are adapted to Stip.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
