export const ROLES = [
  "backend",
  "api",
  "frontend",
  "security",
  "verification",
  "documentation",
  "research",
  "architecture",
  "design",
  "database",
  "cloud",
  "delivery",
  "communication",
];
export const PHASES = ["explore", "validate", "apply", "check", "docs"];
const own = (x, k) => Object.prototype.hasOwnProperty.call(x, k);
export function invariant(ok, message) {
  if (!ok) throw new Error(message);
}
export function object(x) {
  return x && typeof x === "object" && !Array.isArray(x);
}
export function slug(x) {
  invariant(
    typeof x === "string" && /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(x),
    "Invalid identifier",
  );
  return x;
}
export function validateSettings(value = {}) {
  invariant(object(value), "Orchestration settings must be an object");
  invariant(
    value.version === undefined || value.version === 1,
    "Unsupported orchestration settings version",
  );
  const defaults = value.defaults || {};
  invariant(object(defaults), "Invalid defaults");
  for (const key of ["max_workers", "max_shared_writers"])
    if (defaults[key] !== undefined)
      invariant(
        Number.isInteger(defaults[key]) &&
          defaults[key] >= 1 &&
          defaults[key] <= 8,
        `${key} must be between 1 and 8`,
      );
  invariant(
    (defaults.max_shared_writers ?? 1) === 1,
    "This shared-checkout integration supports one writer; parallel writers require isolated workspaces",
  );
  const c = value.clients?.maestro || value.clients?.opencode || {};
  invariant(object(c), "Invalid Maestro settings");
  function profile(p) {
    invariant(object(p), "Invalid profile");
    for (const key of Object.keys(p))
      invariant(
        ["model", "effort", "fast", "agent", "fallbacks"].includes(key),
        `Unknown profile field: ${key}`,
      );
    for (const key of ["model", "effort", "agent"])
      if (p[key] !== undefined)
        invariant(
          typeof p[key] === "string" && p[key].length > 0,
          `Invalid ${key}`,
        );
    if (p.fast !== undefined)
      invariant(
        p.fast === "inherit" || typeof p.fast === "boolean",
        "Fast must be inherit, true or false",
      );
    if (p.fallbacks !== undefined)
      invariant(
        Array.isArray(p.fallbacks) &&
          p.fallbacks.length <= 4 &&
          p.fallbacks.every((x) => typeof x === "string" && x.length > 0),
        "Invalid same-host model fallbacks",
      );
    if (p.model && p.model !== "inherit") parseRef(p.model);
  }
  profile(
    Object.fromEntries(
      Object.entries(defaults).filter(
        ([k]) => !["max_workers", "max_shared_writers"].includes(k),
      ),
    ),
  );
  for (const [role, p] of Object.entries(c.roles || {})) {
    slug(role);
    invariant(
      !["discovery", "coordinator"].includes(role),
      "Discovery stays with the coordinator",
    );
    profile(p);
  }
  for (const [phase, p] of Object.entries(c.phases || {})) {
    invariant(PHASES.includes(phase), "Invalid phase");
    profile(p);
  }
  for (const [phase, roles] of Object.entries(c.phase_roles || {})) {
    invariant(PHASES.includes(phase) && object(roles), "Invalid phase roles");
    for (const [role, p] of Object.entries(roles)) {
      slug(role);
      invariant(
        !["discovery", "coordinator"].includes(role),
        "Discovery stays with the coordinator",
      );
      profile(p);
    }
  }
  return value;
}
export function merge(a = {}, b = {}) {
  const out = { ...a };
  for (const [k, v] of Object.entries(b)) {
    invariant(
      !["__proto__", "constructor", "prototype"].includes(k),
      "Unsafe settings key",
    );
    out[k] = object(v)
      ? merge(object(out[k]) ? out[k] : {}, v)
      : structuredClone(v);
  }
  return out;
}
export function parseRef(value) {
  const i = value.indexOf("/");
  invariant(
    i > 0 && i < value.length - 1,
    "Choose a provider/model reference from Maestro",
  );
  const parts = value.slice(i + 1).split("#");
  const [id, variant] = parts;
  invariant(
    id && parts.length <= 2 && (parts.length === 1 || variant),
    "Invalid model reference",
  );
  return { providerID: value.slice(0, i), id, ...(variant ? { variant } : {}) };
}
export function refText(ref) {
  return ref
    ? `${ref.providerID}/${ref.id}${ref.variant ? "#" + ref.variant : ""}`
    : "inherit";
}
export function variants(model) {
  const base = { settings: model.settings || {}, body: model.body || {} };
  const entries = [{ id: undefined }, ...(model.variants || [])];
  return entries.map((v) => {
    const e = merge(base, v);
    const effort =
      e.settings?.reasoningEffort ??
      e.body?.reasoning?.effort ??
      e.body?.reasoning_effort ??
      e.body?.output_config?.effort ??
      null;
    const tier = e.settings?.serviceTier ?? e.body?.service_tier;
    const speed = e.settings?.speed ?? e.body?.speed;
    const fast = tier === "priority" || speed === "fast";
    return { id: v.id, effort, fast };
  });
}
export function capabilities(models) {
  return models
    .filter((m) => m.enabled !== false && m.capabilities?.tools !== false)
    .map((m) => ({
      ref: `${m.providerID}/${m.id}`,
      name: m.name || m.id,
      variants: variants(m),
      fastSupported: variants(m).some((v) => v.fast),
    }));
}
export function assignment(settings, role, phase, override = {}) {
  slug(role);
  invariant(
    !["discovery", "coordinator"].includes(role),
    "Discovery belongs to the coordinator",
  );
  validateSettings(settings);
  const c = settings.clients?.maestro || settings.clients?.opencode || {};
  const layers = [
    ["defaults", settings.defaults || {}],
    ["phase", c.phases?.[phase] || {}],
    ["role", c.roles?.[role] || {}],
    ["phase-role", c.phase_roles?.[phase]?.[role] || {}],
    ["task", override],
  ];
  let value = { model: "inherit", effort: "inherit", fast: "inherit" },
    sources = {};
  for (const [source, p] of layers) {
    if (p.model !== undefined && p.model !== value.model) {
      value.effort = "inherit";
      value.fast = "inherit";
      sources.effort = source;
      sources.fast = source;
    }
    for (const key of ["model", "effort", "fast", "agent", "fallbacks"])
      if (own(p, key)) {
        value[key] = p[key];
        sources[key] = source;
      }
  }
  return { value, sources };
}
export function resolveProfile(
  settings,
  role,
  phase,
  parent,
  models,
  override = {},
) {
  const { value, sources } = assignment(settings, role, phase, override);
  invariant(
    parent?.id && parent?.providerID,
    "The coordinator has no resolved model",
  );
  let wanted =
    value.model === "inherit" ? { ...parent } : parseRef(value.model);
  let model = models.find(
    (m) =>
      m.id === wanted.id &&
      m.providerID === wanted.providerID &&
      m.enabled !== false,
  );
  let fallback;
  if (!model)
    for (const candidate of value.fallbacks || []) {
      const ref = parseRef(candidate);
      const found = models.find(
        (m) =>
          m.id === ref.id &&
          m.providerID === ref.providerID &&
          m.enabled !== false,
      );
      if (found) {
        fallback = {
          from: refText(wanted),
          to: candidate,
          reason: "Preferred model unavailable in the current Maestro catalog",
        };
        wanted = ref;
        model = found;
        break;
      }
    }
  invariant(
    model,
    `Configured Maestro model is unavailable: ${refText(wanted)}`,
  );
  invariant(
    model.capabilities?.tools !== false,
    "Selected model does not support tools",
  );
  const all = variants(model);
  // Maestro serializes an unset session variant as "default". A catalog
  // overlay explicitly named default still takes precedence over the base.
  const initial =
    all.find((v) => v.id === wanted.variant) ||
    (wanted.variant === "default"
      ? all.find((v) => v.id === undefined)
      : undefined);
  invariant(initial, "Configured model variant is unavailable");
  let choices = all;
  if (value.effort !== "inherit")
    choices = choices.filter((v) => v.effort === value.effort);
  else choices = choices.filter((v) => v.effort === initial.effort);
  if (value.fast !== "inherit")
    choices = choices.filter((v) => v.fast === value.fast);
  else choices = choices.filter((v) => v.fast === initial.fast);
  const selected = choices.find((v) => v.id === wanted.variant) || choices[0];
  invariant(
    selected,
    "This model does not expose the requested effort/Fast combination",
  );
  const ref = {
    providerID: model.providerID,
    id: model.id,
    ...(selected.id ? { variant: selected.id } : {}),
  };
  return {
    fallback,
    model: ref,
    modelName: model.name || model.id,
    effort: selected.effort ?? selected.id ?? "model default",
    fast: selected.fast,
    requested: value,
    sources,
  };
}
export function cycle(state) {
  const phases = [
    "Bootstrap",
    "Explore",
    "Validate",
    "Apply",
    "Check",
    "Docs",
    "Archive",
  ];
  if (!state)
    return phases.map((label, i) => ({
      label,
      status: i === 0 ? "pending" : "pending",
    }));
  const at =
    {
      exploring: 1,
      draft: 2,
      approved: 2,
      applying: 3,
      checked: 4,
      documented: 5,
      archived: 6,
    }[state.phase] ?? 1;
  return phases
    .map((label, i) => ({
      label,
      status:
        i < at
          ? "complete"
          : i > at
            ? "pending"
            : ["approved", "checked", "documented", "archived"].includes(
                  state.phase,
                )
              ? "complete"
              : "active",
      detail:
        i === 2
          ? state.approval_current
            ? "approved"
            : state.phase === "draft"
              ? "awaiting approval"
              : state.approval
                ? "stale approval"
                : undefined
          : undefined,
    }))
    .map((s, i) =>
      (state.approval ||
        ["approved", "applying", "checked", "documented"].includes(
          state.phase,
        )) &&
      !state.approval_current &&
      i >= 2
        ? {
            ...s,
            status: i === 2 ? "stale" : "pending",
            detail: i === 2 ? "stale approval" : undefined,
          }
        : s,
    );
}
