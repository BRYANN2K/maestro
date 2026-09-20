import { randomUUID } from "node:crypto";
import { hash } from "./store.mjs";
import { invariant, resolveProfile, refText, slug } from "./routing.mjs";

const ACTIVE = new Set([
  "starting",
  "running",
  "waiting",
  "unknown",
  "correction",
]);
const contractJobs = (store) =>
  store.changes().flatMap((c) => store.registry(c.id)?.jobs || []);
const publicResult = (job, reused = false) => ({
  helper_id: job.helper_id,
  kind: "helper",
  change_id: job.change_id,
  role: job.role,
  phase: job.phase,
  session_id: job.session_id,
  profile: job.profile,
  status: job.status,
  ...(job.error ? { error: job.error } : {}),
  reused,
});

export async function reconcileHelpers({ store, host }) {
  const registry = store.helpers();
  const updates = await Promise.all(
    registry.jobs.map(async (job) => {
      if (job.launch_confirmed === false)
        return {
          ...job,
          status: "unknown",
          error: "Native launch has not been confirmed; do not redispatch.",
        };
      if (!job.session_id) return job;
      try {
        const info = await host.session(job.session_id);
        invariant(
          info.id === job.session_id && info.parentID === job.parent_id,
          "Native helper identity or parent does not match",
        );
        if (
          !ACTIVE.has(job.status) &&
          !info.outcome &&
          info.execution_observed !== true
        )
          return job;
        const waiting = await host.waiting(job.session_id);
        const resumed = !info.outcome && !info.time?.idle;
        return {
          ...job,
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
          finished_at: resumed
            ? null
            : job.finished_at ||
              (info.outcome
                ? info.time?.idle || new Date().toISOString()
                : null),
          updated_at: new Date().toISOString(),
          error: null,
        };
      } catch (error) {
        return ACTIVE.has(job.status)
          ? { ...job, status: "unknown", error: error.message }
          : job;
      }
    }),
  );
  if (!updates.length) return registry;
  return store.mutateHelpers((current) => {
    for (const update of updates) {
      const index = current.jobs.findIndex(
        (job) => job.helper_id === update.helper_id,
      );
      // A concurrent native dispatch confirmation must not be replaced by an older observation.
      const observed = registry.jobs.find(
        (job) => job.helper_id === update.helper_id,
      );
      if (
        index >= 0 &&
        current.jobs[index].updated_at === observed.updated_at &&
        current.jobs[index].session_id === observed.session_id &&
        current.jobs[index].launch_confirmed === observed.launch_confirmed
      )
        current.jobs[index] = update;
    }
    return current;
  });
}

export async function research(
  coordinator,
  {
    question,
    role = "research",
    phase = "explore",
    change_id: changeID,
    background = true,
  },
  context,
) {
  const { store, host } = coordinator;
  invariant(
    typeof question === "string" &&
      question.trim().length > 0 &&
      question.length <= 8000,
    "A bounded research question must contain 1 to 8000 characters",
  );
  invariant(
    ["explore", "validate"].includes(phase),
    "Research helpers support explore or validate only",
  );
  slug(role);
  invariant(
    !["coordinator", "discovery"].includes(role),
    "Discovery stays with the main coordinator",
  );
  invariant(typeof background === "boolean", "Background must be a boolean");
  invariant(
    typeof context.id === "string" &&
      context.id.length > 0 &&
      context.id.length <= 512,
    "A native tool call identifier is required for durable helper dispatch",
  );
  if (changeID !== undefined) {
    slug(changeID);
    invariant(
      store.changes().some((c) => c.id === changeID && !c.archived),
      "Research context must name an existing active change",
    );
  }
  const parent = await host.session(context.sessionID);
  invariant(
    parent.id === context.sessionID && !parent.parentID,
    "Only the main coordinator can request research helpers",
  );
  const questionText = question.trim();
  const requestKey = hash([
    parent.id,
    changeID || null,
    role,
    phase,
    questionText,
  ]);
  const callKey = hash([parent.id, context.id]);
  await coordinator.reconcileHelpers();
  const settings = store.settings();
  const profile = resolveProfile(
    settings,
    role,
    phase,
    parent.model,
    await host.models(),
  );
  for (const change of store.changes().filter((c) => !c.archived))
    await coordinator.reconcile(change.id);
  let job,
    reused = false;
  store.mutateHelpers((registry) => {
    const previous = registry.jobs.find((j) => j.call_key === callKey);
    invariant(
      !previous || previous.request_key === requestKey,
      "This native tool call already belongs to a different research question",
    );
    const existing =
      previous ||
      registry.jobs.find(
        (j) => j.request_key === requestKey && ACTIVE.has(j.status),
      );
    if (existing) {
      job = existing;
      reused = true;
      return registry;
    }
    const active = [...contractJobs(store), ...registry.jobs].filter((j) =>
      ACTIVE.has(j.status),
    );
    invariant(
      active.length < (settings.defaults?.max_workers ?? 2),
      "Worker limit reached",
    );
    const helperID = `helper-${randomUUID()}`;
    job = {
      kind: "helper",
      helper_id: helperID,
      run_id: helperID,
      task_id: helperID,
      call_key: callKey,
      request_key: requestKey,
      change_id: changeID || null,
      role,
      phase,
      objective: questionText,
      write_paths: [],
      read_paths: [],
      criteria: [],
      parent_id: parent.id,
      session_id: null,
      agent_id: `stip-helper-${hash(helperID).slice(0, 16)}`,
      attempt: 1,
      launch_confirmed: false,
      status: "starting",
      profile,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    registry.jobs.push(job);
    return registry;
  });
  if (reused) return publicResult(job, true);
  const prompt = [
    `You are a read-only ${role} research helper supporting the main Stipulate coordinator during ${phase}.`,
    "Discovery, product decisions, specification approval and implementation stay with the main coordinator.",
    "Answer only the bounded question below. Do not delegate or invoke Stipulate lifecycle tools. Do not create, edit or delete any project files, including through shell commands or scripts. Your write ownership is empty. Do not implement proposed solutions.",
    changeID
      ? `Optional project context is in .workflow/changes/${changeID}/proposal.md and spec.md. These documents may be drafts; they do not authorize implementation.`
      : "Read existing project context only as needed to answer the question.",
    "Return a concise answer with inspectable sources or file references, distinguish observations from assumptions, and state unresolved questions. Send findings to the coordinator; do not claim contract acceptance.",
    `Question:\n${questionText}`,
  ].join("\n\n");
  let launched = false;
  try {
    await host.prepare(job);
    launched = true;
    const result = await host.spawn(
      {
        agent: job.agent_id,
        description: `${role}: ${questionText}`.slice(0, 100),
        prompt,
        background,
      },
      context,
    );
    const childID = host.childID(result);
    invariant(
      childID,
      "Native helper did not return a session identifier; launch state is unknown",
    );
    invariant(
      ![...contractJobs(store), ...store.helpers().jobs].some(
        (j) => j.session_id === childID,
      ),
      "Native child is already assigned to another worker",
    );
    const child = await host.session(childID);
    invariant(
      child.id === childID && child.parentID === parent.id,
      "Maestro did not create a native child of this coordinator",
    );
    store.mutateHelpers((registry) => {
      const current = registry.jobs.find((j) => j.helper_id === job.helper_id);
      Object.assign(current, {
        session_id: childID,
        launch_confirmed: true,
        status: "running",
        actual_model: refText(child.model),
        updated_at: new Date().toISOString(),
      });
      return registry;
    });
    const current = (await coordinator.reconcileHelpers()).jobs.find(
      (j) => j.helper_id === job.helper_id,
    );
    // Native foreground output is returned to the coordinator, never copied into project state.
    return {
      ...publicResult(current),
      ...(background ? {} : { native_result: result }),
    };
  } catch (error) {
    store.mutateHelpers((registry) => {
      const current = registry.jobs.find((j) => j.helper_id === job.helper_id);
      current.status = launched ? "unknown" : "failed";
      current.launch_confirmed = !launched;
      current.error = error.message;
      current.updated_at = new Date().toISOString();
      return registry;
    });
    throw error;
  }
}
