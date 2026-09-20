# customer-support — explore

Prepare diagnosis, assistance, and escalation for problems users actually encounter.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the entire catalog. Relevant example: prepare a procedure to diagnose stalled synchronization and recover data. Out-of-scope example: refactor an internal function without affecting incidents, help, or user behavior.

This extension organizes user assistance and field feedback: channels, requests, knowledge bases, response times, resolution, escalation, complaints, support accessibility, and improvement loops. Select it during `stip-explore` when changing customer experience, help channels, support centers, complaint processes, troubleshooting information, or visible operational capabilities. It does not replace UX, SRE, quality, or legal work; it contributes the support perspective and evidence of perceived service.

ISO 10002:2018 provides guidance for planning, designing, operating, maintaining, and improving complaint handling ([ISO 10002](https://www.iso.org/cms/%20render/live/en/sites/isoorg/contents/data/standard/07/15/71580.html)). GOV.UK recommends estimating demand, defining service levels, and using requests to improve services ([Set up and manage user support](https://www.gov.uk/service-manual/helping-people-to-use-your-service/set-up-and-manage-user-support)). Zendesk describes first reply time, resolution, and CSAT calibrated to channels and expectations ([support metrics](https://support.zendesk.com/hc/en-us/articles/4408832234394-Analyzing-the-metrics-that-matter-to-improve-customer-support)). These are guidance and practices; SLAs, regulatory deadlines, or accessibility obligations apply only when required by context. This extension does not guarantee compliance or satisfaction.

## Recognize and reuse existing work

For a new project, identify audiences, channels, expected volumes, languages, assistance needs, hours, service levels, request categories, knowledge bases, escalations, incidents, and owners. For an existing service, inspect tickets, first-response/resolution times, recurring reasons, reopening rates, CSAT/comments, viewed articles, complaints, communicated incidents, and changes that generated volume.

Use **established**, **inferred**, **incomplete**, **missing**, and **not-applicable**. A published FAQ does not prove resolution; a configured SLA does not establish attainment; averages can hide long-running cases. Distinguish no requests due to absent traffic or hidden channels from no need. For new projects, test with representative users or simulated requests; for existing projects, compare before/after and segment by channel and reason.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP provides a visible contact channel, minimal classification, a first-line response, escalation procedure, and troubleshooting article/message for critical cases. For a small internal tool, an owned address or queue and testable runbook may suffice. Go deeper for volume, criticality, vulnerable audiences, or commitments: measured knowledge bases, multiple channels, hours/staffing, segmented SLAs, ISO 10002 complaint analysis, CSAT by reason, translation/accessibility, incident communication, and product feedback. Metrics must not reward fast but inaccurate responses.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

UX and accessibility address experience; `quality-engineering` verifies journeys; SRE and security address incidents and data; privacy governs tickets and identifiers; release communicates changes. Avoid impossible deadlines, speed-only measurement, confidential disclosures, or treating every request as a bug without triage. Libraries without supported users, undistributed internal notes, and changes affecting neither outcomes nor help channels do not trigger this extension; record the absence of need where relevant.

ISO 10002 guides complaints, GOV.UK describes public-service practices, and Zendesk provides tooling guidance; none guarantees experience in this context. Process, MVP thresholds, statuses, and criteria are our integration synthesis.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
