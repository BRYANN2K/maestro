# security-engineering — explore

Reduce abuse risks affecting assets and trust boundaries touched by the change.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: add private-data access with a new authorization boundary. Out-of-scope example: adjust typographic spacing without affecting assets, permissions, or trust flows.

This extension identifies a change's security threats and requirements and connects controls to verifiable evidence. It covers threat models, trust boundaries, identity and authorization, secrets, dependencies, network exposure, useful logging, vulnerability management, and delivery-chain integrity. Select it during `stip-explore` when introducing or changing untrusted input, identity, permissions, secrets, sensitive data, dependencies, exposed services, or delivery mechanisms. A local edit without these impacts may remain unselected.

NIST SSDF 1.1 offers secure development practices that can be integrated into an SDLC ([NIST SP 800-218](https://csrc.nist.gov/pubs/sp/800/218/final)). NIST CSF 2.0 provides an outcome taxonomy without prescribing implementation ([NIST CSF 2.0](https://www.nist.gov/publications/nist-cybersecurity-framework-csf-20)). OWASP ASVS 5.0 provides web-application design, development, and verification requirements ([OWASP ASVS](https://github.com/OWASP/ASVS)). Additional obligations arise from contractual, regulatory, sector, or accepted-risk context. This extension promises neither legal compliance, certification, nor absence of vulnerabilities.

## Recognize and reuse existing work

For a new project, identify actors/assets, data flows, trust boundaries, authentication/authorization requirements, cryptographic choices, dependencies, network exposure, secret strategy, logging, and incident response. For an existing project, inspect threat models, IAM controls, SAST/SCA/DAST scans, SBOMs, dependency advisories, branch rules, penetration-test reports, disclosure policies, deployment configurations, and known incidents.

Classify results as **established** (observed evidence linked to a version), **inferred** (plausible indication without direct verification), **incomplete** (partial evidence), **missing** (no evidence after a defined search), or **not-applicable** (out-of-scope surface, justified). A scanner report not run against the current commit is not current evidence; an OpenSSF Scorecard score is a signal to investigate, not a security certificate. Report limited access to systems, logs, or secrets separately.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP covers trust boundaries, critical inputs and authorization, secret protection, direct dependencies, and repeatable negative tests. A targeted scan may suffice for a small change when scope and results are verified. Go deeper according to risk, exposure, and criticality: detailed threat modeling, versioned ASVS, SAST/SCA/DAST, SBOM/provenance, pipeline hardening, fuzzing, independent review, response exercises, and post-delivery monitoring. SSDF and CSF do not require every practice to be installed in core v1.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`privacy-engineering` addresses risks to people; `cloud-engineering` covers platform controls; `quality-engineering` supplies tests; `devops-delivery` and `release-management` may provide provenance and gates; SRE covers production detection and response. Do not mistake scores, checklists, or reports for threat analysis. Avoid real data in tests without justification and protection, secret logging, or scanning branches that differ from the verified artifact. Purely local, unexposed changes without new assets, permissions, or data need not activate this extension; document the reasoned non-selection.

The selected controls, thresholds, classifications, and loops are our integration synthesis. NIST and OWASP pages do not guarantee compliance; legal requirements must be assessed in the relevant jurisdiction.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
