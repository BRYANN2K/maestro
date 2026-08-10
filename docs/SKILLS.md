# Agent Skills in Maestro

Maestro implements the [Agent Skills](https://agentskills.io) metadata
contract with progressive disclosure and an explicit execution boundary.

## Lifecycle

1. Startup retains only bounded `SKILL.md` frontmatter and a SHA-256 integrity
   hash of the complete bounded file. Body bytes are discarded immediately and
   are not injected. Catalog metadata is exposed to Settings and `/skills list`.
2. `/skills show <id>` reads the complete bounded source for human inspection.
3. `/skills run <id>` is the only instruction-injection path. It reopens the
   exact file identity discovered in step 1 and fails if it changed.
4. The configured Chat route executes the read-only turn. This works for both
   Maestro's native agent and subscription-backed agents such as Codex/Luna.

There is no description-based or model-triggered skill selection. A skill's
`allowed-tools`, metadata, body, or linked resources are untrusted data and
cannot expand Maestro permissions.

## Discovery roots

Project roots, in deterministic order:

- `.agents/skills`
- `.claude/skills` (compatibility)
- `.cursor/skills` (compatibility)

User roots:

- `~/.agents/skills`
- `~/.config/agents/skills` (compatibility)

`option skill-path` may add explicitly configured roots. Every root is scanned
one directory deep. Project/user path components, skill directories, and
`SKILL.md` must be real directories/files rather than symlinks.

Bounds are deliberately conservative: 16 roots, 512 immediate entries per
root, 256 valid skills total, 16 KiB of frontmatter, and 128 KiB per
`SKILL.md`. Source must be valid UTF-8 without terminal controls, bidi format
controls, YAML aliases, or custom YAML tags.

The official name contract is enforced: 1-64 lowercase ASCII letters, digits,
and single hyphens; no leading/trailing hyphen or `--`; frontmatter `name` must
equal the directory name. `description` is required and limited to 1024 safe
Unicode characters; `compatibility` is limited to 500.

Invalid entries appear as disabled diagnostics and cannot be enabled,
inspected as instructions, or run.

## Identity and collisions

Every valid skill receives a stable qualified ID such as:

- `project:security-review`
- `project-claude:security-review`
- `user:security-review`
- `configured-01:security-review`

An unqualified name is accepted only when unique. Collisions never use silent
precedence; use the qualified ID shown by `/skills list`.

## Enablement state

Valid discovered skills default to enabled for backward compatibility, but
enabled means "available for an explicit run," never automatic invocation.

```text
maestro skills disable project:security-review --scope=project
maestro skills enable project:security-review --scope=session
```

Session overrides take precedence over project defaults. State is stored in a
private per-project JSON record under Maestro's data directory (or
`MAESTRO_SKILLS_DIR` for an isolated runtime). Updates use a cross-process
owner lock, stale-lock quarantine, an atomic rename, a directory sync, `0700`
directories, and `0600` files.

## Execution authority

The complete skill source is transported in a JSON data envelope below a
runtime-owned `MAESTRO_OPERATION: READ_ONLY_TASK` block and the repository
authority contract. Running a skill does not authorize:

- file creation or modification;
- shell commands or Git mutations;
- network, MCP, or secret access;
- loading linked resources merely because the body mentions them;
- converting `allowed-tools` metadata into Maestro or vendor permissions.

Native and subscription routes use the same envelope. The execution route is
resolved through the current orchestrator role settings, so a configured
`subscription / codex / gpt-5.6-luna` route is preserved instead of silently
falling back to the native engine.
