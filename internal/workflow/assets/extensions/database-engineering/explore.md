# database-engineering — explore

Preserve invariants, compatibility, performance, and recovery when changing persisted data.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: migrate a column used by two application versions without data loss. Out-of-scope example: change visual styling without affecting schemas, queries, or persistence.

This extension protects persisted data and the queries serving it: models, constraints, migrations, transactions, isolation, indexes, plans, backups, restoration, access, and schema evolution. Select it during `stip-explore` when adding or changing a table/collection, migration, nontrivial query, persistence invariant, retention policy, or backup/restore path. It can also apply when an API changes pagination or consistency without adding a table.

It does not apply to changes without persisted reads/writes unless performance or data contracts change. It does not choose PostgreSQL, SQLite, or an ORM for the project. The reviewed PostgreSQL 17 documentation describes MVCC visibility and concurrency while noting conflicts and deadlocks that still need handling ([Concurrency Control](https://www.postgresql.org/docs/17/mvcc.html)). It distinguishes estimated plans and costs from `EXPLAIN` and actual execution and row/time measurements from `EXPLAIN ANALYZE`; the latter can write data and requires a controlled environment, reversible transaction, or explicit authorization ([Using EXPLAIN](https://www.postgresql.org/docs/17/using-explain.html)). SQLite explains atomic commits, locks, and rollback after failure ([Atomic Commit](https://www.sqlite.org/atomiccommit.html)). These sources do not turn every CRUD change into optimization or failure testing.

## Recognize and reuse existing work

For a new project, identify schemas, data ownership, migrations, constraints, indexes, volume, critical queries, isolation, credentials, backups, restoration, and retention. For an existing project, inspect the actual database in a safe environment, read migrations and rollback procedures, measure queries with plans, simulate concurrency/duplicates, verify permissions, and restore a backup. Classify findings as **established** (reproducible DDL, plan, test, or restoration), **inferred** (unmeasured intent), **incomplete** (an engine, volume, or case missing), **missing** (search found no artifact), or **not-applicable** (no affected persisted data). A migration file does not establish compatibility with the previous version; a code constraint does not prove concurrent enforcement in the database.

OWASP recommends database isolation, API access for thick clients, encrypted transport when needed, and restricted privileges ([Database Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Database_Security_Cheat_Sheet.html)). Use this to choose relevant controls based on observed project secrets and roles. `EXPLAIN` characterizes a dated plan; it does not establish performance at every cardinality.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes schema/migration, a key constraint, a read/write test, least-privilege access, an exercised migration, and measurement of the critical query. Go deeper for financial or personal data, high volume, destructive migrations, multiple clients, high concurrency, or critical restoration: test expand/contract compatibility, deadlocks, isolation, plans across cardinalities, backfill, restored backups, encryption, and role separation. Do not optimize from an isolated plan without representative load.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`backend-engineering` owns business transactions, retries, and deployment; `api-integrations` exposes consistency and pagination; `data-engineering` covers analytical tables and lineage; `security-engineering` may deepen threat analysis. Avoid enforcing invariants only in the UI, combining data migration with incompatible changes, treating an unrestored backup as proof, or assuming indexes always reduce cost. SQLite and PostgreSQL have different properties; documentation examples do not establish behavior under this project's load. Changes without persistence or queries do not trigger this extension.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
