# cloud-engineering — apply

Read the approved contract and only the selected references. Preserve existing user work, conventions, and authorizations; do not expand scope.

## Domain procedure

Compare active inventory with IaC; identify the provider, regions, IAM, quotas, and dependencies. Prepare the change plan, reproducible configuration, and rollback. Verify least-privilege access, health, and recovery in the authorized environment, then compare costs with agreed objectives. Record drift; a successful plan does not prove deployment. A cloud-only change does not activate UX/design domains.

Break down this procedure according to the change's risks; steps may overlap or be not-applicable with justification. Start from reusable foundations identified during explore. Verify expected behavior incrementally and correct within the contract; use [check.md](check.md) for domain observations.

## Expected outcome

Deliver a concrete, inspectable result, its decisions, and evidence linked to actual `AC-n` IDs. Record the commands, environment, data, or participants actually used and what remains simulated. If the work reveals a new requirement, return to contract revision and approval; do not move a threshold to make a result pass.

This extension does not grant human approval, execute hooks, or implicitly authorize publication, external contact, or deployment. Carry out previously authorized actions within their scope; request only authorization that is actually missing for an action that requires it.
