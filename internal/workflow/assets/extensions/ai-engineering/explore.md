# ai-engineering — explore

Scope and evaluate a model capability, including its tools, errors, costs, and human fallback.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the whole catalog. Relevant example: add an assistant that extracts data and proposes a controlled tool call. Out-of-scope example: add a deterministic rule without a model or probabilistic behavior.

This extension turns model-based capabilities into verifiable systems: tasks, context, tools, structured outputs, limits, evaluations, safeguards, costs, latency, traceability, and human fallback. Select it during `stip-explore` when a model or ML pipeline generates, classifies, summarizes, retrieves, calls tools, or chooses actions. It also applies when a model integration changes a user contract even if the call is hidden inside an SDK.

It does not apply to deterministic search, conventional business rules, or API integrations without model behavior. It does not replace `api-integrations`, `backend-engineering`, `privacy-engineering`, or human review; it exposes where model uncertainty enters the contract. This reference uses official OpenAI documentation for evaluations, function calling, and Agents SDK, plus NIST AI RMF for trustworthiness. It infers nothing about training data, memory, or model internals: only observed outputs in the configured deployment count as evidence.

## Recognize and reuse existing work

For a new project, identify tasks/users, model/configuration, prompts/context, tools, output schema, data policy, thresholds, evaluation data, cost/latency, logs, redaction, fallback, and ownership. For an existing project, replay representative cases, inspect errors/refusals, tool calls, invalid outputs, latency, quotas, and decision traces, and compare with the latest evaluation set. Classify findings as **established** (reproducible output, trace, evaluation, or command), **inferred** (unmeasured intent), **incomplete** (missing segment, tool, or error class), **missing** (search found no artifact), or **not-applicable** (no probabilistic decision/output). A versioned prompt does not establish quality; a successful demo does not establish robustness.

OpenAI's evaluation guide compares outputs with expected style/content and recommends representative data and reference labels ([Evals](https://developers.openai.com/api/docs/guides/evals)). Function calling describes an application supplying tools, receiving calls, executing them application-side, returning results, and obtaining a final response or further calls ([Function calling](https://developers.openai.com/api/docs/guides/function-calling)). The Agents guide distinguishes direct Responses API use from delegating loop management, orchestration, guardrails, approvals, and tracing to Agents SDK ([Agents SDK](https://developers.openai.com/api/docs/guides/agents)). NIST AI RMF provides a voluntary approach to trustworthy design, development, use, and evaluation ([AI Risk Management Framework](https://www.nist.gov/itl/ai-risk-management-framework)). These sources describe mechanisms and principles, not guaranteed domain-correct answers.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP includes a bounded task, output schema, small representative labeled/rubric-based dataset, agreed critical-class thresholds, output validation, at least one failure/refusal case, a budget, and human or deterministic fallback. Go deeper for sensitive decisions, private data, writing tools, many languages, multiple tenants, or cost/latency commitments: stratified evaluations, prompt/injection attacks, per-tool authorization, approvals, redaction, regression tests, segment monitoring, human review, and incident exercises. Agents SDK is not justification for general system access.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`api-integrations` owns authentication, timeouts, and tool contracts; `backend-engineering` executes and authorizes effects; `data-engineering` versions datasets and lineage; `analytics-experimentation` measures product effects; `privacy-engineering` bounds data and retention. Avoid model-selected irreversible effects without approval, secrets in prompts, treating well-formed JSON as truth, retries of writing tools without idempotency, or guarantees inferred from previous outputs. OpenAI documentation describes APIs and orchestration loops, not domain truth or constant behavior without evaluations. No training assumptions are made. Semantic search without generation can still trigger this extension when embeddings or reranking introduce probabilistic relevance to evaluate; deterministic indexes remain data/backend concerns according to the contract.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
