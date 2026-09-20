import { expect, test, setDefaultTimeout } from "bun:test";
import React, { act } from "react";
import { testRender } from "@opentui/react/test-utils";
import { App } from "../src/app";
import { FakeClient } from "./fixtures";
setDefaultTimeout(30000);

async function mount(client = new FakeClient(), width = 160, height = 48) {
  let setup!: Awaited<ReturnType<typeof testRender>>;
  await act(async () => {
    setup = await testRender(<App client={client} quit={() => {}} />, {
      width,
      height,
    });
    await Bun.sleep(20);
  });
  const settle = async () => {
    await act(async () => {
      await Bun.sleep(20);
    });
    await setup.renderOnce();
    // Focus reveal runs after layout; render the resulting scroll position.
    await setup.renderOnce();
  };
  const frame = () => setup.captureCharFrame();
  const click = async (text: string, last = false) => {
    await settle();
    const lines = frame().split("\n"),
      y = last
        ? lines.findLastIndex((l) => l.includes(text))
        : lines.findIndex((l) => l.includes(text));
    expect(y, `Missing ${text}\n${frame()}`).toBeGreaterThanOrEqual(0);
    await act(async () => {
      await setup.mockMouse.click(lines[y]!.indexOf(text), y);
    });
    await settle();
  };
  const key = async (
    text: string,
    modifiers?: { ctrl?: boolean; shift?: boolean },
  ) => {
    await act(async () => {
      setup.mockInput.pressKey(text, modifiers);
    });
    await settle();
  };
  const dispose = () =>
    act(async () => {
      setup.renderer.destroy();
    });
  await settle();
  return { ...setup, client, settle, frame, click, key, dispose };
}
test("three-pane contract, deliberate approval, model selection and responsive navigation", async () => {
  const ui = await mount();
  try {
    expect(ui.frame()).toContain("Give every session a memory");
    expect(ui.frame()).toContain("AC-1");
    expect(ui.frame()).toContain("CONVERSATION");
    await ui.click("Approve contract");
    expect(ui.frame()).toContain("APPROVE THIS CONTRACT");
    expect(ui.client.calls.some((c) => c.op === "approve")).toBe(false);
    await ui.click("Keep reviewing");
    expect(ui.client.calls.some((c) => c.op === "approve")).toBe(false);
    await ui.click("Approve contract");
    await ui.click("Confirm approval");
    expect(ui.client.calls.find((c) => c.op === "approve")?.args).toEqual({
      change: "session-memory",
      revision: ui.client.state.contract!.contract_digest,
    });
    expect(ui.frame()).toContain("This exact contract is approved.");
    await ui.key("l", { ctrl: true });
    expect(ui.frame()).toContain("CHOOSE A MODEL");
    await ui.click("fixture/reviewer");
    expect(ui.client.state.model).toBe("fixture/reviewer");
    await ui.click("Build");
    expect(ui.frame()).toContain("Persist and restore");
    await ui.click("Run task");
    expect(
      ui.client.calls.some(
        (c) => c.op === "delegate" && c.args.task === "store-context",
      ),
    ).toBe(true);
    for (const [w, h] of [
      [120, 36],
      [80, 24],
    ]) {
      await act(async () => ui.resize(w!, h!));
      await ui.settle();
      expect(ui.frame()).toContain("Ctrl+Q quit");
      await ui.key("g", { ctrl: true });
      expect(ui.frame()).toContain("CONVERSATION");
      await ui.key("g", { ctrl: true });
    }
  } finally {
    await ui.dispose();
  }
});
test("private key entry, permission deny, cancellation and literal file preview", async () => {
  const ui = await mount();
  try {
    await ui.key("p", { ctrl: true });
    await ui.click("API key ·");
    await act(async () => {
      await ui.mockInput.typeText("sk-private-fixture");
    });
    await ui.settle();
    expect(ui.frame()).not.toContain("sk-private-fixture");
    expect(ui.frame()).toContain("••••");
    await ui.click("Save connection");
    expect(ui.client.calls.find((c) => c.op === "key")?.args.key).toBe(
      "sk-private-fixture",
    );
    await act(async () => {
      ui.client.emit("busy", true);
      ui.client.emit("prompt", {
        id: "p1",
        kind: "permission",
        title: "Allow bash?",
        choices: ["Deny", "Allow once"],
        detail: "npm test",
      });
    });
    await ui.settle();
    expect(ui.frame()).toContain("PERMISSION REQUIRED");
    await ui.click("Deny");
    expect(ui.client.calls.find((c) => c.op === "answer")?.args).toEqual({
      id: "p1",
      value: "0",
    });
    await act(async () =>
      ui.client.emit("prompt", {
        id: "p2",
        kind: "permission",
        title: "Allow write?",
        choices: ["Deny", "Allow once"],
      }),
    );
    await ui.settle();
    await ui.key("ESCAPE");
    expect(ui.client.calls.some((c) => c.op === "cancel")).toBe(true);
    await act(async () => ui.client.emit("busy", false));
    await ui.settle();
    await ui.click("Files");
    await ui.click("README.md");
    expect(ui.frame()).toContain("# Source heading");
    expect(ui.frame()).toContain("`literal`");
  } finally {
    await ui.dispose();
  }
});
test("empty workspace and streamed conversation without invented activity", async () => {
  const client = new FakeClient();
  client.state = {
    ...client.state,
    model: "",
    workflow: { available: false, changes: [] },
    documents: {},
    contract: undefined,
    conversation: [],
  };
  const ui = await mount(client);
  try {
    expect(ui.frame()).toContain("Good software starts with a clear intent.");
    expect(ui.frame()).toContain("Connect a model");
    expect(ui.frame()).not.toContain("AC-1");
    await act(async () => {
      client.emit("stream", {
        Type: "text_delta",
        Content: { Text: "First " },
      });
      client.emit("stream", {
        Type: "text_delta",
        Content: { Text: "answer." },
      });
    });
    await ui.settle();
    expect(ui.frame()).toContain("First answer.");
    await ui.key("k", { ctrl: true });
    expect(ui.frame()).toContain("COMMANDS");
    await ui.key("ESCAPE");
  } finally {
    await ui.dispose();
  }
});

test("composer preserves pasted multiline text and clears after submission", async () => {
  const ui = await mount();
  try {
    await ui.click("Refine the contract…");
    await act(async () => {
      await ui.mockInput.pasteBracketedText("First line\nSecond line");
    });
    await ui.settle();
    expect(ui.frame()).toContain("First line");
    expect(ui.frame()).toContain("Second line");
    await ui.key("RETURN");
    expect(ui.client.calls.find((c) => c.op === "chat")?.args.text).toBe(
      "First line\nSecond line",
    );
  } finally {
    await ui.dispose();
  }
});

test("compact change picker and unreadable contract approval guard", async () => {
  const client = new FakeClient();
  client.state.contractError =
    "Cannot preview spec: artifact exceeds preview limit";
  const ui = await mount(client, 80, 24);
  try {
    await ui.click("Approve contract");
    expect(ui.frame()).not.toContain("APPROVE THIS CONTRACT");
    expect(client.calls.some((c) => c.op === "approve")).toBe(false);
    await ui.key("k", { ctrl: true });
    await ui.click("Switch change");
    await ui.click("Model discovery");
    expect(client.calls.at(-1)?.args.change).toBe("model-discovery");
  } finally {
    await ui.dispose();
  }
});

test("opening a new dialog restores text focus after a command click", async () => {
  const ui = await mount();
  try {
    await ui.key("k", { ctrl: true });
    await ui.click("New change");
    await act(async () => { await ui.mockInput.typeText("Keyboard review"); });
    await ui.settle();
    expect(ui.frame()).toContain("Keyboard review");
    await ui.click("Create change");
    expect(ui.client.calls.find(c => c.op === "create")?.args).toEqual({id: "keyboard-review", text: "Keyboard review"});
  } finally { await ui.dispose(); }
});

test("compact connection form scrolls keyboard focus into view", async () => {
  let deliverAccounts!: () => void;
  const accountsReady = new Promise<void>(resolve => { deliverAccounts = resolve; });
  class AccountsClient extends FakeClient {
    override async request<T = any>(op: string, args: Record<string, unknown> = {}): Promise<T> {
      const result = await super.request<any>(op, args);
      if (op === "providers") {
        await accountsReady;
        result.accounts = ["OpenAI", "Anthropic", "Google", "GitHub"].map(id => ({id, label: id, authenticated: false}));
      }
      return result;
    }
  }
  const ui = await mount(new AccountsClient(), 80, 24);
  try {
    await ui.key("p", { ctrl: true });
    expect(ui.frame()).toContain("openai");
    // Late account discovery changes layout while the same input stays focused.
    await act(async () => { deliverAccounts(); });
    await ui.settle();
    expect(ui.frame()).toContain("openai");
    await ui.key("TAB");
    expect(ui.frame()).toContain("Base URL");
    await ui.key("TAB");
    expect(ui.frame()).toContain("API key");
    await act(async () => { await ui.mockInput.typeText("sk-private-review"); });
    await ui.settle();
    expect(ui.frame()).toContain("••••");
    expect(ui.frame()).not.toContain("sk-private-review");
    await ui.key("TAB");
    expect(ui.frame()).toContain("Save connection");
    await ui.key("RETURN");
    expect(ui.client.calls.find(c => c.op === "key")?.args.key).toBe("sk-private-review");
  } finally { await ui.dispose(); }
});

test("HTTP links are clickable in messages, contracts and login prompts", async () => {
  const client = new FakeClient();
  client.state.conversation = [{role: "assistant", content: "Read [documentation](https://example.test/docs)."}];
  client.state.documents!.spec = "# Review links\n\n## Reference\nSee https://example.test/contract.";
  const ui = await mount(client);
  try {
    expect(client.calls.some(c => c.op === "open_link")).toBe(false);
    const lines = ui.frame().split("\n");
    const y = lines.findIndex(line => line.includes("documentation"));
    const x = lines[y]!.indexOf("documentation");
    await act(async () => { await ui.mockMouse.drag(x, y, x + 5, y); });
    await ui.settle();
    expect(client.calls.some(c => c.op === "open_link")).toBe(false);
    await ui.click("documentation");
    expect(client.calls.filter(c => c.op === "open_link").at(-1)?.args.url).toBe("https://example.test/docs");
    await ui.click("https://example.test/contract");
    expect(client.calls.filter(c => c.op === "open_link").at(-1)?.args.url).toBe("https://example.test/contract");
    await act(async () => {
      client.emit("busy", true);
      client.emit("prompt", { id: "oauth-link", kind: "input", title: "Complete login", detail: "Open https://example.test/login?state=fixture&code=123" });
    });
    await ui.settle();
    await ui.click("https://example.test/login");
    expect(client.calls.filter(c => c.op === "open_link").at(-1)?.args.url).toBe("https://example.test/login?state=fixture&code=123");
  } finally { await ui.dispose(); }
});

test("actions stay disabled until the post-operation state refresh completes", async () => {
  class SlowClient extends FakeClient {
    holdState = false;
    release?: () => void;
    override async request<T = any>(op: string, args: Record<string, unknown> = {}): Promise<T> {
      if (op === "approve") { this.holdState = true; this.emit("busy", false); }
      if (op === "state" && this.holdState) await new Promise<void>(resolve => { this.release = resolve; });
      return super.request<T>(op, args);
    }
  }
  const client = new SlowClient();
  const ui = await mount(client);
  try {
    await ui.click("Approve contract");
    await ui.click("Confirm approval");
    await ui.click("Confirm approval");
    expect(client.calls.filter(c => c.op === "approve")).toHaveLength(1);
    await act(async () => { client.release?.(); await Bun.sleep(20); });
    await ui.settle();
    expect(ui.frame()).toContain("This exact contract is approved.");
  } finally { client.release?.(); await ui.dispose(); }
});

test("model discovery updates an already open selector", async () => {
  class DiscoveringClient extends FakeClient {
    discovered = false;
    override async request<T = any>(op: string, args: Record<string, unknown> = {}): Promise<T> {
      const result = await super.request<any>(op, args);
      if (op === "providers") result.models = this.discovered ? ["local/discovered"] : [];
      return result;
    }
  }
  const client = new DiscoveringClient(), ui = await mount(client);
  try {
    await ui.key("l", {ctrl: true});
    expect(ui.frame()).not.toContain("local/discovered");
    client.discovered = true;
    await act(async () => { await Bun.sleep(1150); });
    await ui.settle();
    expect(ui.frame()).toContain("local/discovered");
  } finally { await ui.dispose(); }
});
