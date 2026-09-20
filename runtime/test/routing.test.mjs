import test from "node:test";
import assert from "node:assert/strict";
import {
  assignment,
  capabilities,
  cycle,
  merge,
  parseRef,
  resolveProfile,
  validateSettings,
} from "../stipulate/routing.mjs";

const parent = { providerID: "openai", id: "main", variant: "high" };
const models = [
  {
    providerID: "openai",
    id: "main",
    name: "Main",
    capabilities: { tools: true },
    settings: { reasoningEffort: "low" },
    variants: [
      { id: "high", settings: { reasoningEffort: "high" } },
      {
        id: "high-priority",
        settings: { reasoningEffort: "high", serviceTier: "priority" },
      },
      { id: "fast-label-only", settings: { reasoningEffort: "low" } },
    ],
  },
  {
    providerID: "anthropic",
    id: "other",
    capabilities: { tools: true },
    body: { output_config: { effort: "medium" } },
    variants: [{ id: "careful", body: { output_config: { effort: "high" } } }],
  },
];

test("Maestro's serialized default variant inherits base settings and supports effort overrides", () => {
  const nativeParent = { providerID: "openai", id: "main", variant: "default" };
  const inherited = resolveProfile(
    {},
    "research",
    "explore",
    nativeParent,
    models,
  );
  assert.equal(inherited.effort, "low");
  assert.equal(inherited.model.variant, undefined);
  const adjusted = resolveProfile(
    { clients: { opencode: { phases: { explore: { effort: "high" } } } } },
    "research",
    "explore",
    nativeParent,
    models,
  );
  assert.equal(adjusted.model.variant, "high");
  assert.throws(
    () =>
      resolveProfile(
        {},
        "research",
        "explore",
        { ...nativeParent, variant: "missing" },
        models,
      ),
    /variant is unavailable/,
  );
});

test("unassigned worker inherits coordinator model and effective variant", () => {
  const profile = resolveProfile({}, "backend", "apply", parent, models);
  assert.deepEqual(profile.model, parent);
  assert.equal(profile.effort, "high");
  assert.equal(profile.fast, false);
  assert.equal(profile.requested.model, "inherit");
});

test("phase-role overrides role, phase and defaults, while task overrides remain most specific", () => {
  const settings = {
    defaults: { effort: "low" },
    clients: {
      opencode: {
        phases: { check: { effort: "medium" } },
        roles: { security: { effort: "high" } },
        phase_roles: { check: { security: { effort: "low" } } },
      },
    },
  };
  const phaseRole = assignment(settings, "security", "check");
  assert.equal(phaseRole.value.effort, "low");
  assert.equal(phaseRole.sources.effort, "phase-role");
  assert.equal(
    assignment(settings, "security", "check", { effort: "high" }).sources
      .effort,
    "task",
  );
});

test("switching models resets inherited lower-level effort and speed", () => {
  const settings = {
    defaults: {
      model: "openai/main#high-priority",
      effort: "high",
      fast: true,
    },
    clients: {
      opencode: { roles: { documentation: { model: "anthropic/other" } } },
    },
  };
  const profile = resolveProfile(
    settings,
    "documentation",
    "docs",
    parent,
    models,
  );
  assert.deepEqual(profile.model, { providerID: "anthropic", id: "other" });
  assert.equal(profile.effort, "medium");
  assert.equal(profile.fast, false);
  assert.equal(profile.requested.effort, "inherit");
  assert.equal(profile.requested.fast, "inherit");
});

test("effort and priority speed are independent model-dependent settings", () => {
  const settings = {
    clients: {
      opencode: { roles: { backend: { effort: "high", fast: true } } },
    },
  };
  const profile = resolveProfile(settings, "backend", "apply", parent, models);
  assert.equal(profile.model.variant, "high-priority");
  assert.equal(profile.effort, "high");
  assert.equal(profile.fast, true);
  settings.clients.opencode.roles.backend.fast = false;
  assert.equal(
    resolveProfile(settings, "backend", "apply", parent, models).model.variant,
    "high",
  );
});

test("a variant label does not prove Fast capability", () => {
  const model = structuredClone(models[0]);
  model.variants = [model.variants.find((v) => v.id === "fast-label-only")];
  assert.equal(capabilities([model])[0].fastSupported, false);
  assert.throws(
    () =>
      resolveProfile(
        { defaults: { fast: true } },
        "backend",
        "apply",
        { providerID: "openai", id: "main" },
        [model],
      ),
    /effort|Fast|combination/i,
  );
});

test("a variant named high with low effort cannot satisfy requested high effort", () => {
  const deceptive = {
    providerID: "openai",
    id: "deceptive",
    settings: { reasoningEffort: "low" },
    variants: [{ id: "high", settings: { reasoningEffort: "low" } }],
  };
  assert.throws(
    () =>
      resolveProfile(
        { defaults: { effort: "high" } },
        "backend",
        "apply",
        { providerID: "openai", id: "deceptive" },
        [deceptive],
      ),
    /effort|combination/i,
  );
});

test("unsupported combinations and missing models fail instead of silently inheriting", () => {
  assert.throws(
    () =>
      resolveProfile(
        { defaults: { model: "anthropic/other", fast: true } },
        "backend",
        "apply",
        parent,
        models,
      ),
    /Fast|combination/i,
  );
  assert.throws(
    () =>
      resolveProfile(
        { defaults: { model: "openai/missing" } },
        "backend",
        "apply",
        parent,
        models,
      ),
    /unavailable/i,
  );
  assert.throws(
    () =>
      resolveProfile(
        { defaults: { model: "openai/main#missing" } },
        "backend",
        "apply",
        parent,
        models,
      ),
    /variant/i,
  );
  assert.throws(
    () => resolveProfile({}, "backend", "apply", null, models),
    /coordinator/i,
  );
});

test("only explicitly configured available same-host fallbacks are selected", () => {
  const settings = {
    clients: {
      opencode: {
        roles: {
          backend: {
            model: "openai/unavailable",
            fallbacks: ["openai/also-missing", "anthropic/other"],
          },
        },
      },
    },
  };
  const profile = resolveProfile(settings, "backend", "apply", parent, models);
  assert.deepEqual(profile.model, { providerID: "anthropic", id: "other" });
  assert.equal(profile.requested.model, "openai/unavailable");
  assert.ok(profile.fallback, "The fallback choice must remain inspectable.");
});

test("disabled and non-tool models are excluded from the menu and cannot execute workers", () => {
  const values = [
    ...models,
    { providerID: "x", id: "disabled", enabled: false },
    { providerID: "x", id: "no-tools", capabilities: { tools: false } },
  ];
  assert.deepEqual(
    capabilities(values).map((x) => x.ref),
    ["openai/main", "anthropic/other"],
  );
  assert.throws(
    () =>
      resolveProfile(
        { defaults: { model: "x/disabled" } },
        "backend",
        "apply",
        parent,
        values,
      ),
    /unavailable/i,
  );
  assert.throws(
    () =>
      resolveProfile(
        { defaults: { model: "x/no-tools" } },
        "backend",
        "apply",
        parent,
        values,
      ),
    /tools/i,
  );
});

test("settings reject reserved coordinator roles and unsupported shared writers", () => {
  for (const role of ["discovery", "coordinator"]) {
    assert.throws(
      () =>
        validateSettings({ clients: { opencode: { roles: { [role]: {} } } } }),
      /coordinator|discovery/i,
    );
  }
  assert.throws(
    () =>
      validateSettings({ defaults: { max_workers: 3, max_shared_writers: 2 } }),
    /writer|shared/i,
  );
});

test("settings merge preserves other hosts, respects local precedence and rejects prototype keys", () => {
  const personal = {
    defaults: { model: "inherit", effort: "low" },
    clients: { codex: { roles: { backend: { model: "astra" } } } },
  };
  const project = {
    defaults: { max_workers: 2 },
    clients: { opencode: { roles: { backend: { effort: "high" } } } },
  };
  const local = {
    clients: { opencode: { roles: { backend: { effort: "low" } } } },
  };
  const value = merge(merge(personal, project), local);
  assert.equal(value.clients.opencode.roles.backend.effort, "low");
  assert.equal(value.clients.codex.roles.backend.model, "astra");
  assert.equal(value.defaults.max_workers, 2);
  assert.equal(project.clients.opencode.roles.backend.effort, "high");
  assert.throws(
    () => merge({}, JSON.parse('{"__proto__":{"unsafe":true}}')),
    /unsafe/i,
  );
});

test("model refs require an explicit Maestro provider and a complete optional variant", () => {
  assert.deepEqual(parseRef("openai/main#high"), parent);
  for (const value of [
    "main",
    "/main",
    "openai/",
    "openai/main#",
    "openai/main#high#extra",
  ]) {
    assert.throws(() => parseRef(value));
  }
});

test("stale compact approval does not leave implementation and checks marked complete", () => {
  const view = cycle({
    phase: "checked",
    schema_version: 2,
    approval_current: false,
  });
  assert.equal(view[2].status, "stale");
  assert.equal(view[3].status, "pending");
  assert.equal(view[4].status, "pending");
});

test("cycle projection distinguishes preparation, approval and archived completion", () => {
  assert.equal(
    cycle(null).every((x) => x.status === "pending"),
    true,
  );
  assert.equal(
    cycle({ phase: "exploring", approval_current: false })[1].status,
    "active",
  );
  assert.equal(
    cycle({ phase: "draft", approval_current: false })[2].detail,
    "awaiting approval",
  );
  const approved = cycle({ phase: "approved", approval_current: true });
  assert.equal(approved[2].status, "complete");
  assert.equal(approved[3].status, "pending");
  assert.equal(
    cycle({ phase: "archived", approval_current: true }).every(
      (x) => x.status === "complete",
    ),
    true,
  );
});
