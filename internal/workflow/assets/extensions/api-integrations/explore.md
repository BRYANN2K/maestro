# api-integrations — explore

Define and verify an API boundary, including identities, errors, retries, and versions.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: integrate a webhook with signatures, duplicates, delays, and partial responses. Out-of-scope example: modify a purely local function without an API boundary.

This extension addresses boundaries between a product and an API: contract discovery, authentication, encoding, errors, deadlines, retries, idempotency, pagination, versioning, and compatibility. Select it during `stip-explore` when consuming, exposing, or replacing an HTTP API, OAuth integration, webhook, or external service, or when an SDK hides a contract decision. It also applies to internal changes observable by existing clients or integrations.

It does not apply to a purely local function with no network boundary. It does not replace domain design, overall security, `backend-engineering`, or the remote provider. OpenAPI defines a language-independent description of HTTP interfaces so people and tools can discover capabilities without reading source code ([OpenAPI Specification](https://spec.openapis.org/oas/latest.html)). RFC 9110 defines HTTP methods, statuses, headers, content, and request/response semantics ([HTTP Semantics](https://www.rfc-editor.org/rfc/rfc9110.html)). Protocol contracts do not establish provider reliability or the suitability of every field for the product.

## Recognize and reuse existing work

For a new project, find versioned specifications or documentation, environments, credentials and scopes, rate limits, request/response examples, deadlines, webhooks, ownership, and the provider's change policy. For an existing project, capture a real request in a safe environment, compare consumed and published schemas, read contract tests, inspect secret/status/retry handling, and verify actual integration usage. Classify findings as **established** (reproducible contract, test, or response), **inferred** (unverified documentation), **incomplete** (a direction or version is missing), **missing** (a search found no artifact), or **not-applicable** (no relevant boundary). An installed SDK does not prove the version actually called; a 200 response establishes neither error shapes nor future compatibility.

OAuth 2.0 distinguishes clients, resource owners, authorization servers, and resource servers, with access tokens sent to resources ([RFC 6749](https://datatracker.ietf.org/doc/html/rfc6749.html)). The IETF OAuth 2.0 Security Best Current Practice updates the threat model, recommends protections such as PKCE according to the flow, and deprecates less secure modes ([RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html)). RFC 6749 describes the protocol model; RFC 9700 supplies security recommendations that may require explicit compatibility decisions. Stripe documents idempotency keys for replaying supported requests without duplicate effects, retaining the first result for a documented window ([Idempotent requests](https://docs.stripe.com/api/idempotent_requests?lang=curl)). These references inform specific decisions; they do not justify retrying every mutation or storing tokens without a rotation policy.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes a versioned contract for used operations, a minimal authentication flow, secrets outside the repository, a timeout, error handling, a contract test, and a disablement procedure. Go deeper when APIs modify money or data, receive webhooks, process sensitive information, serve multiple clients, face tight quotas, or have uncertain compatibility: examine rotation, least privilege, webhook signatures/replay, mutation idempotency, backward compatibility, per-provider observability, and graceful degradation. Having an SDK is not an integration strategy.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`backend-engineering` owns business execution paths, concurrency, and rollback; `database-engineering` verifies persistence and transactions; `frontend-engineering` translates errors into understandable states; `ai-engineering` may expose a tool without removing authorization checks. Avoid blindly retrying POST requests, logging tokens, treating HTTP 200 as business success, confusing schema compatibility with semantic compatibility, or relying on a sandbox more permissive than production. An unused internal URL does not trigger this extension; an API that changes visible content may do so even without client-code changes.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
