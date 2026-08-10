# Focus-first output

Maestro's human-facing prose is **ADHD-friendly by default**. In the product,
the feature is called *focus-first output*: it describes the interface rather
than diagnosing or labelling the person using it.

## Contract

Human-facing Chat, Coach and Learn output follows these rules:

1. Put the answer, result, or smallest useful action first.
2. Split multi-step work into at most five numbered, bounded steps.
3. Keep one idea per short paragraph and suppress unrelated tangents.
4. Make progress and errors explicit with `Done:`, `State:`, `Blocked:`,
   `Cause:`, `Fix:`, and `Next:` labels.
5. End unfinished work with one concrete next action, not a pleasantry.

Coach exercises show exactly one `Next:` action, one two-minute time box, why
it matters now, and one `Done when:` condition. Learn artifacts start with the
file's purpose, prioritize at most five source regions (twelve with `--deep`),
and finish with at most one follow-up. Deterministic Learn and Coach failures
name both the cause and the concrete recovery action; unknown failures are not
given a guessed fix.

## Rendering

The TUI gives the stable state labels visual weight while preserving the exact
transcript. It never rewrites code fences, diffs, selected source, paths, tool
payloads, or saved conversation content. The hierarchy remains readable in
`NO_COLOR`; color is reinforcement, not the only signal. Fence tracking keeps
the opening delimiter type and width, including incomplete streamed blocks,
so a shorter nested delimiter cannot expose code to prose formatting.

Learn consumes the model's structured JSON privately. The TUI creates a
deterministic `Coach · generating a focused lesson` progress state before the
worker starts, resolves it when generation finishes, and shows only the
validated Markdown proposal or a human-readable `Cause`/`Fix` failure.
The Learn runner is independently confined: native runs expose zero tools
(including `read`, `grep`, and `ask`) and never connect MCP. Subscription CLI
routes fail closed because their filesystem reads cannot be restricted to the
source already embedded in the request; select a native/API model for Learn.

Step and list limits are generation and structured-response validation rules.
The renderer does not destructively shorten an already generated paragraph or
drop list items. The bounded legacy Learn fallback prefers a complete
paragraph, sentence, or word boundary and marks omitted text with an ellipsis.
Its code view is an exact source-line window capped in both lines and bytes;
omitted source is disclosed, and a line that cannot fit is never truncated or
presented as exact.

## Safety boundaries

This contract applies only to prose intended for a person. It is deliberately
excluded from proposal JSON, review verdicts, tool protocols, MCP schemas,
Git data, and every other machine-readable boundary. Readability guidance
cannot loosen permissions, change an operation, or make invalid structured
output acceptable. Chat builds one common task envelope, so native and
subscription routes receive the same focus-first guidance; structured Learn,
session-title, Skill, proposal, build, review, and docs prompts do not receive
it.

## Sources

- [`ayghri/i-have-adhd`](https://github.com/ayghri/i-have-adhd) — action-first
  output, bounded steps, visible state, concrete next actions, and
  matter-of-fact errors. The upstream project is MIT licensed.
- [W3C WAI: Use Clear Step-by-step Instructions](https://www.w3.org/WAI/WCAG2/supplemental/patterns/o4p07-step-instructions/)
- [W3C WAI: Make Each Step Clear](https://www.w3.org/WAI/WCAG2/supplemental/patterns/o1p04-clear-steps/)
- [W3C WAI: Keep Text Succinct](https://www.w3.org/WAI/WCAG2/supplemental/patterns/o3p05-succinct-text/)
- [W3C WAI: Cognitive and learning barriers](https://www.w3.org/WAI/people-use-web/abilities-barriers/cognitive/)

The W3C material is supplemental cognitive-accessibility guidance, not a
claim that one presentation works for every person. Maestro keeps motion
optional and makes focus, state, and controls understandable without it.
