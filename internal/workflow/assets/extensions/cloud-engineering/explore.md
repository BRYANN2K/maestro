# cloud-engineering — explore

Design reproducible cloud resources with access controls, recovery, health, and cost considerations.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: migrate a managed service and define its restoration, access, and budget. Out-of-scope example: modify a local application without affecting cloud services or topology.

This extension examines cloud and cloud-native platform choices: deployment architecture, accounts and regions, networking, identity, storage, resilience, recovery, performance, cost, sustainability, and operations. Select it during `stip-explore` when creating or changing a cloud resource, infrastructure as code, managed service, runtime image/platform, topology, or availability constraint. Cloud-only work does not trigger UX/design by default; this extension does not design interfaces or user journeys.

AWS ([Well-Architected Framework](https://docs.aws.amazon.com/wellarchitected/latest/framework/definitions.html)), Azure ([Well-Architected Framework](https://learn.microsoft.com/en-us/azure/well-architected/)), and Google Cloud ([Well-Architected Framework](https://docs.cloud.google.com/architecture/framework?authuser=0&hl=en)) provide pillars and assessment questions for their contexts. CNCF describes cloud-native practices as programmable, repeatable, secure, resilient, manageable, sustainable, and observable ([CNCF Cloud Native Definition](https://github.com/cncf/toc/blob/main/DEFINITION.md)). These frameworks offer guidance; they do not certify an architecture or establish portability between providers.

## Recognize and reuse existing work

For a new project, identify the provider, account/project organization, regions and zones, network architecture, IAM, secrets, managed services, IaC, data strategy, backups, RTO/RPO, quotas, budget, and observability. For an existing project, inspect IaC modules and state, resource inventory, configuration drift, actual diagrams, cost rules, incidents, recovery tests, metrics, provider limits, and external dependencies.

Classify evidence with the core taxonomy: **established** (actual state and version observed), **inferred** (an indication without direct observation), **incomplete** (partial coverage or environment), **missing** (search found no evidence), or **not-applicable** (outside the selected risk, with justification). A deployed stack is not proof of resilience; a Terraform file proves neither application nor absence of drift.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes an up-to-date diagram, an owner, reproducible configuration, least-privilege access, proportionate backup or recovery, a health signal, and a cost estimate. A single region and manual recovery may be acceptable for a short-lived prototype when explicit. Go deeper when criticality, volume, data, multiple regions, provider dependency, or operational requirements demand it: pillar review, failure tests, replication, automated quotas, encryption and keys, policy as code, continuous optimization, and a provider exit plan. No provider framework automatically justifies every best practice.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`security-engineering` covers threats and secrets; `privacy-engineering` covers location and personal data; SRE covers SLOs and incidents; `devops-delivery` covers delivery; `release-management` covers artifacts; and `quality-engineering` covers tests. Do not confuse cloud-native with mandatory microservices, provider availability with service availability, or IaC with absence of drift. Do not copy production identifiers into examples. Do not activate this extension for documentation-only work without architectural change or a local application using no cloud service; record inapplicable dependencies where relevant.

The cited frameworks may be provider-specific and evolve over time. The process, MVP depth, and criteria are our integration synthesis; they are neither Well-Architected certification nor an availability commitment.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
