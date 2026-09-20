import test from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  mkdirSync,
  writeFileSync,
  readFileSync,
  existsSync,
  rmSync,
  symlinkSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Coordinator } from "../stipulate/coordinator.mjs";
import { ProjectStore, atomic } from "../stipulate/store.mjs";

const clone = (value) => structuredClone(value);
function fixture(t, settings = {}) {
  const root = mkdtempSync(join(tmpdir(), "stip-research-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const store = new ProjectStore(root, {
    personal: join(root, "personal.json"),
  });
  atomic(store.path(".workflow/config.json"), {
    schema_version: 1,
    orchestration: settings,
  });
  const parent = {
    id: "ses_main",
    model: { providerID: "native", id: "main", variant: "high" },
    agent: "build",
    time: {},
  };
  const models = [
    {
      providerID: "native",
      id: "main",
      capabilities: { tools: true },
      settings: { reasoningEffort: "low" },
      variants: [{ id: "high", settings: { reasoningEffort: "high" } }],
    },
    {
      providerID: "native",
      id: "research",
      capabilities: { tools: true },
      settings: { reasoningEffort: "low" },
      variants: [
        {
          id: "low-fast",
          settings: { reasoningEffort: "low", serviceTier: "priority" },
        },
      ],
    },
  ];
  const sessions = new Map([[parent.id, clone(parent)]]),
    calls = [],
    definitions = [];
  const host = {
    models: async () => clone(models),
    session: async (id) => {
      if (host.unreachable?.has(id))
        throw new Error("Native backend disconnected");
      assert.ok(sessions.has(id), "Unknown native session " + id);
      return clone(sessions.get(id));
    },
    waiting: async (id) => host.waitingIDs?.has(id) || false,
    prepare: async (job) => {
      definitions.push(clone(job));
      if (host.prepareError) throw new Error(host.prepareError);
    },
    spawn: async (input, context) => {
      calls.push({ input: clone(input), context: clone(context) });
      if (host.spawnError) throw new Error(host.spawnError);
      if (host.missingID) return { content: "Unknown dispatch outcome" };
      const id = host.reuseID || "ses_helper_" + calls.length;
      const job = definitions.find((j) => j.agent_id === input.agent);
      sessions.set(id, {
        id,
        parentID: host.wrongParent || context.sessionID,
        model: job.profile.model,
        agent: input.agent,
        time: {},
        ...(!input.background ? { outcome: "succeeded" } : {}),
      });
      return {
        metadata: { sessionID: id },
        content:
          "Inspected package.json: no runtime dependencies are declared.",
      };
    },
    childID: (result) => result.metadata?.sessionID,
  };
  const coordinator = new Coordinator(store, host);
  let sequence = 0;
  const request = (input = {}, context = {}) =>
    coordinator.research(
      { question: "Which runtime dependencies are declared?", ...input },
      { sessionID: parent.id, id: "call_" + ++sequence, ...context },
    );
  return {
    root,
    store,
    parent,
    models,
    sessions,
    calls,
    definitions,
    host,
    coordinator,
    request,
  };
}

test("a draft helper inherits the coordinator and persists only read-only native metadata", async (t) => {
  const f = fixture(t),
    result = await f.request();
  assert.equal(result.kind, "helper");
  assert.deepEqual(result.profile.model, f.parent.model);
  assert.equal(result.status, "running");
  assert.equal(f.calls[0].input.background, true);
  assert.match(f.calls[0].input.prompt, /Do not create, edit or delete/);
  assert.match(f.calls[0].input.prompt, /Do not implement proposed solutions/);
  const job = f.store.helpers().jobs[0];
  assert.deepEqual(job.write_paths, []);
  assert.equal(job.parent_id, f.parent.id);
  assert.equal(job.session_id, result.session_id);
  assert.equal(job.acceptance, undefined);
  assert.equal(job.contract_digest, undefined);
  assert.equal(
    existsSync(f.store.path(`.workflow/.runtime/${job.helper_id}/state.json`)),
    false,
  );
  assert.deepEqual(f.store.changes(), []);
});

test("research uses actual phase-role effort and Fast capabilities", async (t) => {
  const f = fixture(t, {
    clients: {
      opencode: {
        phases: { validate: { model: "native/research" } },
        phase_roles: { validate: { security: { effort: "low", fast: true } } },
      },
    },
  });
  const result = await f.request({ role: "security", phase: "validate" });
  assert.deepEqual(result.profile.model, {
    providerID: "native",
    id: "research",
    variant: "low-fast",
  });
  assert.equal(result.profile.effort, "low");
  assert.equal(result.profile.fast, true);
  assert.equal(f.definitions[0].profile.model.variant, "low-fast");
});

test("foreground findings return to the coordinator without persisting native output", async (t) => {
  const f = fixture(t),
    result = await f.request({ background: false });
  assert.equal(result.status, "returned");
  assert.match(result.native_result.content, /no runtime dependencies/);
  const bytes = readFileSync(
    f.store.path(".workflow/.runtime/helpers.json"),
    "utf8",
  );
  assert.doesNotMatch(bytes, /no runtime dependencies/);
  assert.equal(JSON.parse(bytes).jobs[0].native_outcome, "succeeded");
});

test("exact tool-call replay is idempotent even after the helper returns", async (t) => {
  const f = fixture(t),
    first = await f.request({ background: false }, { id: "durable_call" });
  const replay = await f.request({ background: false }, { id: "durable_call" });
  assert.equal(replay.session_id, first.session_id);
  assert.equal(replay.reused, true);
  assert.equal(f.calls.length, 1);
  await assert.rejects(
    f.request({ question: "A different question" }, { id: "durable_call" }),
    /different research question/,
  );
  assert.equal(f.calls.length, 1);
});

test("a separate tool call cannot duplicate the same active helper request", async (t) => {
  const f = fixture(t),
    first = await f.request(),
    second = await f.request();
  assert.equal(second.helper_id, first.helper_id);
  assert.equal(second.reused, true);
  assert.equal(f.calls.length, 1);
});

test("unknown launch reserves capacity and is never automatically retried", async (t) => {
  const f = fixture(t, { defaults: { max_workers: 1 } });
  f.host.missingID = true;
  await assert.rejects(f.request(), /session identifier/);
  const retry = await f.request();
  assert.equal(retry.status, "unknown");
  assert.equal(retry.session_id, null);
  assert.equal(retry.reused, true);
  await assert.rejects(
    f.request({ question: "A second question" }),
    /Worker limit/,
  );
  assert.equal(f.calls.length, 1);
});

test("definite preparation failure is terminal while uncertain native launch stays unknown", async (t) => {
  const f = fixture(t, { defaults: { max_workers: 1 } });
  f.host.prepareError = "Agent unavailable";
  await assert.rejects(f.request(), /Agent unavailable/);
  assert.equal(f.store.helpers().jobs[0].status, "failed");
  f.host.prepareError = undefined;
  f.host.spawnError = "Connection lost after dispatch";
  await assert.rejects(f.request(), /Connection lost/);
  assert.deepEqual(
    f.store.helpers().jobs.map((j) => j.status),
    ["failed", "unknown"],
  );
  assert.equal(f.calls.length, 1);
});

test("restart reconciles a persisted native helper without redispatching it", async (t) => {
  const f = fixture(t),
    first = await f.request();
  f.host.unreachable = new Set([first.session_id]);
  assert.equal(
    (await f.coordinator.reconcileHelpers()).jobs[0].status,
    "unknown",
  );
  f.host.unreachable.clear();
  f.sessions.get(first.session_id).outcome = "succeeded";
  const restored = new Coordinator(
    new ProjectStore(f.root, { personal: join(f.root, "personal.json") }),
    f.host,
  );
  const state = await restored.reconcileHelpers();
  assert.equal(state.jobs[0].status, "returned");
  assert.equal(state.jobs[0].session_id, first.session_id);
  assert.equal(f.calls.length, 1);
});

test("waiting, cancelled and resumed native helpers remain truthfully observable", async (t) => {
  const f = fixture(t),
    first = await f.request();
  f.host.waitingIDs = new Set([first.session_id]);
  assert.equal(
    (await f.coordinator.reconcileHelpers()).jobs[0].status,
    "waiting",
  );
  f.host.waitingIDs.clear();
  f.sessions.get(first.session_id).outcome = "interrupted";
  assert.equal(
    (await f.coordinator.reconcileHelpers()).jobs[0].status,
    "cancelled",
  );
  delete f.sessions.get(first.session_id).outcome;
  f.sessions.get(first.session_id).execution_observed = true;
  assert.equal(
    (await f.coordinator.reconcileHelpers()).jobs[0].status,
    "running",
  );
});

test("finished helper history does not consume slots when old native sessions become unavailable", async (t) => {
  const f = fixture(t, { defaults: { max_workers: 1 } });
  const first = await f.request({ background: false });
  f.host.unreachable = new Set([first.session_id]);
  assert.equal(
    (await f.coordinator.reconcileHelpers()).jobs[0].status,
    "returned",
  );
  const second = await f.request({
    question: "Which tests cover startup?",
    background: false,
  });
  assert.equal(second.status, "returned");
  assert.equal(f.calls.length, 2);
});

test("helper capacity includes workers from every change", async (t) => {
  const f = fixture(t, { defaults: { max_workers: 1 } });
  atomic(f.store.path(".workflow/changes/other/state.json"), {
    phase: "applying",
  });
  f.store.mutate("other", () => ({
    version: 1,
    contract_digest: "old",
    jobs: [
      {
        run_id: "run_worker",
        task_id: "implement",
        attempt: 1,
        parent_id: "ses_other",
        session_id: null,
        launch_confirmed: false,
        status: "unknown",
        acceptance: "pending",
        write_paths: ["src/"],
      },
    ],
  }));
  await assert.rejects(f.request(), /Worker limit/);
  assert.equal(f.calls.length, 0);
  assert.equal(f.store.helpers().jobs.length, 0);
});

test("read-only research can share available capacity with the one project writer", async (t) => {
  const f = fixture(t, { defaults: { max_workers: 2 } });
  atomic(f.store.path(".workflow/changes/other/state.json"), {
    phase: "applying",
  });
  f.store.mutate("other", () => ({
    version: 1,
    contract_digest: "old",
    jobs: [
      {
        run_id: "run_worker",
        task_id: "implement",
        attempt: 1,
        parent_id: "ses_other",
        session_id: null,
        launch_confirmed: false,
        status: "unknown",
        acceptance: "pending",
        write_paths: ["src/"],
      },
    ],
  }));
  assert.equal((await f.request()).status, "running");
});

test("child sessions, invalid phases, unbounded questions and reserved roles cannot request helpers", async (t) => {
  const f = fixture(t);
  for (const input of [
    { phase: "apply" },
    { question: " " },
    { question: "x".repeat(8001) },
    { role: "discovery" },
    { role: "coordinator" },
    { role: "../security" },
    { background: "yes" },
    { change_id: "missing" },
  ]) {
    await assert.rejects(f.request(input));
  }
  await assert.rejects(
    f.request({}, { id: undefined }),
    /tool call identifier/,
  );
  f.sessions.set("ses_child", {
    id: "ses_child",
    parentID: f.parent.id,
    model: f.parent.model,
  });
  await assert.rejects(
    f.request({}, { sessionID: "ses_child" }),
    /main coordinator/,
  );
  assert.equal(f.calls.length, 0);
});

test("native identity mismatches leave an uncertain helper reserved", async (t) => {
  const f = fixture(t);
  f.host.wrongParent = "ses_unrelated";
  await assert.rejects(f.request(), /native child/);
  assert.equal(f.store.helpers().jobs[0].status, "unknown");
  assert.equal((await f.request()).reused, true);
  assert.equal(f.calls.length, 1);
});

test("a helper cannot reuse a session already assigned to a contract task", async (t) => {
  const f = fixture(t);
  atomic(f.store.path(".workflow/changes/other/state.json"), {
    phase: "applying",
  });
  f.sessions.set("ses_worker", {
    id: "ses_worker",
    parentID: f.parent.id,
    model: f.parent.model,
    outcome: "succeeded",
  });
  f.store.mutate("other", () => ({
    version: 1,
    contract_digest: "old",
    jobs: [
      {
        run_id: "run_worker",
        task_id: "implement",
        attempt: 1,
        parent_id: f.parent.id,
        session_id: "ses_worker",
        launch_confirmed: true,
        status: "returned",
        acceptance: "accepted",
        write_paths: ["src/"],
      },
    ],
  }));
  f.host.reuseID = "ses_worker";
  await assert.rejects(f.request(), /already assigned/);
  assert.equal(f.store.helpers().jobs[0].status, "unknown");
});

test("snapshot helpers follow the actual native root and do not mix conversations", async (t) => {
  const f = fixture(t),
    first = await f.request();
  f.sessions.set("ses_second", { ...f.parent, id: "ses_second" });
  const second = await f.request({}, { sessionID: "ses_second" });
  const childView = await f.coordinator.snapshot(first.session_id);
  assert.equal(childView.viewOnly, true);
  assert.deepEqual(
    childView.helpers.map((j) => j.session_id),
    [first.session_id],
  );
  const otherView = await f.coordinator.snapshot("ses_second");
  assert.deepEqual(
    otherView.helpers.map((j) => j.session_id),
    [second.session_id],
  );
  assert.equal((await f.coordinator.snapshot()).helpers.length, 2);
  assert.equal((await f.coordinator.snapshot("ses_unknown")).helpers.length, 0);
});

test("private helper registry rejects unsafe storage and corrupted read-only ownership", async (t) => {
  const f = fixture(t);
  await f.request();
  const path = f.store.path(".workflow/.runtime/helpers.json");
  const registry = f.store.helpers();
  registry.jobs[0].write_paths = ["src/"];
  atomic(path, registry);
  assert.throws(() => f.store.helpers(), /read-only helper/);
  atomic(path, { version: 2, jobs: [] });
  assert.throws(() => f.store.helpers(), /helper registry/);
  rmSync(path);
  const target = join(f.root, "unmanaged.json");
  writeFileSync(target, JSON.stringify({ version: 1, jobs: [] }));
  symlinkSync(target, path);
  assert.throws(() => f.store.helpers(), /Symlinks/);
  assert.throws(() => f.store.mutateHelpers((r) => r), /Symlinks/);
});

test("helper and contract mutations share the workflow lock", async (t) => {
  const f = fixture(t);
  writeFileSync(f.store.path(".workflow/.lock"), "external operation");
  assert.throws(() => f.store.mutateHelpers((r) => r), /locked/);
  assert.equal(
    existsSync(f.store.path(".workflow/.runtime/helpers.json")),
    false,
  );
});
