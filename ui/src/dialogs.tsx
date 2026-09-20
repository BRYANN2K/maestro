import React, { useEffect, useRef, useState } from "react";
import { useTerminalDimensions } from "@opentui/react";
import { Button, Caption, Field, Gap, Rule, Scroll } from "./components";
import { T, clean, label } from "./theme";
import { LinkedText } from "./links";
import type { Client, Prompt, ProviderData, State } from "./types";
export type Modal =
  | "changes"
  | "commands"
  | "connections"
  | "models"
  | "new"
  | "approve"
  | "prompt"
  | "history"
  | "files"
  | null;
export function Dialogs({
  modal,
  close,
  setModal,
  client,
  state,
  data,
  prompt,
  busy,
  error,
  notice,
  run,
  refresh,
  connect,
  onDraft,
}: {
  modal: Modal;
  close: () => void;
  setModal: (m: Modal) => void;
  client: Client;
  state: State | null;
  data: ProviderData;
  prompt: Prompt | null;
  busy: boolean;
  error: string;
  notice: string;
  run: (
    op: string,
    args?: Record<string, unknown>,
    success?: string,
  ) => Promise<any>;
  refresh: (change?: string) => Promise<void>;
  connect: (m?: Modal) => Promise<void>;
  onDraft: (text: string) => void;
}) {
  const { width, height } = useTerminalDimensions();
  const [query, setQuery] = useState(""),
    [provider, setProvider] = useState("openai"),
    [key, setKey] = useState(""),
    [url, setURL] = useState(""),
    [title, setTitle] = useState(""),
    [items, setItems] = useState<any[]>([]),
    [file, setFile] = useState<{ path: string; text: string } | null>(null),
    [localError, setLocalError] = useState("");
  const gen = useRef(0);
  useEffect(() => {
    setQuery("");
    setKey("");
    setFile(null);
    setLocalError("");
    setItems([]);
    const current = ++gen.current;
    if (modal === "history" || modal === "files")
      void client
        .request<any[]>(modal === "history" ? "sessions" : "files")
        .then((v) => {
          if (current === gen.current) setItems(v || []);
        })
        .catch((e) => {
          if (current === gen.current) setLocalError(e.message);
        });
    return () => {
      gen.current++;
    };
  }, [modal, client]);
  const action = async (
    op: string,
    args: Record<string, unknown>,
    message = "",
  ) => {
    const success = await run(op, args, message);
    if (success) {
      setKey("");
      close();
    }
    return success;
  };
  const chooser = (name: string) => () => {
    close();
    onDraft(name);
  };
  const change = state?.workflow?.changeID || "";
  const headings: Record<string, string> = {
    commands: "COMMANDS",
    connections: "CONNECTIONS",
    models: "CHOOSE A MODEL",
    new: "NEW CHANGE",
    approve: "APPROVE THIS CONTRACT",
    prompt:
      prompt?.kind === "permission" ? "PERMISSION REQUIRED" : "YOUR INPUT",
    history: "SESSION HISTORY",
    changes: "SWITCH CHANGE",
    files: file ? "FILE PREVIEW" : "PROJECT FILES",
  };
  const dialogWidth = Math.min(width - 4, modal === "files" && file ? 110 : 80),
    dialogHeight = Math.min(
      height - 4,
      modal === "approve" ? 18 : modal === "new" ? 21 : 32,
    );
  return (
    <box
      position="absolute"
      top={Math.max(1, Math.floor((height - dialogHeight) / 2))}
      left={Math.max(1, Math.floor((width - dialogWidth) / 2))}
      width={dialogWidth}
      height={dialogHeight}
      zIndex={100}
      backgroundColor={T.panel}
      border
      borderColor={T.line}
      flexDirection="column"
      paddingX={2}
      paddingY={1}
    >
      <box
        flexDirection="row"
        height={1}
        flexShrink={0}
        justifyContent="space-between"
      >
        <text fg={T.accent}>
          <b>{headings[modal || ""]}</b>
        </text>
        <Button
          id="modal-close"
          onPress={() => {
            if (prompt) void client.request("cancel");
            close();
          }}
        >
          esc close
        </Button>
      </box>
      <Gap />
      {modal === "commands" && (
        <>
          <Field
            id="modal-search"
            value={query}
            onChange={setQuery}
            placeholder="Find an action, or enter a /command"
            onSubmit={() => {
              if (query.startsWith("/")) {
                close();
                onDraft(query);
              }
            }}
          />
          <Gap />
          <Scroll id="modal-list">
            {[
              ["New change", () => setModal("new")],
              ["Switch change", () => setModal("changes")],
              ["Connect a model", () => void connect()],
              ["Select model", () => void connect("models")],
              ["Browse files", () => setModal("files")],
              ["Session history", () => setModal("history")],
              [
                "Refresh contract",
                () => {
                  void refresh();
                  close();
                },
              ],
              [
                "Validate current contract",
                () =>
                  void action("validate", { change }, "Contract validated."),
              ],
              [
                "Start approved work",
                () =>
                  void action("start", { change }, "Approved work started."),
              ],
              [
                "Record verification evidence",
                chooser(`/workflow check ${change} --results `),
              ],
              [
                "Document this change",
                chooser(`/workflow docs ${change} --summary `),
              ],
              [
                "Archive reviewed change",
                chooser(`/workflow archive ${change} --message `),
              ],
              [
                "Review a worker contribution",
                chooser(
                  `/workflow contribution ${change} TASK accepted "Review reason"`,
                ),
              ],
              ["Configure workflow profiles", chooser("/workflow profiles")],
              [
                "Install domain extensions",
                chooser("/workflow install-extensions "),
              ],
              ["Manage Git workspaces", chooser("/git list")],
              ["Manage integrations", chooser("/mcp list")],
              ["Discover skills", chooser("/skills list")],
            ]
              .filter(([name]) =>
                String(name).toLowerCase().includes(query.toLowerCase()),
              )
              .map(([name, fn], i) => (
                <box key={String(name)} marginBottom={1} flexShrink={0}>
                  <Button
                    id={`modal-command-${i}`}
                    disabled={busy}
                    onPress={fn as () => void}
                  >
                    {String(name)}
                  </Button>
                </box>
              ))}
          </Scroll>
          <Rule />
          <text fg={T.muted}>
            Tab navigate · Enter choose · Ctrl+1…5 phases · Ctrl+G chat
          </text>
        </>
      )}
      {modal === "new" && (
        <>
          <text fg={T.muted} wrapMode="word">
            Give the change a name. Maestro will create its proposal and
            contract files.
          </text>
          <Gap />
          <Caption>TITLE</Caption>
          <Field
            id="modal-search"
            value={title}
            onChange={setTitle}
            placeholder="Add authentication"
          />
          <Gap />
          <Caption>CHANGE ID</Caption>
          <Field
            id="modal-id"
            value={query}
            onChange={setQuery}
            placeholder={
              title
                .toLowerCase()
                .replace(/[^a-z0-9]+/g, "-")
                .replace(/^-|-$/g, "") || "add-authentication"
            }
          />
          <Gap />
          <Button
            id="modal-create"
            primary
            disabled={busy || !title.trim()}
            onPress={async () => {
              const id =
                query ||
                title
                  .toLowerCase()
                  .replace(/[^a-z0-9]+/g, "-")
                  .replace(/^-|-$/g, "");
              if (await action("create", { id, text: title })) {
                await refresh(id);
              }
            }}
          >
            Create change →
          </Button>
        </>
      )}
      {modal === "approve" && (
        <>
          <text fg={T.ink}>
            <b>{label(change)}</b>
          </text>
          <Gap />
          <text fg={T.muted} wrapMode="word">
            Approve the specification, acceptance criteria and execution plan
            you reviewed. Approval applies only to this exact contract.
          </text>
          <Gap />
          <text fg={T.accent}>
            Contract{" "}
            {state?.contract?.contract_digest?.slice(0, 12) || "unavailable"}
          </text>
          <text fg={T.dim}>
            Files stay unchanged until you start the approved work.
          </text>
          <Gap />
          <box flexDirection="row" gap={2}>
            <Button
              id="modal-confirm"
              primary
              disabled={
                busy ||
                !state?.contract?.contract_digest ||
                Boolean(state?.contractError)
              }
              onPress={() =>
                void action(
                  "approve",
                  { change, revision: state?.contract?.contract_digest },
                  "Contract approved. Ready to build.",
                )
              }
            >
              Confirm approval
            </Button>
            <Button id="modal-review" onPress={close}>
              Keep reviewing
            </Button>
          </box>
        </>
      )}
      {(modal === "connections" || modal === "models") && (
        <>
          {modal === "models" ? (
            <>
              <Field
                id="modal-search"
                value={query}
                onChange={setQuery}
                placeholder="Search provider / model…"
              />
              <Gap />
              <Scroll id="modal-models">
                {data.models
                  .filter((m) => m.toLowerCase().includes(query.toLowerCase()))
                  .slice(0, 250)
                  .map((m, i) => (
                    <Button
                      key={m}
                      id={`modal-model-${i}`}
                      selected={state?.model === m}
                      disabled={busy}
                      onPress={() =>
                        void action("model", { model: m }, `Selected ${m}`)
                      }
                    >
                      {m}
                    </Button>
                  ))}
                {!data.models.length && (
                  <text fg={T.muted}>
                    Connect a provider to discover its models.
                  </text>
                )}
              </Scroll>
            </>
          ) : (
            <Scroll id="modal-connections">
              <Caption>ACCOUNTS</Caption>
              <Gap />
              {data.accounts.map((account) => (
                <box
                  key={account.id}
                  flexDirection="row"
                  justifyContent="space-between"
                  height={2}
                  flexShrink={0}
                >
                  <text fg={T.ink}>{clean(account.label)}</text>
                  <Button
                    id={`modal-account-${account.id}`}
                    disabled={busy}
                    onPress={() =>
                      void action(account.authenticated ? "logout" : "oauth", {
                        provider: account.id,
                      })
                    }
                  >
                    {account.authenticated ? "Disconnect" : "Connect →"}
                  </Button>
                </box>
              ))}
              <Rule />
              <Gap />
              <Caption>API KEY / OPENAI-COMPATIBLE ENDPOINT</Caption>
              <Gap />
              <Field
                id="modal-search"
                value={provider}
                onChange={setProvider}
                placeholder="Provider: openai, anthropic, or a custom name"
              />
              <Gap />
              <Field
                id="modal-url"
                value={url}
                onChange={setURL}
                placeholder="Base URL (custom endpoint only)"
              />
              <Gap />
              <Field
                id="modal-key"
                value={key}
                onChange={setKey}
                placeholder="API key · stored in your private vault"
                secret
              />
              <Gap />
              <box flexDirection="row" gap={1}>
                <Button
                  id="modal-save-key"
                  primary
                  disabled={
                    busy || !provider.trim() || (!key.trim() && !url.trim())
                  }
                  onPress={() =>
                    void action(
                      url.trim() ? "provider" : "key",
                      { provider: provider.trim(), url: url.trim(), key },
                      "Connection saved. Open the model selector to choose a model.",
                    )
                  }
                >
                  Save connection
                </Button>
                <Button
                  id="modal-open-models"
                  disabled={busy}
                  onPress={() => void connect("models")}
                >
                  Choose model →
                </Button>
              </box>
              <Gap />
              <text fg={T.dim} wrapMode="word">
                Compatible endpoints discover models automatically. Keys are
                never written to your project config.
              </text>
              <Gap />
              <Caption>CONFIGURED PROVIDERS</Caption>
              {data.providers.map((p) => (
                <text key={p.name} fg={T.muted}>
                  {p.name} · {p.models} models ·{" "}
                  {p.key_set
                    ? "key configured"
                    : p.requires_key
                      ? "key required"
                      : "local"}
                </text>
              ))}
            </Scroll>
          )}
          {busy && notice && (
            <box height={6} flexShrink={0}>
              <Scroll id="modal-auth-output">
                <text fg={T.accent} wrapMode="word">
                  <LinkedText text={notice} />
                </text>
              </Scroll>
            </box>
          )}
        </>
      )}
      {modal === "prompt" && prompt && (
        <>
          <text fg={T.ink} wrapMode="word">
            <b>{clean(prompt.title)}</b>
          </text>
          <Gap />
          <Scroll id="modal-prompt-detail">
            <text fg={T.muted} wrapMode="word">
              <LinkedText text={clean(
                prompt.detail ||
                  notice ||
                  "Maestro is waiting for your response.",
              )} />
            </text>
          </Scroll>
          <Gap />
          {prompt.choices?.length ? (
            <box flexDirection="column" gap={1}>
              {prompt.choices.map((choice, i) => (
                <Button
                  key={i}
                  id={`modal-answer-${i}`}
                  onPress={() => {
                    void client.request("answer", {
                      id: prompt.id,
                      value: String(i),
                    });
                    close();
                  }}
                >
                  {choice}
                </Button>
              ))}
            </box>
          ) : (
            <>
              <Field
                id="modal-search"
                value={key}
                onChange={setKey}
                placeholder="Enter value…"
                secret
                onSubmit={() => {
                  void client.request("answer", { id: prompt.id, value: key });
                  setKey("");
                  close();
                }}
              />
              <Gap />
              <Button
                id="modal-answer"
                primary
                onPress={() => {
                  void client.request("answer", { id: prompt.id, value: key });
                  setKey("");
                  close();
                }}
              >
                Continue →
              </Button>
            </>
          )}
        </>
      )}
      {modal === "changes" && (
        <>
          <Field
            id="modal-search"
            value={query}
            onChange={setQuery}
            placeholder="Find a change…"
          />
          <Gap />
          <Scroll id="modal-changes">
            {(state?.workflow?.changes || [])
              .filter((c) =>
                (c.title || c.id).toLowerCase().includes(query.toLowerCase()),
              )
              .map((c, i) => (
                <box key={c.id} marginBottom={1} flexShrink={0}>
                  <Button
                    id={`modal-change-${i}`}
                    disabled={busy}
                    selected={c.id === change}
                    onPress={() => {
                      void refresh(c.id);
                      close();
                    }}
                  >
                    {label(clean(c.title || c.id))} · {c.phase}
                  </Button>
                </box>
              ))}
          </Scroll>
          <Button id="modal-new-change" onPress={() => setModal("new")}>
            + New change
          </Button>
        </>
      )}
      {modal === "history" && (
        <>
          <Field
            id="modal-search"
            value={query}
            onChange={setQuery}
            placeholder="Search saved sessions…"
          />
          <Gap />
          <Scroll id="modal-history">
            {items
              .filter((s) =>
                String(s.title || s.id)
                  .toLowerCase()
                  .includes(query.toLowerCase()),
              )
              .map((s, i) => (
                <box
                  key={s.id}
                  flexDirection="column"
                  marginBottom={1}
                  flexShrink={0}
                >
                  <Button
                    id={`modal-session-${i}`}
                    disabled={busy || s.disabled}
                    onPress={() => void action("resume", { id: s.id })}
                  >
                    {clean(s.title || s.id)} · {s.phase}
                  </Button>
                  {s.disabled && (
                    <text fg={T.dim} wrapMode="word">
                      {clean(s.disabled_reason || "Workspace unavailable")}
                    </text>
                  )}
                </box>
              ))}
            {!items.length && <text fg={T.muted}>No saved sessions.</text>}
          </Scroll>
        </>
      )}
      {modal === "files" && (
        <>
          {file ? (
            <>
              <Button id="modal-back" onPress={() => setFile(null)}>
                ← Files
              </Button>
              <Caption>{clean(file.path)}</Caption>
              <Gap />
              <Scroll id="modal-source">
                <text fg={T.ink} wrapMode="word">
                  {clean(file.text)}
                </text>
              </Scroll>
            </>
          ) : (
            <>
              <Field
                id="modal-search"
                value={query}
                onChange={setQuery}
                placeholder="Filter project files…"
              />
              <Gap />
              <Scroll id="modal-files">
                {items
                  .filter((p) =>
                    String(p).toLowerCase().includes(query.toLowerCase()),
                  )
                  .slice(0, 500)
                  .map((p, i) => (
                    <Button
                      key={p}
                      id={`modal-file-${i}`}
                      disabled={busy}
                      onPress={() => {
                        const current = gen.current;
                        void client
                          .request<{ path: string; text: string }>("file", {
                            path: p,
                          })
                          .then((f) => {
                            if (current === gen.current) setFile(f);
                          })
                          .catch((e) => setLocalError(e.message));
                      }}
                    >
                      {clean(p)}
                    </Button>
                  ))}
              </Scroll>
            </>
          )}
        </>
      )}
      {(localError || error) && (
        <text fg={T.red} wrapMode="word" maxHeight={3}>
          {clean(localError || error)}
        </text>
      )}
    </box>
  );
}
