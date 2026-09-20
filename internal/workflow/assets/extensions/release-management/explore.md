# release-management — explore

Prepare an identifiable release with compatibility, artifacts, release notes, and recovery.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the whole catalog. Relevant example: prepare a distributable release with migration and compatibility notes. Out-of-scope example: explore a prototype without planned versioning or distribution.

This extension makes a release identifiable, understandable, and verifiable for consumers: version, notes, compatibility, deprecation, artifacts, provenance, and withdrawal criteria. Select it during `stip-explore` when changing a public API, package, binary, distributed format, version policy, release notes, or artifact integrity. It does not apply to every core `archive`: archive closes a local change and does not publish a release.

SemVer 2.0.0 relates public API changes to MAJOR/MINOR/PATCH increments and requires published content to remain immutable ([Semantic Versioning](https://semver.org/)). Keep a Changelog recommends a curated, readable, version-organized history ([Keep a Changelog](https://keepachangelog.com/en/1.0.0/)). GitHub describes optional release immutability, locking tags and assets and creating an attestation ([Immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)). These are respectively an adopted specification, an editorial convention, and a platform capability, not universal obligations.

## Recognize and reuse existing work

For a new project, identify the public API, compatibility policy, version format, target registry/package, release-note template, expected artifacts, signatures/provenance, previous-version support, and deprecation plan. For an existing project, inspect tags and releases, changelogs, actual downloaded commits/artifacts, historical breakages, build metadata, signatures, distribution channels, migration issues, and supported versions.

Use **established**, **inferred**, **incomplete**, **missing**, and **not-applicable**. A tag does not prove code or assets stayed unchanged; release notes do not prove compatibility; SemVer is meaningful only with a declared public API. If ISO standards or customer obligations require a different format, explicitly select it in the contract.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP defines the consumed API or contract, selects a version convention, produces readable notes, ties an artifact to the verified commit, and documents publication steps. A build ID and local log may suffice for an undistributed internal tool. Go deeper according to exposure and risk: immutable releases, signatures and attestations, SLSA provenance, compatibility matrices, canary channels, deprecation/migration guidance, SBOMs, consumer-side verification, and support policies. Adopt SemVer and Keep a Changelog intentionally rather than assuming them.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`devops-delivery` provides builds and provenance; `security-engineering` covers integrity and signatures; `quality-engineering` checks compatibility; support prepares messages; SRE covers production rollout risks. Do not modify published tags, reuse version numbers, turn raw logs into uncurated notes, or call `archive` publication. GitHub immutable releases are a platform option and do not protect copies distributed elsewhere. Internal refactoring without consumers or external artifacts does not trigger this extension unless it changes a documented contract.

The process and MVP depth are an integration synthesis; sources do not guarantee compatibility or integrity for projects that do not implement their conditions.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
