# Maestro Coach

Maestro Coach is an optional, project-local learning loop for developers who
start from approximate prompts and want to become confident supervisors of a
spec-driven AI workflow. It is not a second chat persona and it never blocks
the delivery pipeline.

## Product principles

1. Practice on the real repository, at a natural breakpoint in the current
   task.
2. Start with a worked example, then ask the learner to explain the decision.
3. Fade help as independent evidence accumulates; restore help after a
   conceptual error.
4. Prefer deterministic evidence (source, diff, tests, parser output) before a
   model's feedback.
5. Revisit a skill in a different context. Completion of a lesson is not proof
   of transfer.
6. Stay quiet while the user types, selects code, reviews a permission, or an
   agent streams output.
7. Keep progress private by default. Never store or emit code, prompts, paths,
   diffs, free-text answers, or secrets as telemetry.

## Learning loop

Each skill follows the same cognitive-apprenticeship sequence:

| Step | Learner experience | Support |
|---|---|---|
| Observe | Read a short, labelled worked example from the current task. | Full reasoning and sub-goals. |
| Explain | Predict an outcome or explain why one decision was made. | Prompt plus optional hint. |
| Complete | Fill one missing acceptance criterion, test, or threat. | Hint ladder. |
| Perform | Do a real, bounded action in the IDE or spec flow. | On-demand help only. |
| Retrieve | Recall the same principle later in a different file or phase. | No answer shown first. |
| Reflect | Compare prediction with evidence and state the reusable rule. | Concise feedback. |

The initial curriculum covers:

- intent, users, constraints, non-goals, and observable outcomes;
- Given/When/Then scenarios and requirement-to-test traceability;
- repository navigation, code tracing, and dependency boundaries;
- reading diffs, checking assumptions, tests, and regression risk;
- secrets, input validation, authentication versus authorization, least
  privilege, dependency risk, and threat modelling;
- review evidence, documentation trade-offs, atomic commits, archive and
  rollback.

Guided mode includes examples and hints. Challenge mode asks the learner to
articulate the decision first and exposes help only on request. A skill fades
only after explicit successful practice, including at least one later or
different context; a model claim alone never changes mastery.

## Calm contextual UX

Coach is represented by the monochrome `∴` mark in the activity rail. An
automatic offer is at most one line and appears only after a breakpoint such
as a completed turn, validated proposal, available diff, completed test, or
review verdict. The initial policy is one offer per phase with a 20-minute
cooldown. Dismiss and snooze are durable.

`/learn` opens Coach mode and progress. Existing `/learn <path> [--deep]`
remains compatible as an explicit request to explain and optionally stage a
shareable note. The primary commands are:

- `/learn guided`, `/learn challenge`, `/learn off`, `/learn status`;
- `/learn next` to open the current contextual activity;
- `/learn done` and `/learn later` for explicit progress and snooze;
- `/learn <path> [--deep]` for the existing source explanation/export path.

Opening an activity never invokes a model or tool. `Enter` prepares the
contextual coaching prompt, and the user remains in control of sending it.

## Security and privacy contract

Learn source access is jailed to the active workspace. It accepts only a
bounded, regular UTF-8 text file and refuses symlinks, devices, FIFOs,
executables, binaries, secrets, credential/config stores, dependency caches,
generated output, and paths outside the repository. The snapshot carries a
SHA-256 fingerprint and exact line map.

Repository text is untrusted data. Model output is schema-validated and
bounded; claimed line ranges must exist and quoted code must match the source
snapshot. Learn never executes repository content. A check or test remains a
separate, explicit action governed by Maestro's normal permissions.

Private progress lives under Maestro's user data directory with `0700`
directories and atomic `0600` files. Shareable `maestro/learn/*.md` artifacts
are created only through the existing proposal review flow.

## Evaluation

The north-star measure is delayed, cross-context success without a hint—not
lesson completion. Product guardrails are time-to-spec, task completion,
dismiss/disable rate, interruption-during-input (target zero), token cost,
security false positives, and the learner's ability to identify an unsafe or
unsupported AI claim.

If analytics are enabled, events contain only schema version, salted project
identifier, skill/activity IDs, surface, support level, attempt/hint counts,
latency bucket and result. Content and paths are forbidden fields.

## Research basis

- Sweller & Cooper, worked examples and schema acquisition:
  <https://doi.org/10.1207/s1532690xci0201_3>
- Chi et al., self-explanation and monitoring understanding:
  <https://doi.org/10.1207/s15516709cog1302_1>
- Renkl, Atkinson & Maier, fading from examples to independent problems:
  <https://doi.org/10.1080/00220970209599510>
- Kalyuga et al., expertise reversal:
  <https://doi.org/10.1037/0022-0663.93.3.579>
- Roediger & Karpicke, delayed retention from retrieval practice:
  <https://doi.org/10.1111/j.1467-9280.2006.01693.x>
- Collins, Brown & Newman, cognitive apprenticeship:
  <https://doi.org/10.5840/thinking19888129>
- Corbett & Anderson, transparent per-skill learner modelling:
  <https://doi.org/10.1007/BF01099821>
- Microsoft Research Lumière, contextual assistance and user modelling:
  <https://www.microsoft.com/en-us/research/publication/lumiere-project-bayesian-user-modeling-inferring-goals-needs-software-users/>
- Iqbal & Bailey, deferring interruptions to task breakpoints:
  <https://doi.org/10.1145/1357054.1357070>
- Acar et al., source choice and secure coding outcomes:
  <https://conferences.computer.org/sp/pdfs/sp/2016/0824a289.pdf>
- Perry et al., security and overconfidence with AI assistants:
  <https://doi.org/10.1145/3576915.3623157>
- NIST Secure Software Development Framework 1.1:
  <https://csrc.nist.gov/pubs/sp/800/218/final>
- OWASP Application Security Verification Standard 5.0:
  <https://owasp.org/www-project-application-security-verification-standard/>

