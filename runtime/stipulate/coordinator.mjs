import { randomUUID } from "node:crypto";
import {
  invariant,
  resolveProfile,
  capabilities,
  cycle,
  refText,
} from "./routing.mjs";
import { hash, atomic, json } from "./store.mjs";
import { research, reconcileHelpers } from "./helpers.mjs";
const ACTIVE = new Set([
  "starting",
  "running",
  "waiting",
  "unknown",
  "correction",
]);
export const latest = (jobs, id) =>
  jobs.filter((j) => j.task_id === id).sort((a, b) => b.attempt - a.attempt)[0];
export class Coordinator {
  constructor(store, host) {
    this.store = store;
    this.host = host;
  }
  async research(input, context) {
    return research(this, input, context);
  }
  async reconcileHelpers() {
    return reconcileHelpers(this);
  }
  async reconcile(id) {
    const r = this.store.registry(id);
    if (!r) return null;
    const updates = await Promise.all(
      r.jobs
        .filter(
          (j) =>
            latest(r.jobs, j.task_id)?.run_id === j.run_id &&
            (j.session_id || ACTIVE.has(j.status)),
        )
        .map(async (j) => {
          if (j.launch_confirmed === false && !j.stop_confirmed)
            return {
              ...j,
              status: "unknown",
              error: "Native launch has not been confirmed; do not redispatch.",
            };
          if (!j.session_id)
            return {
              ...j,
              status: "unknown",
              error:
                "Launch outcome unknown; reconcile the native session before retrying.",
            };
          try {
            const info = await this.host.session(j.session_id);
            if (j.stop_confirmed && info.execution_observed !== true) return j;
            invariant(
              info.parentID === j.parent_id,
              "Native parent does not match this task",
            );
            const waiting = await this.host.waiting(j.session_id);
            const resumed = !!j.session_id && !info.outcome && !info.time?.idle;
            return {
              ...j,
              stop_confirmed: resumed ? false : j.stop_confirmed,
              finished_at: resumed
                ? null
                : j.finished_at ||
                  (info.outcome
                    ? info.time?.idle || new Date().toISOString()
                    : null),
              acceptance: resumed ? "pending" : j.acceptance,
              status: waiting
                ? "waiting"
                : info.outcome === "succeeded"
                  ? "returned"
                  : info.outcome === "failed"
                    ? "failed"
                    : info.outcome === "interrupted"
                      ? "cancelled"
                      : info.time?.idle || info.execution_observed === false
                        ? "unknown"
                        : "running",
              native_outcome: info.outcome,
              actual_model: refText(info.model),
              updated_at: new Date().toISOString(),
            };
          } catch (e) {
            return { ...j, status: "unknown", error: e.message };
          }
        }),
    );
    if (!updates.length) return r;
    return this.store.mutate(id, (current) => {
      invariant(
        current?.contract_digest === r.contract_digest,
        "Contract changed during reconciliation",
      );
      for (const j of updates) {
        const n = current.jobs.findIndex((x) => x.run_id === j.run_id);
        if (n >= 0 && latest(current.jobs, j.task_id)?.run_id === j.run_id)
          current.jobs[n] = {
            ...current.jobs[n],
            ...j,
            acceptance: ["running", "waiting", "unknown"].includes(j.status)
              ? "pending"
              : current.jobs[n].acceptance,
            review: current.jobs[n].review,
          };
      }
      return current;
    });
  }
  async snapshot(parentID, id) {
    const settings = this.store.settings();
    const models = await this.host.models();
    const changes = this.store.changes();
    const requestedSessionID = parentID;
    let coordinator, sessionError;
    if (parentID)
      try {
        const seen = new Set();
        for (let depth = 0; depth < 16; depth++) {
          invariant(
            !seen.has(parentID),
            "Native session ancestry contains a cycle",
          );
          seen.add(parentID);
          const current = await this.host.session(parentID);
          invariant(
            current.id === parentID,
            "Native session identity does not match the requested session",
          );
          if (!current.parentID) {
            coordinator = current;
            break;
          }
          parentID = current.parentID;
        }
        invariant(
          coordinator,
          "Native session ancestry exceeds the supported depth",
        );
      } catch (e) {
        sessionError = e.message;
      }
    id = id || (coordinator ? this.store.binding(coordinator.id) : undefined);
    // Automatic selection is safe only when one active change exists. Dispatch still binds explicitly.
    if (!id) {
      const active = changes.filter((c) => !c.archived);
      if (active.length === 1) id = active[0].id;
    }
    const settingsByScope = this.store.settingsByScope();
    const helpers = (await this.reconcileHelpers()).jobs.filter((j) =>
      coordinator ? j.parent_id === coordinator.id : !requestedSessionID,
    );
    const base = {
      available: changes.length > 0,
      changes,
      settings,
      settingsByScope,
      settingsRevision: hash(settingsByScope),
      catalog: capabilities(models),
      jobs: [],
      helpers,
      extensions: [],
      cycle: cycle(null),
      viewOnly: !coordinator || requestedSessionID !== coordinator.id,
      ...(sessionError ? { error: sessionError } : {}),
    };
    if (coordinator)
      base.coordinator = {
        sessionID: coordinator.id,
        model: refText(coordinator.model),
        agent: coordinator.agent,
      };
    if (!id) return base;
    try {
      const state = this.store.status(id);
      const registry = await this.reconcile(id);
      return {
        ...base,
        changeID: id,
        phase: state.phase,
        tasks: state.schema_version === 2 ? this.store.plan(id).tasks : [],
        cycle: cycle(state),
        extensions: state.extensions || [],
        jobs: registry?.jobs || [],
        error: state.runtime?.error || sessionError,
      };
    } catch (e) {
      return { ...base, changeID: id, error: e.message };
    }
  }
  async delegate(
    {
      change_id: id,
      task_id: taskID,
      instructions = "",
      correction = false,
      background = true,
    },
    context,
  ) {
    const parent = await this.host.session(context.sessionID);
    invariant(
      !parent.parentID,
      "Only the main coordinator can dispatch Stip tasks",
    );
    const state = this.store.status(id);
    invariant(
      state.schema_version === 2,
      "Install and approve an execution plan before delegation",
    );
    invariant(
      !state.runtime?.error,
      state.runtime?.error || "Invalid registry",
    );
    invariant(
      state.approval_current,
      "The current specification and execution plan need user approval",
    );
    const plan = this.store.plan(id);
    const task = plan.tasks.find((t) => t.id === taskID);
    invariant(task, "Task is not in the approved execution plan");
    invariant(
      task.phase === "docs"
        ? ["checked", "documented"].includes(state.phase)
        : state.phase === "applying",
      "Task is not eligible in the current lifecycle phase",
    );
    const settings = this.store.settings(),
      models = await this.host.models();
    const profile = resolveProfile(
      settings,
      task.role,
      task.phase,
      parent.model,
      models,
    );
    await this.reconcile(id);
    await this.reconcileHelpers();
    for (const other of this.store
      .changes()
      .filter((c) => c.id !== id && !c.archived))
      await this.reconcile(other.id);
    const runID = randomUUID();
    let job;
    this.store.mutate(id, (r) => {
      const fresh = this.store.status(id);
      invariant(
        fresh.approval_current &&
          fresh.contract_digest === state.contract_digest,
        "The contract changed before dispatch",
      );
      invariant(
        task.phase === "docs"
          ? ["checked", "documented"].includes(fresh.phase)
          : fresh.phase === "applying",
        "Lifecycle phase changed before dispatch",
      );
      r = r || {
        version: 1,
        change_id: id,
        contract_digest: state.contract_digest,
        jobs: [],
      };
      invariant(
        r.contract_digest === state.contract_digest,
        "Registry belongs to another contract; reconcile it first",
      );
      for (const dependency of task.depends_on)
        invariant(
          latest(r.jobs, dependency)?.acceptance === "accepted",
          `Dependency ${dependency} has not been accepted`,
        );
      if (task.phase !== "apply")
        for (const t of plan.tasks.filter((t) => t.phase === "apply"))
          invariant(
            latest(r.jobs, t.id)?.acceptance === "accepted",
            "Integrate all implementation work before verification or documentation",
          );
      if (task.phase === "docs")
        for (const t of plan.tasks.filter((t) => t.phase === "check"))
          invariant(
            latest(r.jobs, t.id)?.acceptance === "accepted",
            "Accept every verification contribution before documentation",
          );
      const previous = latest(r.jobs, taskID);
      invariant(
        !previous || !ACTIVE.has(previous.status),
        "This task already has an active or unknown native run",
      );
      invariant(
        !previous || previous.acceptance !== "accepted" || correction,
        "Use correction to resume an accepted task within its approved contract",
      );
      if (correction) {
        const affected = new Set([taskID]);
        let grew = true;
        while (grew) {
          grew = false;
          for (const t of plan.tasks)
            if (
              !affected.has(t.id) &&
              t.depends_on.some((d) => affected.has(d))
            ) {
              affected.add(t.id);
              grew = true;
            }
        }
        for (const j of r.jobs)
          if (affected.has(j.task_id)) {
            invariant(
              !ACTIVE.has(j.status),
              "A dependent task is still active",
            );
            j.acceptance = "rejected";
          }
      }
      invariant(
        !correction || previous?.session_id,
        "A correction needs an existing native worker session",
      );
      const other = this.store
        .changes()
        .filter((c) => c.id !== id)
        .flatMap((c) => this.store.registry(c.id)?.jobs || []);
      const active = [...r.jobs, ...other, ...this.store.helpers().jobs].filter(
        (j) => ACTIVE.has(j.status),
      );
      invariant(
        active.length < (settings.defaults?.max_workers ?? 2),
        "Worker limit reached",
      );
      // Shared-checkout writes are serialized even when a larger user limit is set.
      invariant(
        !task.write_paths.length || !active.some((j) => j.write_paths?.length),
        "Another worker owns the shared checkout writer slot",
      );
      job = {
        run_id: runID,
        task_id: task.id,
        role: task.role,
        phase: task.phase,
        objective: task.objective,
        write_paths: task.write_paths,
        read_paths: task.read_paths || [],
        criteria: task.criteria,
        parent_id: parent.id,
        session_id: correction ? previous.session_id : null,
        agent_id: `stip-${hash([id, task.id]).slice(0, 16)}`,
        attempt: (previous?.attempt || 0) + 1,
        launch_confirmed: false,
        status: "starting",
        acceptance: "pending",
        profile,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      };
      r.jobs.push(job);
      return r;
    });
    this.store.bind(parent.id, id);
    const prompt = [
      `You are the ${task.role} worker for Stipulate change ${id}, task ${task.id}.`,
      `The main agent coordinates this approved contract. You are not alone; preserve unrelated edits. Do not delegate or approve/check/archive the workflow.`,
      `Approved objective: ${task.objective}`,
      `Acceptance criteria: ${task.criteria.join(", ")}`,
      `Read the current contract at .workflow/changes/${id}/spec.md and proposal.md.`,
      `Write ownership: ${task.write_paths.length ? task.write_paths.join(", ") : "READ ONLY; report findings to the coordinator."}`,
      `Read context: ${(task.read_paths || []).join(", ")}`,
      task.expected_output ||
        "Return changed paths, evidence, commands and results, remaining risks. Do not claim acceptance.",
      `Stay within the approved objective. Additional coordinator context follows:\n${instructions}`,
    ].join("\n\n");
    let launched = false;
    try {
      await this.host.prepare(job);
      launched = true;
      const result = await this.host.spawn(
        {
          agent: job.agent_id,
          description: `${task.role}: ${task.id}`.slice(0, 100),
          prompt,
          background,
          ...(job.session_id ? { sessionID: job.session_id } : {}),
        },
        context,
      );
      const childID = this.host.childID(result);
      invariant(
        childID,
        "Native subagent did not return a session identifier; launch state is unknown",
      );
      invariant(
        !this.store
          .registry(id)
          .jobs.some((j) => j.task_id !== taskID && j.session_id === childID),
        "Native child is already assigned to an unrelated task",
      );
      const child = await this.host.session(childID);
      invariant(
        child.parentID === parent.id,
        "Maestro did not create a native child of this coordinator",
      );
      this.store.mutate(id, (r) => {
        const j = r.jobs.find((j) => j.run_id === runID);
        Object.assign(j, {
          session_id: childID,
          launch_confirmed: true,
          status: "running",
          actual_model: refText(child.model),
          updated_at: new Date().toISOString(),
        });
        return r;
      });
      return {
        change_id: id,
        task_id: taskID,
        session_id: childID,
        profile,
        status: "running",
        acceptance: "pending",
      };
    } catch (e) {
      this.store.mutate(id, (r) => {
        const j = r.jobs.find((j) => j.run_id === runID);
        j.status = launched ? "unknown" : "failed";
        j.launch_confirmed = !launched;
        j.error = e.message;
        return r;
      });
      throw e;
    }
  }
  async contribution(id, taskID, decision, reason, runID) {
    invariant(
      ["accepted", "rejected"].includes(decision),
      "Invalid contribution decision",
    );
    invariant(
      typeof reason === "string" && reason.trim().length >= 8,
      "Record the integration/verification reason",
    );
    const state = this.store.status(id);
    invariant(state.approval_current, "Contract approval is stale");
    await this.reconcile(id);
    const r = this.store.mutate(id, (r) => {
      const fresh = this.store.status(id);
      invariant(
        fresh.approval_current &&
          fresh.contract_digest === state.contract_digest,
        "Contract changed during contribution review",
      );
      invariant(
        r?.contract_digest === state.contract_digest,
        "Registry contract mismatch",
      );
      const j = latest(r.jobs, taskID);
      invariant(
        !runID || j?.run_id === runID,
        "The selected worker attempt changed; refresh before reviewing",
      );
      invariant(
        j && j.status === "returned",
        "Only a returned native contribution can be reviewed",
      );
      j.acceptance = decision;
      j.review = { reason, at: new Date().toISOString() };
      return r;
    });
    return { task_id: taskID, acceptance: decision, jobs: r.jobs };
  }
  async interrupt(id, taskID, runID, reason) {
    invariant(
      reason === undefined ||
        (typeof reason === "string" &&
          reason.trim().length > 0 &&
          reason.length <= 2000),
      "Cancellation reason must contain 1 to 2000 characters",
    );
    const j = latest(this.store.registry(id)?.jobs || [], taskID);
    invariant(!runID || j?.run_id === runID, "The selected attempt changed");
    invariant(j?.session_id, "No native session to interrupt");
    const stopped = await this.host.interrupt(j.session_id);
    if (stopped?.quiescent)
      this.store.mutate(id, (r) => {
        for (const task of r.jobs)
          if (task.session_id === j.session_id && ACTIVE.has(task.status)) {
            task.status = "cancelled";
            task.acceptance = "pending";
            task.stop_confirmed = true;
            task.finished_at = task.finished_at || new Date().toISOString();
            task.updated_at = new Date().toISOString();
          }
        return r;
      });
    else await this.reconcile(id);
    this.store.mutate(id, (r) => {
      const current = latest(r.jobs, taskID);
      if (current?.run_id === j.run_id && current.status === "cancelled")
        current.cancellation = {
          reason: reason?.trim() || null,
          at: new Date().toISOString(),
        };
      return r;
    });
    return { session_id: j.session_id };
  }
  async installPlan(id, plan, migrate = false) {
    const path = this.store.path(
      `.workflow/.runtime/plan-${randomUUID()}.json`,
    );
    atomic(path, plan);
    try {
      return this.store.engineCommand([
        "plan",
        id,
        "--file",
        path,
        ...(migrate ? ["--migrate"] : []),
      ]);
    } finally {
      const { unlinkSync } = await import("node:fs");
      unlinkSync(path);
    }
  }
}
