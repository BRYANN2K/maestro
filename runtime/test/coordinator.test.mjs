import test from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  existsSync,
  rmSync,
  symlinkSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { Coordinator } from "../stipulate/coordinator.mjs";
import { ProjectStore, atomic, hash, safe } from "../stipulate/store.mjs";

const clone = (value) => structuredClone(value);
const task = (id, phase = "apply", write_paths = ["src/" + id]) => ({
  id,
  role:
    phase === "check"
      ? "verification"
      : phase === "docs"
        ? "documentation"
        : "backend",
  phase,
  objective: "Complete " + id,
  criteria: ["AC-1"],
  depends_on: [],
  write_paths,
});
const parent = {
  id: "ses_parent",
  model: { providerID: "openai", id: "main", variant: "high" },
  agent: "build",
  time: {},
};
const models = [
  {
    providerID: "openai",
    id: "main",
    capabilities: { tools: true },
    settings: { reasoningEffort: "low" },
    variants: [{ id: "high", settings: { reasoningEffort: "high" } }],
  },
];

class MemoryStore {
  constructor(tasks) {
    this.tasks = clone(tasks);
    this.value = null;
    this.helperValue = { version: 1, jobs: [] };
    this.bound = {};
    this.preferences = { defaults: { max_workers: 2, max_shared_writers: 1 } };
    this.state = {
      schema_version: 2,
      id: "sample",
      phase: "applying",
      approval_current: true,
      contract_digest: "contract-1",
      extensions: ["backend-engineering"],
    };
  }
  status() {
    return clone(this.state);
  }
  plan() {
    return { version: 1, tasks: clone(this.tasks) };
  }
  settings() {
    return clone(this.preferences);
  }
  settingsByScope() {
    return { personal: {}, project: this.settings(), local: {} };
  }
  changes() {
    return [{ id: "sample", phase: this.state.phase, archived: false }];
  }
  registry() {
    return clone(this.value);
  }
  helpers() {
    return clone(this.helperValue);
  }
  mutateHelpers(fn) {
    this.helperValue = clone(fn(clone(this.helperValue)));
    return clone(this.helperValue);
  }
  mutate(id, fn) {
    const next = fn(clone(this.value));
    this.value = clone(next);
    return clone(next);
  }
  bind(id, change) {
    this.bound[id] = change;
  }
  binding(id) {
    return this.bound[id];
  }
}

function fixture(tasks = [task("implement")]) {
  const store = new MemoryStore(tasks),
    sessions = new Map([[parent.id, clone(parent)]]),
    calls = [];
  let currentJob;
  const host = {
    models: async () => {
      await host.onModels?.();
      return clone(models);
    },
    session: async (id) => {
      await host.onSession?.(id);
      if (host.unreachable?.has(id)) throw new Error("Disconnected");
      const value = sessions.get(id);
      assert.ok(value, "Unknown native session " + id);
      return clone(value);
    },
    waiting: async (id) => host.pendingPermissions?.has(id) || false,
    prepare: async (job) => {
      currentJob = clone(job);
      await host.onPrepare?.(job);
    },
    spawn: async (input, context) => {
      calls.push({ input: clone(input), context: clone(context) });
      await host.beforeSpawn?.();
      if (host.spawnError) throw new Error(host.spawnError);
      if (host.omitChildID) return {};
      const id =
        host.reuseChild || input.sessionID || "ses_child_" + calls.length;
      sessions.set(id, {
        id,
        parentID: host.wrongParent || context.sessionID,
        model: host.childModel || currentJob.profile.model,
        agent: currentJob.agent_id,
        time: {},
      });
      return { metadata: { sessionID: id } };
    },
    childID: (result) => result.metadata?.sessionID,
    interrupt: async (id) => {
      sessions.get(id).outcome = "interrupted";
    },
  };
  const coordinator = new Coordinator(store, host);
  const dispatch = (task_id = "implement", extra = {}) =>
    coordinator.delegate(
      { change_id: "sample", task_id, ...extra },
      { sessionID: parent.id },
    );
  const returned = async (id) => {
    sessions.get(id).outcome = "succeeded";
    await coordinator.reconcile("sample");
  };
  const accept = (taskID) =>
    coordinator.contribution(
      "sample",
      taskID,
      "accepted",
      "Inspected the contribution and its observed results.",
    );
  return {
    store,
    host,
    sessions,
    calls,
    coordinator,
    dispatch,
    returned,
    accept,
  };
}

test("delegation launches a real native child with inherited model and pending acceptance", async () => {
  const f = fixture(),
    result = await f.dispatch();
  assert.equal(f.calls.length, 1);
  assert.equal(f.calls[0].input.background, true);
  assert.equal(result.session_id, "ses_child_1");
  assert.deepEqual(result.profile.model, parent.model);
  assert.equal(result.acceptance, "pending");
  assert.equal(f.store.value.jobs[0].parent_id, parent.id);
  assert.equal(f.store.bound[parent.id], "sample");
  assert.match(f.calls[0].input.prompt, /AC-1/);
  assert.match(f.calls[0].input.prompt, /src\/implement/);
});

test("stale approval and non-v2 changes are rejected before dispatch", async () => {
  for (const state of [{ approval_current: false }, { schema_version: 1 }]) {
    const f = fixture();
    Object.assign(f.store.state, state);
    await assert.rejects(f.dispatch(), /approval|approve|plan/i);
    assert.equal(f.calls.length, 0);
    assert.equal(f.store.value, null);
  }
});

test("approval changed during asynchronous model lookup is checked again before reservation", async () => {
  const f = fixture();
  f.host.onModels = () => {
    f.store.state.approval_current = false;
    f.store.state.contract_digest = "contract-2";
  };
  await assert.rejects(f.dispatch(), /approval|contract|changed|stale/i);
  assert.equal(f.calls.length, 0);
  assert.equal(f.store.value?.jobs?.length || 0, 0);
});

test("phase eligibility is checked again if another client finalizes check during model lookup", async () => {
  const f = fixture(),
    first = await f.dispatch();
  await f.returned(first.session_id);
  await f.accept("implement");
  f.host.onModels = () => {
    f.store.state.phase = "checked";
  };
  await assert.rejects(
    f.dispatch("implement", { correction: true }),
    /phase|lifecycle|changed/i,
  );
  assert.equal(f.calls.length, 1);
});

test("invalid registry diagnostics block dispatch rather than appending to corrupt state", async () => {
  const f = fixture();
  f.store.state.runtime = {
    error: "Duplicate task attempt in native registry.",
  };
  await assert.rejects(f.dispatch(), /registry|runtime|duplicate/i);
  assert.equal(f.calls.length, 0);
});

test("a registry from an older contract cannot gain new worker reservations", async () => {
  const f = fixture();
  f.store.value = {
    version: 1,
    change_id: "sample",
    contract_digest: "old-contract",
    jobs: [],
  };
  const before = clone(f.store.value);
  await assert.rejects(f.dispatch(), /contract|registry/i);
  assert.deepEqual(f.store.value, before);
  assert.equal(f.calls.length, 0);
});

test("a child cannot recursively act as the main coordinator", async () => {
  const f = fixture();
  f.sessions.set("ses_nested", {
    ...parent,
    id: "ses_nested",
    parentID: parent.id,
  });
  await assert.rejects(
    f.coordinator.delegate(
      { change_id: "sample", task_id: "implement" },
      { sessionID: "ses_nested" },
    ),
    /main coordinator/i,
  );
  assert.equal(f.calls.length, 0);
});

test("restart reconciliation prevents duplicate dispatch of the same native task", async () => {
  const f = fixture();
  await f.dispatch();
  const resumed = new Coordinator(f.store, f.host);
  await assert.rejects(
    resumed.delegate(
      { change_id: "sample", task_id: "implement" },
      { sessionID: parent.id },
    ),
    /active|unknown|already/i,
  );
  assert.equal(f.calls.length, 1);
  assert.equal(f.store.value.jobs.length, 1);
});

test("concurrent callers cannot reserve the same task twice", async () => {
  const f = fixture();
  let release, signal;
  const barrier = new Promise((resolve) => {
    release = resolve;
  });
  const prepared = new Promise((resolve) => {
    signal = resolve;
  });
  f.host.onPrepare = () => signal();
  f.host.beforeSpawn = () => barrier;
  const first = f.dispatch();
  await prepared;
  await assert.rejects(f.dispatch(), /active|unknown|already/i);
  release();
  await first;
  assert.equal(f.calls.length, 1);
  assert.equal(f.store.value.jobs.length, 1);
});

test("native return remains pending until the coordinator accepts inspected output", async () => {
  const f = fixture(),
    result = await f.dispatch();
  await f.returned(result.session_id);
  assert.equal(f.store.value.jobs[0].status, "returned");
  assert.equal(f.store.value.jobs[0].acceptance, "pending");
  await f.accept("implement");
  assert.equal(f.store.value.jobs[0].acceptance, "accepted");
});

test("approval invalidated while observing a returned session blocks contribution acceptance", async () => {
  const f = fixture(),
    first = await f.dispatch();
  f.sessions.get(first.session_id).outcome = "succeeded";
  f.host.onSession = (id) => {
    if (id === first.session_id) f.store.state.approval_current = false;
  };
  await assert.rejects(
    f.accept("implement"),
    /approval|contract|stale|changed/i,
  );
  assert.notEqual(f.store.value.jobs[0].acceptance, "accepted");
});

test("native-confirmed model is recorded separately from the requested profile", async () => {
  const f = fixture();
  f.host.childModel = { providerID: "openai", id: "main" };
  const first = await f.dispatch();
  await f.returned(first.session_id);
  assert.deepEqual(f.store.value.jobs[0].profile.model, parent.model);
  assert.equal(f.store.value.jobs[0].actual_model, "openai/main");
});

test("directly resuming an accepted native child invalidates acceptance and quiescence", async () => {
  const f = fixture(),
    first = await f.dispatch();
  await f.returned(first.session_id);
  await f.accept("implement");
  delete f.sessions.get(first.session_id).outcome;
  f.sessions.get(first.session_id).time = {};
  await f.coordinator.reconcile("sample");
  assert.equal(f.store.value.jobs[0].status, "running");
  assert.equal(f.store.value.jobs[0].acceptance, "pending");
});

test("dependent task waits for acceptance rather than native completion", async () => {
  const second = task("followup");
  second.depends_on = ["implement"];
  const f = fixture([task("implement"), second]),
    first = await f.dispatch();
  await f.returned(first.session_id);
  await assert.rejects(f.dispatch("followup"), /dependency|accepted/i);
  await f.accept("implement");
  await f.dispatch("followup");
  assert.equal(f.calls.length, 2);
});

test("verification waits for all implementation tasks and uses an independent child", async () => {
  const f = fixture([
    task("implement"),
    task("second"),
    task("verify", "check", []),
  ]);
  const a = await f.dispatch();
  await f.returned(a.session_id);
  await f.accept("implement");
  await assert.rejects(f.dispatch("verify"), /implementation|integrate/i);
  const b = await f.dispatch("second");
  await f.returned(b.session_id);
  await f.accept("second");
  const review = await f.dispatch("verify");
  assert.notEqual(review.session_id, a.session_id);
  assert.notEqual(review.session_id, b.session_id);
  assert.equal(f.calls.at(-1).input.sessionID, undefined);
});

test("a native host cannot reuse an implementer session for a different verification task", async () => {
  const f = fixture([task("implement"), task("verify", "check", [])]);
  const first = await f.dispatch();
  await f.returned(first.session_id);
  await f.accept("implement");
  f.host.reuseChild = first.session_id;
  await assert.rejects(
    f.dispatch("verify"),
    /session|independent|unrelated|already/i,
  );
  assert.notEqual(f.store.value.jobs.at(-1).status, "running");
});

test("shared checkout writers are serialized while independent read-only work can proceed", async () => {
  const f = fixture([
    task("implement"),
    task("other"),
    task("inspect", "apply", []),
  ]);
  await f.dispatch();
  await assert.rejects(f.dispatch("other"), /writer|shared/i);
  await f.dispatch("inspect");
  assert.equal(f.calls.length, 2);
  await assert.rejects(f.dispatch("other"), /limit|worker/i);
});

test("permission waits, disconnected outcomes and idle unknown sessions are not accepted", async () => {
  const f = fixture(),
    first = await f.dispatch();
  f.host.pendingPermissions = new Set([first.session_id]);
  await f.coordinator.reconcile("sample");
  assert.equal(f.store.value.jobs[0].status, "waiting");
  await assert.rejects(f.accept("implement"), /returned/i);
  f.host.pendingPermissions.clear();
  f.host.unreachable = new Set([first.session_id]);
  await f.coordinator.reconcile("sample");
  assert.equal(f.store.value.jobs[0].status, "unknown");
  await assert.rejects(f.dispatch(), /active|unknown/i);
  f.host.unreachable.clear();
  f.sessions.get(first.session_id).time.idle = Date.now();
  await f.coordinator.reconcile("sample");
  assert.equal(f.store.value.jobs[0].status, "unknown");
  await f.returned(first.session_id);
  assert.equal(f.store.value.jobs[0].status, "returned");
});

test("an uncertain launch without a child ID remains non-retryable after restart", async () => {
  const f = fixture();
  f.host.omitChildID = true;
  await assert.rejects(f.dispatch(), /session|unknown/i);
  assert.equal(f.store.value.jobs[0].status, "unknown");
  const restarted = new Coordinator(f.store, f.host);
  await assert.rejects(
    restarted.delegate(
      { change_id: "sample", task_id: "implement" },
      { sessionID: parent.id },
    ),
    /active|unknown/i,
  );
  assert.equal(f.calls.length, 1);
});

test("a definite pre-launch preparation failure remains safely retryable after configuration repair", async () => {
  const f = fixture();
  f.host.onPrepare = () => {
    throw new Error("Configured native agent is unavailable");
  };
  await assert.rejects(f.dispatch(), /unavailable/i);
  assert.equal(f.calls.length, 0);
  assert.equal(f.store.value.jobs[0].status, "failed");
  f.host.onPrepare = undefined;
  await f.dispatch();
  assert.equal(f.calls.length, 1);
  assert.equal(f.store.value.jobs.at(-1).attempt, 2);
});

test("wrong native parent is preserved as unknown rather than reported as a valid worker", async () => {
  const f = fixture();
  f.host.wrongParent = "ses_unrelated";
  await assert.rejects(f.dispatch(), /native child|coordinator|parent/i);
  assert.equal(f.store.value.jobs[0].status, "unknown");
});

test("scoped correction can revisit an accepted implementation without changing the contract", async () => {
  const f = fixture(),
    first = await f.dispatch();
  await f.returned(first.session_id);
  await f.accept("implement");
  await assert.rejects(f.dispatch(), /accepted|correction|revalidation/i);
  const corrected = await f.dispatch("implement", {
    correction: true,
    instructions: "Fix the boundary case found by independent verification.",
  });
  assert.equal(corrected.session_id, first.session_id);
  assert.equal(f.calls.at(-1).input.sessionID, first.session_id);
  assert.equal(f.store.value.jobs.at(-1).attempt, 2);
  assert.equal(f.store.value.jobs.at(-1).acceptance, "pending");
  assert.equal(f.store.value.contract_digest, "contract-1");
});

test("documentation waits for all verification contributions to be accepted", async () => {
  const f = fixture([
    task("implement"),
    task("verify", "check", []),
    task("document", "docs", ["README.md"]),
  ]);
  const first = await f.dispatch();
  await f.returned(first.session_id);
  await f.accept("implement");
  const review = await f.dispatch("verify");
  await f.returned(review.session_id);
  f.store.state.phase = "checked";
  await assert.rejects(
    f.dispatch("document"),
    /check|verification|accepted|integrate/i,
  );
  await f.accept("verify");
  await f.dispatch("document");
});

test("interrupt confirms native cancellation without deleting the contribution history", async () => {
  const f = fixture(),
    first = await f.dispatch();
  await f.coordinator.interrupt("sample", "implement");
  assert.equal(f.store.value.jobs[0].session_id, first.session_id);
  assert.equal(f.store.value.jobs[0].status, "cancelled");
  assert.equal(f.store.value.jobs[0].acceptance, "pending");
});

test("an unconfirmed correction cannot inherit the previous attempt successful outcome", async () => {
  const f = fixture(),
    first = await f.dispatch();
  await f.returned(first.session_id);
  await f.accept("implement");
  f.host.spawnError = "Delivery outcome uncertain";
  await assert.rejects(
    f.dispatch("implement", { correction: true }),
    /uncertain/i,
  );
  const previousCalls = f.calls.length;
  await f.coordinator.reconcile("sample");
  const last = f.store.value.jobs.at(-1);
  assert.equal(last.session_id, first.session_id);
  assert.equal(last.launch_confirmed, false);
  assert.equal(last.status, "unknown");
  assert.equal(last.acceptance, "pending");
  await assert.rejects(
    f.dispatch("implement", { correction: true }),
    /active|unknown/i,
  );
  assert.equal(f.calls.length, previousCalls);
});

test("a confirmed stop survives restart when the correction launch was unconfirmed", async () => {
  const f = fixture(),
    first = await f.dispatch();
  await f.returned(first.session_id);
  await f.accept("implement");
  f.host.spawnError = "Delivery outcome uncertain";
  await assert.rejects(
    f.dispatch("implement", { correction: true }),
    /uncertain/i,
  );
  f.host.interrupt = async (id) => {
    const s = f.sessions.get(id);
    delete s.outcome;
    s.time = {};
    s.execution_observed = false;
    return { quiescent: true };
  };
  await f.coordinator.interrupt(
    "sample",
    "implement",
    undefined,
    "Stop the unresolved correction before proceeding.",
  );
  const stopped = f.store.value.jobs.at(-1);
  assert.equal(stopped.status, "cancelled");
  assert.equal(stopped.stop_confirmed, true);
  assert.ok(stopped.finished_at);
  assert.match(stopped.cancellation.reason, /unresolved correction/);
  await new Coordinator(f.store, f.host).reconcile("sample");
  assert.equal(f.store.value.jobs.at(-1).status, "cancelled");
  assert.equal(f.store.value.jobs.at(-1).finished_at, stopped.finished_at);
});

test("finished timestamps remain fixed on repeated polls and reset on observed resumption", async () => {
  const f = fixture(),
    first = await f.dispatch();
  const finished = Date.now() - 10000;
  f.sessions.get(first.session_id).time.idle = finished;
  await f.returned(first.session_id);
  assert.equal(f.store.value.jobs[0].finished_at, finished);
  f.sessions.get(first.session_id).time.idle = finished + 1000;
  await f.coordinator.reconcile("sample");
  assert.equal(f.store.value.jobs[0].finished_at, finished);
  delete f.sessions.get(first.session_id).outcome;
  f.sessions.get(first.session_id).time = {};
  f.sessions.get(first.session_id).execution_observed = true;
  await f.coordinator.reconcile("sample");
  assert.equal(f.store.value.jobs[0].finished_at, null);
  assert.equal(f.store.value.jobs[0].status, "running");
});

test("a failed interruption cannot record a confirmed cancellation reason", async () => {
  const f = fixture();
  await f.dispatch();
  f.host.interrupt = async () => {
    throw new Error("Native wait timed out");
  };
  await assert.rejects(
    f.coordinator.interrupt(
      "sample",
      "implement",
      undefined,
      "Stop this task.",
    ),
    /timed out/i,
  );
  assert.equal(f.store.value.jobs[0].status, "running");
  assert.equal(f.store.value.jobs[0].cancellation, undefined);
});

function otherChange(
  f,
  { status = "running", outcome, archived = false } = {},
) {
  const originalRegistry = f.store.registry.bind(f.store),
    originalMutate = f.store.mutate.bind(f.store);
  f.store.other = {
    version: 1,
    change_id: "other",
    contract_digest: "other-contract",
    jobs: [
      {
        run_id: "other-run",
        task_id: "other-task",
        attempt: 1,
        parent_id: "ses_other_parent",
        session_id: "ses_other_worker",
        launch_confirmed: true,
        status,
        acceptance: "pending",
        write_paths: ["other/source.py"],
      },
    ],
  };
  f.sessions.set("ses_other_worker", {
    id: "ses_other_worker",
    parentID: "ses_other_parent",
    model: parent.model,
    time: {},
    ...(outcome ? { outcome } : {}),
  });
  f.store.registry = (id) =>
    id === "other" ? clone(f.store.other) : originalRegistry(id);
  f.store.mutate = (id, fn) => {
    if (id !== "other") return originalMutate(id, fn);
    f.store.other = clone(fn(clone(f.store.other)));
    return clone(f.store.other);
  };
  f.store.changes = () => [
    { id: "sample", phase: f.store.state.phase, archived: false },
    { id: "other", phase: archived ? "archived" : "applying", archived },
  ];
}

test("writer and worker limits apply across changes in the same project", async () => {
  const f = fixture([
    task("implement"),
    task("inspect", "apply", []),
    task("more-inspection", "apply", []),
  ]);
  otherChange(f);
  await assert.rejects(f.dispatch(), /writer|shared/i);
  await f.dispatch("inspect");
  assert.equal(f.calls.length, 1);
  await assert.rejects(f.dispatch("more-inspection"), /worker limit/i);
});

test("a completed worker in another active change releases its slot after native reconciliation", async () => {
  const f = fixture();
  otherChange(f, { outcome: "succeeded" });
  await f.dispatch();
  assert.equal(f.store.other.jobs[0].status, "returned");
  assert.equal(f.store.other.jobs[0].acceptance, "pending");
  assert.equal(f.calls.length, 1);
});

test("unavailable archived sessions are not revived into active slots when starting another change", async () => {
  const f = fixture();
  otherChange(f, { status: "returned", outcome: "succeeded", archived: true });
  f.host.unreachable = new Set(["ses_other_worker"]);
  await f.dispatch();
  assert.equal(f.calls.length, 1);
  assert.equal(f.store.other.jobs[0].status, "returned");
});

test("child navigation preserves the actual root coordinator binding with multiple changes", async () => {
  const f = fixture(),
    first = await f.dispatch();
  otherChange(f, { status: "returned", archived: false, outcome: "succeeded" });
  f.store.bind(first.session_id, "other");
  f.sessions.get(first.session_id).model = {
    providerID: "anthropic",
    id: "worker-model",
  };
  const view = await f.coordinator.snapshot(first.session_id);
  assert.equal(view.changeID, "sample");
  assert.equal(view.coordinator.sessionID, parent.id);
  assert.equal(view.coordinator.model, "openai/main#high");
  assert.equal(view.viewOnly, true);
  assert.equal(view.settingsRevision, hash(f.store.settingsByScope()));
  await assert.rejects(
    f.coordinator.delegate(
      { change_id: "sample", task_id: "implement" },
      { sessionID: first.session_id },
    ),
    /main coordinator/i,
  );
  assert.equal((await f.coordinator.snapshot(parent.id)).viewOnly, false);
});

test("nested native child navigation resolves verified ancestors rather than guessing a binding", async () => {
  const f = fixture(),
    first = await f.dispatch();
  otherChange(f, { status: "returned", outcome: "succeeded" });
  f.sessions.set("ses_grandchild", {
    id: "ses_grandchild",
    parentID: first.session_id,
    model: parent.model,
    time: {},
  });
  const view = await f.coordinator.snapshot("ses_grandchild");
  assert.equal(view.changeID, "sample");
  assert.equal(view.coordinator.sessionID, parent.id);
  assert.equal(view.viewOnly, true);
});

test("cyclic native ancestry stays view-only and reports an actionable error", async () => {
  const f = fixture();
  otherChange(f, { status: "returned", outcome: "succeeded" });
  f.sessions.set("ses_a", { id: "ses_a", parentID: "ses_b" });
  f.sessions.set("ses_b", { id: "ses_b", parentID: "ses_a" });
  f.store.bind("ses_a", "other");
  const view = await f.coordinator.snapshot("ses_a");
  assert.match(view.error, /cycle/i);
  assert.equal(view.viewOnly, true);
  assert.equal(view.coordinator, undefined);
  assert.equal(view.changeID, undefined);
});

test("native ancestry lookup is bounded even when no root can be reached", async () => {
  const f = fixture();
  otherChange(f, { status: "returned", outcome: "succeeded" });
  for (let i = 0; i < 18; i++)
    f.sessions.set("ses_chain_" + i, {
      id: "ses_chain_" + i,
      parentID: "ses_chain_" + (i + 1),
    });
  let reads = 0;
  f.host.onSession = () => {
    reads++;
  };
  const view = await f.coordinator.snapshot("ses_chain_0");
  assert.match(view.error, /depth/i);
  assert.equal(reads, 16);
  assert.equal(view.coordinator, undefined);
  assert.equal(view.viewOnly, true);
});

function diskFixture(t) {
  const root = mkdtempSync(join(tmpdir(), "stip-plugin-test-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const project = join(root, "project with spaces");
  mkdirSync(project);
  const personal = join(root, "personal", "stipulate.json");
  const store = new ProjectStore(project, { personal });
  atomic(join(project, ".workflow/config.json"), {
    schema_version: 1,
    extensions: {
      custom: { enabled: false, path: ".workflow/extensions/custom" },
    },
    settings: { require_user_approval: true },
  });
  return { root, project, personal, store };
}

test("real settings merge preserves project metadata and refuses a stale save revision", (t) => {
  const f = diskFixture(t);
  atomic(f.personal, { orchestration: { defaults: { effort: "low" } } });
  atomic(join(f.project, ".workflow/local.json"), {
    orchestration: {
      clients: { opencode: { roles: { backend: { effort: "high" } } } },
    },
  });
  const revision = hash(f.store.settingsByScope());
  f.store.saveSettings(
    { defaults: { max_workers: 2, max_shared_writers: 1 } },
    "project",
    revision,
  );
  const config = JSON.parse(
    readFileSync(join(f.project, ".workflow/config.json"), "utf8"),
  );
  assert.equal(config.extensions.custom.enabled, false);
  assert.equal(config.schema_version, 1);
  assert.equal(
    f.store.settings().clients.opencode.roles.backend.effort,
    "high",
  );
  assert.equal(f.store.settings().defaults.effort, "low");
  const before = readFileSync(join(f.project, ".workflow/config.json"));
  assert.throws(
    () => f.store.saveSettings({}, "project", revision),
    /changed|reload/i,
  );
  assert.deepEqual(
    readFileSync(join(f.project, ".workflow/config.json")),
    before,
  );
});

test("real managed state rejects traversal and symlink ancestors without modifying the target", (t) => {
  const f = diskFixture(t),
    outside = join(f.root, "outside");
  mkdirSync(outside);
  writeFileSync(join(outside, "sentinel"), "keep");
  assert.throws(() => safe(f.project, "../outside/sentinel"), /escapes/i);
  symlinkSync(outside, join(f.project, ".workflow/.runtime"));
  assert.throws(
    () => f.store.mutate("sample", () => ({ version: 1, jobs: [] })),
    /symlink/i,
  );
  assert.equal(readFileSync(join(outside, "sentinel"), "utf8"), "keep");
  assert.equal(existsSync(join(outside, "sample/state.json")), false);
  assert.throws(
    () => f.store.readArtifact("../outside", "spec"),
    /identifier/i,
  );
});

test("personal settings cannot be saved through a symlinked parent directory", (t) => {
  const f = diskFixture(t),
    outside = join(f.root, "outside");
  mkdirSync(outside);
  mkdirSync(dirname(f.personal), { recursive: true });
  const linked = join(f.root, "linked-personal");
  symlinkSync(outside, linked);
  const store = new ProjectStore(f.project, {
    personal: join(linked, "stipulate.json"),
  });
  const revision = hash(store.settingsByScope());
  assert.throws(
    () =>
      store.saveSettings({ defaults: { effort: "low" } }, "personal", revision),
    /symlink|physical|managed/i,
  );
  assert.equal(existsSync(join(outside, "stipulate.json")), false);
});

test("existing workflow lock excludes registry writes and remains owned by its original writer", (t) => {
  const f = diskFixture(t),
    lock = join(f.project, ".workflow/.lock");
  writeFileSync(lock, "another-writer");
  assert.throws(
    () => f.store.mutate("sample", () => ({ version: 1, jobs: [] })),
    /locked/i,
  );
  assert.equal(readFileSync(lock, "utf8"), "another-writer");
  assert.equal(f.store.registry("sample"), null);
});

test("real store consumes the bundled schema-v2 engine and compact diagnostics", (t) => {
  const f = diskFixture(t);
  f.store.engineCommand(["explore", "sample"]);
  writeFileSync(
    join(f.project, ".workflow/changes/sample/spec.md"),
    "- AC-1: Observable behavior.\n",
  );
  const planPath = join(f.root, "plan.json");
  atomic(planPath, { version: 1, tasks: [task("implement")] });
  f.store.engineCommand(["plan", "sample", "--file", planPath]);
  const state = f.store.status("sample");
  assert.equal(state.schema_version, 2);
  assert.equal(state.phase, "draft");
  assert.equal(state.approval_current, false);
  assert.equal("baseline" in state, false);
  assert.equal(f.store.plan("sample").tasks[0].id, "implement");
  atomic(join(f.project, ".workflow/.runtime/sample/state.json"), {
    version: 99,
    change_id: "sample",
    jobs: [],
  });
  assert.match(f.store.status("sample").runtime.error, /invalid/i);
});

test("unknown research helpers consume capacity before contract workers can launch", async () => {
  const f = fixture();
  f.store.preferences.defaults.max_workers = 1;
  f.store.helperValue.jobs.push({
    kind: "helper",
    helper_id: "helper-pending",
    run_id: "helper-pending",
    task_id: "helper-pending",
    role: "research",
    phase: "explore",
    parent_id: parent.id,
    session_id: null,
    status: "starting",
    launch_confirmed: false,
    write_paths: [],
    updated_at: "original",
  });
  await assert.rejects(f.dispatch(), /Worker limit/);
  assert.equal(f.calls.length, 0);
  assert.equal(f.store.value, null);
  assert.equal(f.store.helpers().jobs[0].status, "unknown");
});

test("returned research releases capacity without requiring contract contribution acceptance", async () => {
  const f = fixture();
  f.store.preferences.defaults.max_workers = 1;
  f.sessions.set("ses_research", {
    id: "ses_research",
    parentID: parent.id,
    model: parent.model,
    outcome: "succeeded",
  });
  f.store.helperValue.jobs.push({
    kind: "helper",
    helper_id: "helper-returning",
    run_id: "helper-returning",
    task_id: "helper-returning",
    role: "research",
    phase: "validate",
    parent_id: parent.id,
    session_id: "ses_research",
    status: "running",
    launch_confirmed: true,
    write_paths: [],
    updated_at: "original",
  });
  const result = await f.dispatch();
  assert.equal(result.status, "running");
  assert.equal(f.store.helpers().jobs[0].status, "returned");
  assert.equal(f.store.helpers().jobs[0].acceptance, undefined);
  assert.deepEqual(
    f.store.value.jobs.map((j) => j.task_id),
    ["implement"],
  );
});
