import type { Client, State, UIEvent } from "../src/types";
export function fixture(): State {
  return {
    project: "maestro",
    directory: "/fixture/maestro",
    branch: "main",
    session: "fixture-session",
    model: "fixture/coder",
    initialized: true,
    workflow: {
      available: true,
      changeID: "session-memory",
      phase: "draft",
      changes: [
        { id: "session-memory", title: "Session memory", phase: "draft" },
        { id: "model-discovery", title: "Model discovery", phase: "exploring" },
      ],
      tasks: [
        {
          id: "store-context",
          role: "dev",
          phase: "apply",
          criteria: ["AC-1", "AC-2"],
          depends_on: [],
          write_paths: ["internal/session"],
          objective: "Persist and restore the reviewed session context.",
        },
      ],
      jobs: [],
    },
    documents: {
      proposal:
        "# Session memory\n\nKeep the important context between sessions.",
      spec: "# Give every session a memory\n\n## Intent\nCarry decisions, constraints and progress into the next session.\nMaestro should pick up where you left off.\n\n## Acceptance criteria\n- AC-1: Save the current context when a session ends.\n- AC-2: Restore context when resuming a saved session.\n- AC-3: Keep project data inside its own workspace.\n\n## Boundaries\nNo automatic sharing across projects.\nThe user stays in control of what is remembered.\n\n## Implementation\ninternal/session  ·  context and persistence\ntests/session     ·  restoration and isolation",
      plan: "# Execution plan\nOne scoped task.",
    },
    contract: {
      contract_digest: "9a31c084b6f8".repeat(5).slice(0, 64),
      approval_current: false,
      phase: "draft",
    },
    conversation: [
      {
        role: "user",
        content: "I want Maestro to remember the context between sessions.",
      },
      {
        role: "assistant",
        content:
          "I have drafted a contract around persistence, restoration and project isolation.\n\nThe change is scoped to session storage. Review the acceptance criteria before we build.",
      },
    ],
  };
}
export class FakeClient implements Client {
  state = fixture();
  calls: { op: string; args: Record<string, unknown> }[] = [];
  listeners = new Set<(event: UIEvent) => void>();
  async request<T = any>(
    op: string,
    args: Record<string, unknown> = {},
  ): Promise<T> {
    this.calls.push({ op, args });
    if (op === "state") return structuredClone(this.state) as T;
    if (op === "approve") this.state.contract!.approval_current = true;
    if (op === "model") this.state.model = String(args.model);
    if (op === "providers")
      return {
        providers: [],
        accounts: [
          {
            id: "openai-codex",
            label: "OpenAI · ChatGPT",
            authenticated: false,
          },
        ],
        models: ["fixture/coder", "fixture/reviewer"],
      } as T;
    if (op === "files") return ["README.md"] as T;
    if (op === "file")
      return {
        path: "README.md",
        text: "# Source heading\nconst x = `literal`",
      } as T;
    if (op === "sessions")
      return [
        {
          id: "session-2",
          title: "Previous change",
          phase: "draft",
          disabled: true,
          disabled_reason: "Workspace unavailable",
        },
      ] as T;
    return true as T;
  }
  emit(event: string, data: any) {
    for (const l of this.listeners) l({ event, data });
  }
  subscribe(l: (e: UIEvent) => void) {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  }
  close() {}
}
