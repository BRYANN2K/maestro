# data-engineering — explore

Make data flows traceable, replayable, and verifiable from source to consumer.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: add incremental ingestion that handles duplicates, late data, and schema evolution. Out-of-scope example: change an interface without affecting data flows or transformations.

This extension makes data flows reproducible, observable, and operable: ingestion, transformation, partitioning, schemas, quality, orchestration, storage, lineage, and recovery. Select it during `stip-explore` when changing a source, job, DAG, dataset, file format, analytical table, time window, or quality rule, or when a product begins to depend on batch computation or an asynchronous pipeline.

It does not apply to request-local in-memory computation without a durable dataset, or transactional databases with no pipeline or analytical contract change. It does not choose Airflow, Parquet, dbt, or OpenLineage for the project. Airflow recommends treating tasks as transactions: complete outputs, repeatable results, explicit partitions, remote storage between workers, and secrets in connections ([Airflow Best Practices](https://airflow.apache.org/docs/apache-airflow/stable/best-practices.html)). Parquet separates metadata from column chunks so readers can identify columns before reading them ([Parquet File Format](https://parquet.apache.org/docs/file-format/)). Apply these practices when using the tools; they are not universal obligations.

## Recognize and reuse existing work

For a new project, identify sources and owners, frequency, watermarks, partitions, schemas, quality contracts, formats, storage, orchestration, secrets, retention, costs, and consumers. For an existing project, replay a past window, inspect inputs/outputs, simulate duplicates and delays, compare schemas, measure volumes, and rerun after failure. Classify findings as **established** (reproducible run, dataset, test, or artifact), **inferred** (unexecuted intent/documentation), **incomplete** (missing window, source, or column), **missing** (search found no artifact), or **not-applicable** (no durable pipeline/dataset). A green DAG proves neither idempotency nor freshness; readable Parquet does not establish business data quality.

OpenLineage distinguishes jobs, runs, and datasets, with design and run events; facets can describe schemas, statistics, and quality metrics ([OpenLineage Object Model](https://openlineage.io/docs/spec/object-model/)). This separates documenting a pipeline from establishing which partition a particular run processed. Parquet versions may introduce reader-incompatible features, so verify the actual compatibility matrix rather than the file extension alone ([Parquet format versions](https://parquet.apache.org/docs/file-format/versions/)).

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes an owned source, output dataset, schema/grain, partition or window, deterministic rerun, completeness test, and run report. Go deeper for decision-making, finance, or ML consumers, high volumes, multiple consumers, sensitive data, delays, or freshness SLAs: test backfill, schema evolution, reader compatibility, column-level quality, costs, runtime/design lineage, deletion, and restoration. Do not add a lineage platform to a local script without an ownership or audit need.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`database-engineering` owns transactional storage and indexes; `backend-engineering` provides events and contracts; `api-integrations` covers external ingestion; `analytics-experimentation` defines metric usage; `ai-engineering` may consume datasets without guaranteeing quality. Avoid non-idempotent tasks with retries, local paths between workers, backfills without fixed windows, implicit schemas, and decorative lineage. Adapt Airflow, Parquet, and OpenLineage models to the architecture rather than presenting them as a single standard. An unused temporary CSV does not trigger this extension.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
