# privacy-engineering — explore

Bound personal data processing through purpose, minimization, access, retention, and rights.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the whole catalog. Relevant example: add event collection containing personal identifiers. Out-of-scope example: optimize computation on synthetic data without a new personal-data flow.

This extension addresses risks to people when a change collects, infers, accesses, shares, retains, or deletes information about individuals. It helps define purpose, minimization, visibility, access controls, retention, rights, and decision evidence. Select it during `stip-explore` for personal data, identifiers, cookies, person-linked telemetry, profiles, location, support data, or combinations that may identify a person. It can assess data absence and remain unselected when no person-related data or privacy policy is affected.

Published NIST Privacy Framework 1.0 is voluntary, flexible, and law/jurisdiction-agnostic ([NIST Privacy Framework 1.0](https://csrc.nist.gov/pubs/cswp/10/nist-privacy-framework-version-10/final)). At review, NIST's page described version 1.1 as an initial public draft, not a final standard ([Privacy Framework — status page](https://www.nist.gov/privacy-framework)). ISO 31700-1:2023 sets high-level consumer-product lifecycle privacy-by-design requirements without prescribing methods or technology ([ISO 31700-1](https://www.iso.org/standard/84977.html)); only its public summary was consulted, not the paid full text. Where GDPR applies, Article 25 and EDPB guidance make data protection by design and default obligations conditional on that processing scope ([European Commission — obligations](https://commission.europa.eu/law/law-topic/data-protection/information-business-and-organisations/obligations_en), [EDPB Guidelines 4/2019](https://www.edpb.europa.eu/sites/default/files/files/file1/edpb_guidelines_201904_dataprotection_by_design_and_by_default_v2.0_en.pdf)). This reference is not legal advice and guarantees no compliance.

## Recognize and reuse existing work

For a new project, identify categories of people and data, purposes, the organization's chosen basis or justification, recipients, transfers, retention, defaults, rights, and deletion/export mechanisms. For an existing project, inspect processing records, notices, consent, DPIAs, processor contracts, data schemas, access logs, rights requests, retention jobs, incidents, and telemetry settings.

Use **established** (observed flow, rule, or evidence linked to a version), **inferred** (plausible indication), **incomplete** (partial evidence), **missing** (search found no evidence), or **not-applicable** (no person-related data in scope, with justification). Distinguish unknown data from absent data: an empty table does not prove the schema or provider processes none. DPIA needs depend on risk and applicable law, not merely a file named `DPIA`.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP identifies data categories, purposes, access, retention, and defaults, then verifies critical scenarios with synthetic data. A targeted inventory and access test may suffice for a low-risk change involving nonsensitive data. Go deeper when risk, volume, sensitivity, surveillance, profiling, children, transfers, or providers demand it: DPIAs, affected-person analysis, pseudonymization, environment separation, re-identification tests, automated deletion, and rights-request evidence. NIST PF and ISO 31700 provide structure, not a universal compliance dossier.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`security-engineering` protects confidentiality, integrity, and access; `cloud-engineering` covers location and managed services; `customer-support` may supply incident or request data; SEO must avoid indexing personal data. Do not equate encryption with minimization, consent with every legal basis, or claimed anonymization with demonstrated low re-identification likelihood. Do not copy real data into tests or enable default telemetry without checking purpose and information. Offline algorithms on demonstrably aggregated data without individual links do not trigger this extension; record the non-applicability analysis.

Cited obligations depend on scope and jurisdiction. NIST 1.0 is the published version consulted; version 1.1 was a public draft at review. The ISO page is a public summary, not the full standard. Steps, statuses, and criteria are our integration synthesis rather than a complete prescription from the sources.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
