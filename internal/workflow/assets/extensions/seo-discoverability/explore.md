# seo-discoverability — explore

Make public content discoverable and indexable according to its purpose and constraints.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: publish a public documentation page intended for organic search discovery. Out-of-scope example: modify a private dashboard not intended for indexing.

This extension makes public web content understandable and discoverable by search engines and people without promising ranking. It covers indexability, crawling, links, structure, titles, useful content, structured data, sitemaps, canonicals, and Search Console measurement. Select it during `stip-explore` when publishing or changing public pages, URLs, metadata, JavaScript rendering, link structures, sitemaps, structured data, or indexing controls. Private applications, intranets, and screens without public-discovery requirements do not trigger it; a website alone does not establish an SEO need.

Google Search Essentials distinguishes technical requirements, spam policies, and best practices while stating that compliance does not guarantee crawling, indexing, or display ([Search Essentials](https://developers.google.com/search/docs/essentials)). Its developer guide recommends checking Google's view of a page and considers security, performance, accessibility, and devices ([developer guide](https://developers.google.com/search/docs/fundamentals/get-started-developers)). Sitemaps suggest URLs without forcing indexing ([Build and submit a sitemap](https://developers.google.com/search/docs/crawling-indexing/sitemaps/build-sitemap?hl=en)). Schema.org supplies shared vocabulary without guaranteeing rich results ([Getting started](https://schema.org/docs/gs.html)). These are not legal obligations or traffic guarantees.

## Recognize and reuse existing work

For a new project, identify public URLs, audiences/intents, robots/noindex rules, titles/headings, links, canonicals, sitemaps, server/client rendering, structured data, and Search Console ownership. For an existing project, examine index coverage, crawling errors, traffic changes, orphan pages, redirects, duplicates, rich results, performance, and content issues.

Use **established**, **inferred**, **incomplete**, **missing**, and **not-applicable**. A sitemap does not establish URL accessibility or canonical status; valid JSON-LD does not guarantee rich results; browser visibility does not establish that Googlebot receives the same content. Distinguish intentionally unindexed pages, accidental blocks, and pages not yet crawled.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP covers a canonical URL, useful title/heading/content, crawlable links, no accidental blocking, a sitemap where needed, and HTML/rendering validation. For intentionally non-indexable pages, evidence is the rule and its test, not ranking. Go deeper according to scale and importance: international architecture, relevant structured data, performance budgets, Search Console monitoring, crawler logs, redirect migrations, JavaScript rendering tests, and editorial strategy. Google supplies no ranking-guaranteeing threshold; do not turn advice into a promise.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`quality-engineering` tests rendering and contracts; `privacy-engineering` prevents private personal-data indexing; support and docs provide useful content; cloud/devops cover performance and delivery. Avoid keyword stuffing, generated content without value, treating sitemaps as indexing buttons, and isolated ranking metrics. Nonpublic surfaces or explicitly excluded pages with unchanged rules do not trigger this extension; document non-applicability. Search results fluctuate and depend on external systems.

Search Essentials, sitemap, and Schema.org rules are their publishers' technical guidance. Process, statuses, and criteria are our integration synthesis.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
