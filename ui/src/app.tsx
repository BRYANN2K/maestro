import React, { useCallback, useEffect, useRef, useState } from "react";
import { useKeyboard, useRenderer, useTerminalDimensions } from "@opentui/react";
import { LinkedText, browserURL } from "./links";
import {
  Button,
  Caption,
  Composer,
  FocusRoot,
  Gap,
  Rule,
  Scroll,
  useFocus,
} from "./components";
import { T, clean, label, phaseIndex, phaseLabel, stages } from "./theme";
import type {
  Client,
  Message,
  Prompt,
  ProviderData,
  State,
  Task,
} from "./types";
import { Dialogs, type Modal } from "./dialogs";

export function App({ client, quit }: { client: Client; quit: () => void }) {
  const [modal, setModal] = useState<Modal>(null);
  return (
    <Boundary quit={quit}>
      <FocusRoot modal={modal}>
        <Workspace
          client={client}
          quit={quit}
          modal={modal}
          setModal={setModal}
        />
      </FocusRoot>
    </Boundary>
  );
}
class Boundary extends React.Component<
  { children: React.ReactNode; quit: () => void },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? (
      <Failure quit={this.props.quit} />
    ) : (
      this.props.children
    );
  }
}
function Failure({ quit }: { quit: () => void }) {
  useKeyboard((k) => {
    if (k.name === "q" || k.name === "escape" || (k.ctrl && k.name === "c"))
      quit();
  });
  return (
    <box padding={3} flexDirection="column">
      <text fg={T.ink}>The interface could not render this view.</text>
      <text fg={T.muted}>
        Press q to exit. Your project and saved sessions are preserved.
      </text>
    </box>
  );
}

function Workspace({
  client,
  quit,
  modal,
  setModal,
}: {
  client: Client;
  quit: () => void;
  modal: Modal;
  setModal: (m: Modal) => void;
}) {
  const { width, height } = useTerminalDimensions(),
    focus = useFocus();
  const renderer = useRenderer(), armedLink = useRef<string | null>(null);
  const [state, setState] = useState<State | null>(null),
    [error, setError] = useState(""),
    [backendBusy, setBusy] = useState(false),
    [pendingOperations, setPendingOperations] = useState(0),
    [loading, setLoading] = useState(true);
  const busy = backendBusy || pendingOperations > 0;
  const busyRef = useRef(busy), providersPending = useRef(false);
  busyRef.current = busy;
  const [stage, setStage] = useState(0),
    [tab, setTab] = useState("Specification"),
    [draft, setDraft] = useState(""),
    [messages, setMessages] = useState<Message[]>([]),
    [activity, setActivity] = useState(""),
    [disconnected, setDisconnected] = useState(false);
  const [compactChat, setCompactChat] = useState(false),
    [data, setData] = useState<ProviderData>({
      providers: [],
      accounts: [],
      models: [],
    }),
    [prompt, setPrompt] = useState<Prompt | null>(null),
    [diff, setDiff] = useState(""),
    [notice, setNotice] = useState("");
  const selected = useRef(""),
    session = useRef(""),
    generation = useRef(0),
    alive = useRef(true),
    streamRole = useRef(""),
    output = useRef("");
  const refreshQueue = useRef<Promise<unknown>>(Promise.resolve());
  const refresh = useCallback(
    async (change = selected.current) => {
      const gen = ++generation.current;
      setPendingOperations((n) => n + 1);
      try {
        const request = refreshQueue.current.then(() => client.request<State>("state", { change }));
        refreshQueue.current = request.catch(() => {});
        const next = await request;
        if (!alive.current || gen !== generation.current || !next) return;
        setState(next);
        setLoading(false);
        setError("");
        const id = next.workflow?.changeID || "";
        if (id !== selected.current || !session.current) {
          setStage(phaseIndex(next.workflow?.phase));
          setTab("Specification");
        }
        selected.current = id;
        if (next.session !== session.current) {
          session.current = next.session;
          setMessages(
            (next.conversation || []).slice(-100).map((m) => ({
              role: m.role,
              text: clean(m.content),
            })),
          );
        }
      } catch (e) {
        if (alive.current && gen === generation.current) {
          setLoading(false);
          setError(String((e as Error).message));
        }
      } finally {
        if (alive.current) setPendingOperations((n) => n - 1);
      }
    },
    [client],
  );
  const perform = useCallback(
    async (op: string, args: Record<string, unknown> = {}, success = "") => {
      setError("");
      setPendingOperations((n) => n + 1);
      try {
        const result = await client.request(op, args);
        if (success) setNotice(success);
        await refresh();
        return result ?? true;
      } catch (e) {
        setError(clean((e as Error).message));
        return false;
      } finally {
        if (alive.current) setPendingOperations((n) => n - 1);
      }
    },
    [client, refresh],
  );
  useEffect(() => {
    alive.current = true;
    const unsub = client.subscribe((event) => {
      if (!alive.current) return;
      if (event.event === "busy") {
        setBusy(Boolean(event.data));
        if (!event.data) {
          setActivity("");
          streamRole.current = "";
        }
      } else if (event.event === "disconnect") {
        setDisconnected(true);
        setBusy(false);
        setError(
          "The Maestro backend disconnected. Exit and relaunch to reconnect.",
        );
      } else if (event.event === "prompt") {
        setPrompt(event.data);
        setModal("prompt");
      } else if (event.event === "prompt_closed") {
        setPrompt((current) => {
          if (current?.id === event.data) {
            setModal(null);
            return null;
          }
          return current;
        });
      } else if (event.event === "output") {
        output.current = (output.current + clean(event.data)).slice(-12000);
        setNotice(output.current.trim().slice(-4000));
      } else if (event.event === "stream") {
        const e = event.data,
          c = e.Content || {};
        if (e.Type === "text_delta") {
          const text = clean(c.Text);
          if (!text) return;
          setMessages((previous) => {
            const next = [...previous],
              last = next.at(-1);
            if (
              streamRole.current === "assistant" &&
              last?.role === "assistant"
            )
              next[next.length - 1] = {
                role: "assistant",
                text: (last.text + text).slice(-128000),
              };
            else {
              next.push({ role: "assistant", text });
              streamRole.current = "assistant";
            }
            return next.slice(-100);
          });
        } else if (e.Type === "reasoning_delta")
          setActivity("Thinking through the change…");
        else if (e.Type === "tool_call")
          setActivity(`${clean(c.Name)} · running`);
        else if (e.Type === "tool_result")
          setActivity(
            c.Err ? `${clean(c.Name)} · failed` : `${clean(c.Name)} · complete`,
          );
        else if (e.Type === "sub_agent")
          setActivity(
            c.Status === "running"
              ? `${clean(c.Role)} · ${clean(c.Detail)}`
              : "",
          );
        else if (e.Type === "error") setError(clean(c.Message));
        else if (e.Type === "hitl" && c.ID === "resume")
          setMessages((m) => [...m, { role: "system", text: clean(c.Item) }]);
      }
    });
    void refresh();
    return () => {
      alive.current = false;
      unsub();
    };
  }, [client, refresh]);
  async function loadProviders(reportError = true) {
    if (providersPending.current) return;
    providersPending.current = true;
    try {
      {
        const next = await client.request<ProviderData>("providers");
        setData({
          providers: next.providers || [],
          accounts: next.accounts || [],
          models: next.models || [],
        });
      }
    } catch (e) {
      if (reportError) setError(clean((e as Error).message));
    } finally {
      providersPending.current = false;
    }
  }
  async function connections(view: Modal = "connections") {
    setModal(view);
    await loadProviders();
  }
  useEffect(() => {
    if (modal !== "models" && modal !== "connections") return;
    // Discovery completes asynchronously in the host. Keep an open picker
    // current without requiring the user to close it or restart Maestro.
    const timer = setInterval(() => {
      if (!busyRef.current) void loadProviders(false);
    }, 1000);
    return () => clearInterval(timer);
  }, [modal, client]);
  async function submit() {
    const text = draft.trim();
    if (!text || busy || disconnected) return;
    const aliases: Record<string, Modal> = {
      "/providers": "connections",
      "/model": "models",
      "/models": "models",
      "/resume": "history",
      "/files": "files",
      "/ide": "files",
      "/help": "commands",
      "/settings": "commands",
    };
    if (aliases[text]) {
      setDraft("");
      if (aliases[text] === "connections" || aliases[text] === "models")
        void connections(aliases[text]);
      else setModal(aliases[text]);
      return;
    }
    if (text === "/workflow") {
      setCompactChat(false);
      setDraft("");
      void refresh();
      return;
    }
    streamRole.current = "";
    setMessages((m) => [...m, { role: "user", text }].slice(-100));
    setDraft("");
    output.current = "";
    const ok = await perform("chat", { text });
    if (!ok) setDraft(text);
  }
  const cancel = () => {
    void client.request("cancel").catch(() => {});
    setPrompt(null);
    setModal(null);
  };
  useKeyboard((key) => {
    if (key.ctrl && key.name === "q") {
      key.preventDefault();
      quit();
      return;
    }
    if (key.ctrl && key.name === "c") {
      key.preventDefault();
      if (busy) cancel();
      else if (modal) setModal(null);
      else setDraft("");
      return;
    }
    if (key.ctrl && key.name === "k") {
      key.preventDefault();
      setModal(modal === "commands" ? null : "commands");
      return;
    }
    if (key.ctrl && key.name === "l") {
      key.preventDefault();
      void connections("models");
      return;
    }
    if (key.ctrl && key.name === "p") {
      key.preventDefault();
      void connections();
      return;
    }
    if (key.ctrl && key.name === "g") {
      key.preventDefault();
      setCompactChat((c) => !c);
      focus.set("composer");
      return;
    }
    if (key.name === "escape" && modal) {
      key.preventDefault();
      if (prompt) cancel();
      setModal(null);
      return;
    }
    if (!modal && key.ctrl && /^[1-5]$/.test(key.name)) {
      key.preventDefault();
      setStage(Number(key.name) - 1);
      setCompactChat(false);
    }
  });
  const wf = state?.workflow,
    change = wf?.changeID || "",
    tasks = wf?.tasks || [],
    wide = width >= 136,
    rail = width >= 108,
    short = height < 32;
  const title = clean(
    state?.documents?.spec?.match(/^#\s+(.+)$/m)?.[1] ||
      state?.contract?.title ||
      label(change) ||
      "A clear contract. A considered change.",
  );
  const currentDocument =
    stage === 0
      ? state?.documents?.proposal
      : stage === 1
        ? state?.documents?.spec
        : stage === 2
          ? state?.documents?.plan
          : stage === 3
            ? state?.documents?.evidence
            : state?.documents?.delivery;
  const revision = state?.contract?.contract_digest || "";
  function adjust() {
    setCompactChat(true);
    setDraft("Please adjust the contract: ");
    focus.set("composer");
  }
  const conversation = (
    <box
      flexDirection="column"
      flexGrow={1}
      minHeight={0}
      paddingX={wide ? 3 : 2}
      paddingTop={short ? 0 : 1}
    >
      <box flexDirection="row" justifyContent="space-between" height={1}>
        <Caption>CONVERSATION</Caption>
        <text fg={T.dim}>
          {messages.length.toString().padStart(2, "0")} messages
        </text>
      </box>
      <Gap />
      <Scroll id="conversation-scroll" sticky>
        {messages.length === 0 ? (
          <box flexDirection="column" gap={1}>
            <text fg={T.ink}>What would you like to change?</text>
            <text fg={T.muted} wrapMode="word">
              Explore an idea with Maestro. Shape the contract together before
              implementation begins.
            </text>
          </box>
        ) : (
          messages.map((m, i) => (
            <box
              key={i}
              flexDirection="column"
              marginBottom={2}
              flexShrink={0}
              gap={1}
            >
              <text fg={m.role === "assistant" ? T.accent : T.muted}>
                {m.role === "assistant"
                  ? "◇  MAESTRO"
                  : m.role === "user"
                    ? "YOU"
                    : "SESSION"}
              </text>
              <text fg={m.role === "system" ? T.muted : T.ink} wrapMode="word">
                <LinkedText text={m.text} />
              </text>
            </box>
          ))
        )}
      </Scroll>
      {busy && (
        <text fg={T.accent} height={1}>
          {clean(activity || "Maestro is working…")}
        </text>
      )}
      <box
        flexDirection="column"
        border={["left"]}
        borderColor={T.accent}
        backgroundColor={T.raised}
        paddingY={1}
        marginTop={1}
        flexShrink={0}
      >
        <Composer
          value={draft}
          onChange={setDraft}
          placeholder={
            busy
              ? "Working… Ctrl+C to cancel"
              : change
                ? "Refine the contract…"
                : "Describe the change…"
          }
          onSubmit={() => void submit()}
        />
      </box>
      <box
        flexDirection="row"
        justifyContent="space-between"
        paddingY={1}
        flexShrink={0}
      >
        <text fg={T.dim}>/ actions · Shift+Enter newline</text>
        <Button
          id="send"
          disabled={busy || !draft.trim()}
          onPress={() => void submit()}
        >
          ↵ send
        </Button>
      </box>
    </box>
  );
  return (
    <box
      flexDirection="column"
      width="100%"
      height="100%"
      backgroundColor={T.bg}
      onMouseDown={(event) => {
        armedLink.current = event.button === 0 ? renderer.getLinkAt(event.x, event.y) : null;
      }}
      onMouseDrag={() => { armedLink.current = null; }}
      onMouseUp={(event) => {
        const href = renderer.getLinkAt(event.x, event.y);
        if (event.button === 0 && href && href === armedLink.current && browserURL(href)) {
          event.preventDefault();
          void client.request("open_link", { url: href }).catch((e) => setError(clean(e.message)));
        }
        armedLink.current = null;
      }}
    >
      <box
        height={3}
        flexShrink={0}
        flexDirection="row"
        paddingX={2}
        alignItems="center"
        justifyContent="space-between"
        border={["bottom"]}
        borderColor={T.line}
      >
        <box height={1}>
          <text fg={T.ink}>
            <span fg={T.accent}>◇</span> <b>maestro</b>
            <span fg={T.muted}> / {clean(state?.project || "workspace")}</span>
          </text>
        </box>
        <Button id="connection-status" onPress={() => void connections()}>
          <span fg={T.muted}>{clean(state?.branch || "project")}</span>{" "}
          <span fg={disconnected ? T.red : state?.model ? T.green : T.dim}>
            ●
          </span>{" "}
          {disconnected
            ? "Disconnected"
            : state?.model
              ? "Model selected"
              : "Connect a model"}
        </Button>
      </box>
      <box
        height={3}
        flexShrink={0}
        flexDirection="row"
        justifyContent="space-between"
        alignItems="center"
        paddingX={1}
        border={["bottom"]}
        borderColor={T.line}
      >
        {stages.map((name, i) => (
          <box
            key={name}
            flexDirection="column"
            width="20%"
            height={2}
            border={stage === i ? ["bottom"] : []}
            borderColor={T.accent}
          >
            <Button
              id={`phase-${i}`}
              selected={stage === i && !compactChat}
              onPress={() => {
                setStage(i);
                setCompactChat(false);
                setTab("Specification");
              }}
            >
              {width >= 96 ? `${String(i + 1).padStart(2, "0")}  ` : ""}
              {name}
            </Button>
          </box>
        ))}
      </box>
      <box flexDirection="row" flexGrow={1} minHeight={0}>
        {rail && (
          <box
            flexDirection="column"
            width={27}
            paddingX={2}
            paddingTop={1}
            border={["right"]}
            borderColor={T.line}
          >
            <Caption>CHANGES</Caption>
            <Gap />
            <Scroll id="changes-scroll">
              {(wf?.changes || []).map((c) => (
                <box
                  key={c.id}
                  flexDirection="column"
                  marginBottom={1}
                  flexShrink={0}
                  backgroundColor={c.id === change ? T.selection : T.bg}
                  border={c.id === change ? ["left"] : []}
                  borderColor={T.accent}
                  paddingY={1}
                >
                  <Button
                    id={`change-${c.id}`}
                    selected={c.id === change}
                    disabled={busy}
                    onPress={() => {
                      setLoading(true);
                      void refresh(c.id);
                    }}
                  >
                    {c.archived ? "✓" : c.id === change ? "◆" : "◇"}{" "}
                    {label(clean(c.title || c.id, 22))}
                  </Button>
                  <box paddingLeft={2} height={1}>
                    <text fg={T.muted}>{phaseLabel(c.phase)}</text>
                  </box>
                </box>
              ))}
              <Button
                id="new-change"
                disabled={busy}
                onPress={() => setModal("new")}
              >
                + New change
              </Button>
            </Scroll>
            <box
              flexDirection="column"
              gap={1}
              paddingBottom={1}
              flexShrink={0}
            >
              <Button id="files" onPress={() => setModal("files")}>
                ▱ Files
              </Button>
              <Button id="history" onPress={() => setModal("history")}>
                ◷ History
              </Button>
              <Button id="connections" onPress={() => void connections()}>
                ⌁ Connections
              </Button>
              <Rule />
              <Button id="commands" onPress={() => setModal("commands")}>
                Ctrl+K Commands
              </Button>
            </box>
          </box>
        )}
        {(!compactChat || wide) && (
          <box
            flexDirection="column"
            flexGrow={1}
            minWidth={0}
            paddingX={width >= 136 ? 3 : 2}
            paddingTop={short ? 0 : 1}
            border={wide ? ["right"] : []}
            borderColor={T.line}
          >
            {loading && !state ? (
              <text fg={T.muted}>Opening your workspace…</text>
            ) : !change ? (
              <Welcome
                connected={Boolean(state?.model)}
                onNew={() => setModal("new")}
                onConnect={() => void connections()}
                onChat={() => {
                  setCompactChat(true);
                  focus.set("composer");
                }}
              />
            ) : (
              <>
                <Caption>
                  {stage === 0
                    ? "PROPOSAL"
                    : stage === 1
                      ? "CONTRACT"
                      : stage === 2
                        ? "EXECUTION"
                        : stage === 3
                          ? "EVIDENCE"
                          : "DELIVERY"}
                  {revision ? `  /  ${revision.slice(0, 8)}` : ""}
                </Caption>
                <Gap rows={short ? 0 : 1} />
                <text fg={T.ink} height={1}>
                  <b>{title}</b>
                </text>
                {!short && (
                  <text fg={T.muted} height={1}>
                    {phaseLabel(wf?.phase)}
                    {tasks.length
                      ? ` · ${tasks.length} task${tasks.length === 1 ? "" : "s"}`
                      : ""}
                  </text>
                )}
                <box
                  flexDirection="row"
                  marginTop={short ? 0 : 1}
                  height={2}
                  flexShrink={0}
                  border={["bottom"]}
                  borderColor={T.line}
                >
                  {["Specification", `Plan · ${tasks.length}`, "Changes"].map(
                    (name, i) => (
                      <Button
                        key={name}
                        id={`tab-${i}`}
                        selected={tab === (i === 1 ? "Plan" : name)}
                        onPress={() => {
                          const next = i === 1 ? "Plan" : name;
                          setTab(next);
                          if (next === "Changes")
                            void client
                              .request<string>("diff")
                              .then(setDiff)
                              .catch((e) => setError(e.message));
                        }}
                      >
                        {name}
                      </Button>
                    ),
                  )}
                </box>
                <Gap rows={short ? 0 : 1} />
                <Scroll id="document-scroll">
                  {tab === "Changes" ? (
                    <text fg={T.ink} wrapMode="word">
                      {clean(
                        diff ||
                          "No tracked changes against HEAD. Untracked files remain available in Files.",
                      )}
                    </text>
                  ) : tab === "Plan" || stage === 2 ? (
                    <TaskList
                      tasks={tasks}
                      state={state!}
                      busy={busy}
                      run={perform}
                    />
                  ) : stage === 3 ? (
                    <Evidence state={state!} />
                  ) : (
                    <Document
                      text={
                        currentDocument ||
                        "This artifact has not been written yet. Ask Maestro to prepare it in the conversation."
                      }
                    />
                  )}
                </Scroll>
                <Rule />
                {stage === 1 ? (
                  <box
                    flexDirection="column"
                    paddingY={short ? 0 : 1}
                    gap={short ? 0 : 1}
                    flexShrink={0}
                  >
                    <text
                      fg={state?.contract?.approval_current ? T.green : T.muted}
                    >
                      {state?.contract?.approval_current
                        ? "This exact contract is approved."
                        : "This version is awaiting your approval."}
                    </text>
                    <box
                      flexDirection="row"
                      gap={2}
                      alignItems="center"
                      height={short ? 1 : 3}
                      flexShrink={0}
                    >
                      <Button
                        id="approve"
                        primary
                        large={!short}
                        disabled={
                          busy ||
                          !revision ||
                          Boolean(state?.contractError) ||
                          state?.contract?.approval_current
                        }
                        onPress={() => setModal("approve")}
                      >
                        Approve contract ↵
                      </Button>
                      <Button id="adjust" disabled={busy} onPress={adjust}>
                        {width < 108 ? "Adjust" : "Request adjustment"}
                      </Button>
                    </box>
                  </box>
                ) : (
                  <box flexDirection="row" gap={1} paddingY={1} flexShrink={0}>
                    <Button
                      id="phase-action"
                      primary
                      disabled={busy}
                      onPress={() =>
                        stage === 0
                          ? adjust()
                          : stage === 2
                            ? void perform(
                                "start",
                                { change },
                                "Approved change started.",
                              )
                            : setModal("commands")
                      }
                    >
                      {stage === 0
                        ? "Refine proposal"
                        : stage === 2
                          ? "Start approved work"
                          : "Workflow actions"}
                    </Button>
                    {!wide && (
                      <Button
                        id="open-chat"
                        onPress={() => setCompactChat(true)}
                      >
                        Conversation →
                      </Button>
                    )}
                  </box>
                )}
              </>
            )}
          </box>
        )}
        {(wide || compactChat) && (
          <box
            flexDirection="column"
            width={
              wide
                ? Math.max(37, Math.min(49, Math.floor(width * 0.29)))
                : undefined
            }
            flexGrow={wide ? 0 : 1}
            minHeight={0}
          >
            {conversation}
          </box>
        )}
      </box>
      {!wide && !compactChat && (
        <box
          height={3}
          flexShrink={0}
          paddingX={2}
          paddingTop={1}
          border={["top"]}
          borderColor={T.line}
        >
          <Composer
            compact
            value={draft}
            onChange={setDraft}
            placeholder="Ask Maestro…  Ctrl+G conversation"
            onSubmit={() => {
              setCompactChat(true);
              void submit();
            }}
          />
        </box>
      )}
      {(error || state?.contractError || state?.workflowError || notice) && (
        <box
          height={
            short
              ? 1
              : Math.min(
                  3,
                  Math.max(
                    1,
                    Math.ceil(
                      clean(
                        error ||
                          state?.contractError ||
                          state?.workflowError ||
                          notice,
                      ).length / Math.max(1, width - 4),
                    ),
                  ),
                )
          }
          flexShrink={0}
          paddingX={2}
          backgroundColor={T.panel}
        >
          <text fg={error ? T.red : T.muted} wrapMode="word">
            <LinkedText text={clean(
              error || state?.contractError || state?.workflowError || notice,
              1400,
            )} />
          </text>
        </box>
      )}
      <box
        height={1}
        flexShrink={0}
        flexDirection="row"
        justifyContent="space-between"
        paddingX={2}
        backgroundColor={T.panel}
      >
        <text fg={T.muted}>
          {busy
            ? "● Working"
            : `${stages[stage]} · ${phaseLabel(wf?.phase).toLowerCase()}`}
        </text>
        <text fg={T.dim}>
          {width > 100
            ? `${clean(state?.model || "No model selected", 36)}   ·   `
            : ""}
          Ctrl+K actions Ctrl+Q quit
        </text>
      </box>
      {modal && (
        <Dialogs
          modal={modal}
          close={() => setModal(null)}
          setModal={setModal}
          client={client}
          state={state}
          data={data}
          prompt={prompt}
          busy={busy}
          error={error}
          notice={notice}
          run={perform}
          refresh={refresh}
          connect={connections}
          onDraft={(text) => {
            setDraft(text);
            setCompactChat(true);
            focus.set("composer");
          }}
        />
      )}
    </box>
  );
}
function Welcome({
  connected,
  onNew,
  onConnect,
  onChat,
}: {
  connected: boolean;
  onNew: () => void;
  onConnect: () => void;
  onChat: () => void;
}) {
  return (
    <box
      flexDirection="column"
      flexGrow={1}
      justifyContent="center"
      paddingX={2}
      gap={1}
    >
      <text fg={T.accent}>◇ YOUR NEXT CHANGE</text>
      <Gap />
      <text fg={T.ink}>
        <b>Good software starts with a clear intent.</b>
      </text>
      <text fg={T.muted} wrapMode="word">
        Explore the problem. Shape a contract. Build with evidence.
      </text>
      <Gap rows={2} />
      <box flexDirection="column" gap={1}>
        <Button id="welcome-new" primary onPress={onNew}>
          + Create a change
        </Button>
        <Button id="welcome-chat" onPress={onChat}>
          Start with a conversation →
        </Button>
        {!connected && (
          <Button id="welcome-connect" onPress={onConnect}>
            Connect your model
          </Button>
        )}
      </box>
      <Gap rows={2} />
      <text fg={T.dim}>Your contracts and code stay in your project.</text>
    </box>
  );
}
export function Document({ text }: { text: string }) {
  let heading = 0;
  const lines = clean(text)
    .replace(/^# [^\n]*\n?/, "")
    .trim()
    .split("\n")
    .slice(0, 1500);
  return (
    <box flexDirection="column" paddingRight={1}>
      {lines.map((raw, i) => {
        if (
          !raw.trim() &&
          (/^#{2,4} /.test(lines[i - 1] || "") ||
            /^#{2,4} /.test(lines[i + 1] || ""))
        )
          return null;
        const line = raw.replace(/\*\*/g, "").replace(/`/g, "");
        if (/^# /.test(line)) return null;
        if (/^#{2,4} /.test(line))
          return (
            <box
              key={i}
              flexDirection="column"
              marginTop={i ? 1 : 0}
              marginBottom={1}
              flexShrink={0}
            >
              <text fg={T.muted}>
                {String(++heading).padStart(2, "0")}{" "}
                {line.replace(/^#+\s*/, "").toUpperCase()}
              </text>
            </box>
          );
        const criterion = line.match(
          /^(?:[-*]\s*)?(?:\[[ x]\]\s*)?(AC-\d+)\s*[:—-]?\s*(.*)$/,
        );
        if (criterion)
          return (
            <box key={i} marginBottom={1} flexDirection="row" flexShrink={0}>
              <text fg={T.dim}>□ </text>
              <text fg={T.accent}>{criterion[1]} </text>
              <text fg={T.ink} wrapMode="word" flexShrink={1}>
                <LinkedText text={criterion[2]!} />
              </text>
            </box>
          );
        return line.trim() ? (
          <text
            key={i}
            fg={/^\s*(src|tests)\//.test(line) ? T.muted : T.ink}
            wrapMode="word"
            flexShrink={0}
          >
            <LinkedText text={line} />
          </text>
        ) : (
          <Gap key={i} />
        );
      })}
    </box>
  );
}
function TaskList({
  tasks,
  state,
  busy,
  run,
}: {
  tasks: Task[];
  state: State;
  busy: boolean;
  run: (
    op: string,
    args?: Record<string, unknown>,
    success?: string,
  ) => Promise<any>;
}) {
  return (
    <box flexDirection="column">
      {!tasks.length && (
        <text fg={T.muted}>Ask Maestro to prepare an execution plan.</text>
      )}
      {tasks.map((task, i) => {
        const job = state.workflow?.jobs?.find((j) => j.task_id === task.id);
        return (
          <box
            key={task.id}
            flexDirection="column"
            marginBottom={2}
            gap={1}
            flexShrink={0}
          >
            <text fg={T.ink}>
              <span fg={T.accent}>{String(i + 1).padStart(2, "0")} </span>
              <b>{label(task.id)}</b>{" "}
              <span fg={T.muted}>
                {" "}
                {job?.status || "queued"}
                {job ? ` · ${job.acceptance}` : ""}
              </span>
            </text>
            <text fg={T.ink} wrapMode="word">
              {clean(task.objective)}
            </text>
            <text fg={T.muted} wrapMode="word">
              {task.criteria.join(", ")} · {task.role}
            </text>
            <text fg={T.dim} wrapMode="word">
              Writes: {task.write_paths.join(", ") || "Read only"}
            </text>
            {!!task.depends_on.length && (
              <text fg={T.dim}>After: {task.depends_on.join(", ")}</text>
            )}
            <Button
              id={`run-${task.id}`}
              disabled={
                busy ||
                !state.contract?.approval_current ||
                job?.status === "running" ||
                job?.status === "returned"
              }
              onPress={() =>
                void run("delegate", {
                  change: state.workflow?.changeID,
                  task: task.id,
                })
              }
            >
              Run task →
            </Button>
            {job?.acceptance === "pending" && (
              <text fg={T.amber}>
                Returned work needs review before acceptance.
              </text>
            )}
          </box>
        );
      })}
    </box>
  );
}
function Evidence({ state }: { state: State }) {
  const criteria = state.contract?.check?.report?.criteria;
  if (!criteria?.length)
    return (
      <Document
        text={
          state.documents?.evidence ||
          "No verification evidence recorded yet. Ask Maestro to run the checks and record results for each acceptance criterion."
        }
      />
    );
  return (
    <box flexDirection="column">
      {criteria.map((c) => (
        <box key={c.id} flexDirection="column" gap={1} marginBottom={2}>
          <text fg={c.status === "passed" ? T.green : T.amber}>
            {c.id} {c.status}
          </text>
          <text fg={T.ink} wrapMode="word">
            {clean(c.evidence)}
          </text>
        </box>
      ))}
    </box>
  );
}
