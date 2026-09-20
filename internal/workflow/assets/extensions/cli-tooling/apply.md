# cli-tooling — apply

Read the approved contract and only the selected references. Preserve existing user work, conventions, and authorizations; do not expand scope.

## Domain procedure

Establish arguments, configuration precedence, outputs, exit codes, and compatibility. Separate machine-readable stdout from diagnostic stderr; provide help and actionable errors. Implement non-interactive operation, interruption handling, idempotency, or preview mode where useful. Test pipes, JSON output, failure codes, paths containing spaces, missing configuration, and file preservation. Do not require an interactive terminal inside a script.

Break down this procedure according to the change's risks; steps may overlap or be not-applicable with justification. Start from reusable foundations identified during explore. Verify expected behavior incrementally and correct within the contract; use [check.md](check.md) for domain observations.

## Expected outcome

Deliver a concrete, inspectable result, its decisions, and evidence linked to actual `AC-n` IDs. Record the commands, environment, data, or participants actually used and what remains simulated. If the work reveals a new requirement, return to contract revision and approval; do not move a threshold to make a result pass.

This extension does not grant human approval, execute hooks, or implicitly authorize publication, external contact, or deployment. Carry out previously authorized actions within their scope; request only authorization that is actually missing for an action that requires it.
