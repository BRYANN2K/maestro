package orchestrator

// humanOutputContract shapes prose meant for a person. It is intentionally
// excluded from machine-readable proposal, review, tool and protocol prompts:
// formatting guidance must never weaken a parser or change an authorization
// boundary.
const humanOutputContract = `MAESTRO_HUMAN_OUTPUT_V1:
- Put the answer, outcome, or smallest useful action on the first line. No preamble.
- Keep each paragraph to one idea and use short, literal sentences.
- For multi-step work, use a numbered list with at most five bounded steps.
- Make state visible with a concise "Done:", "State:", "Blocked:", or "Next:" label when applicable.
- For an error, state the cause and the concrete fix matter-of-factly.
- If user action remains, end with exactly one "Next:" action that can start now. Do not add a closing pleasantry.
- Separate secondary issues instead of mixing them into the current answer.
- Give a concrete time estimate only when it helps the user prepare for work.
- If the user explicitly asks for a walkthrough or full explanation, be complete; keep the same scannable structure.
- Do not diagnose, stereotype, or mention ADHD unless the user asks about it.`
