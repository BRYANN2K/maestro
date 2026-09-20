import {
  readFileSync,
  writeFileSync,
  mkdirSync,
  readdirSync,
  existsSync,
  lstatSync,
  realpathSync,
  renameSync,
  unlinkSync,
  openSync,
  closeSync,
} from "node:fs";
import { join, resolve, dirname, relative, isAbsolute } from "node:path";
import { homedir } from "node:os";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { createHash, randomUUID } from "node:crypto";
import { invariant, slug, merge, validateSettings } from "./routing.mjs";
export function json(path, fallback) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (e) {
    if (e.code === "ENOENT") return fallback;
    throw new Error(`Cannot read ${path}: ${e.message}`);
  }
}
export function safe(root, path) {
  const r = resolve(root),
    p = resolve(r, path),
    rel = relative(r, p);
  invariant(
    rel === "" || (!rel.startsWith("..") && !isAbsolute(rel)),
    "Path escapes project",
  );
  let at = r;
  for (const part of rel.split("/").filter(Boolean)) {
    at = join(at, part);
    try {
      invariant(
        !lstatSync(at).isSymbolicLink(),
        "Symlinks are not allowed in managed state",
      );
    } catch (e) {
      if (e.code !== "ENOENT") throw e;
    }
  }
  return p;
}
export function atomic(path, value) {
  mkdirSync(dirname(path), { recursive: true });
  const tmp = join(dirname(path), `.stip-write-${randomUUID()}`);
  try {
    writeFileSync(tmp, JSON.stringify(value, null, 2) + "\n", {
      mode: 0o600,
      flag: "wx",
    });
    renameSync(tmp, path);
  } finally {
    if (existsSync(tmp)) unlinkSync(tmp);
  }
}
export function locked(root, fn) {
  const path = safe(root, ".workflow/.lock");
  mkdirSync(dirname(path), { recursive: true });
  let fd;
  try {
    fd = openSync(path, "wx", 0o600);
  } catch {
    throw new Error("Workflow is locked by another operation");
  }
  try {
    return fn();
  } finally {
    closeSync(fd);
    unlinkSync(path);
  }
}
export function hash(x) {
  return createHash("sha256").update(JSON.stringify(x)).digest("hex");
}
export class ProjectStore {
  constructor(root, options = {}) {
    this.root = realpathSync(root);
    this.engine =
      options.engine ||
      fileURLToPath(new URL("../../internal/workflow/assets/workflow.py", import.meta.url));
    this.personal =
      options.personal ||
      join(
        process.env.XDG_CONFIG_HOME || join(homedir(), ".config"),
        "maestro",
        "stipulate.json",
      );
  }
  path(p) {
    return safe(this.root, p);
  }
  settingsByScope() {
    return {
      personal: json(this.personal, {}).orchestration || {},
      project: json(this.path(".workflow/config.json"), {}).orchestration || {},
      local: json(this.path(".workflow/local.json"), {}).orchestration || {},
    };
  }
  settings() {
    const { personal, project, local } = this.settingsByScope();
    return validateSettings(merge(merge(personal, project), local));
  }
  saveSettings(value, scope = "project", expected) {
    validateSettings(value);
    const path =
      scope === "personal"
        ? safe("/", this.personal)
        : this.path(
            scope === "local"
              ? ".workflow/local.json"
              : ".workflow/config.json",
          );
    invariant(
      ["project", "local", "personal"].includes(scope),
      "Invalid settings scope",
    );
    invariant(
      !existsSync(path) || !lstatSync(path).isSymbolicLink(),
      "Settings destination is a symlink",
    );
    return locked(this.root, () => {
      invariant(
        hash(this.settingsByScope()) === expected,
        "Settings changed elsewhere; reload before saving",
      );
      const doc = json(
        path,
        scope === "project"
          ? {
              schema_version: 1,
              extensions: {},
              settings: { require_user_approval: true },
            }
          : {},
      );
      doc.orchestration = value;
      atomic(path, doc);
      return this.settings();
    });
  }
  engineCommand(args) {
    const out = execFileSync(
      "python3",
      [this.engine, "--root", this.root, ...args],
      {
        encoding: "utf8",
        maxBuffer: 4 * 1024 * 1024,
        timeout: 15000,
        env: { ...process.env, PYTHONDONTWRITEBYTECODE: "1" },
      },
    );
    return JSON.parse(out);
  }
  changes() {
    return ["changes", "archive"].flatMap((folder) => {
      const path = this.path(`.workflow/${folder}`);
      if (!existsSync(path)) return [];
      return readdirSync(path, { withFileTypes: true })
        .filter((x) => x.isDirectory())
        .flatMap((x) => {
          try {
            slug(x.name);
            const s = json(
              this.path(`.workflow/${folder}/${x.name}/state.json`),
            );
            return s
              ? [{ id: x.name, phase: s.phase, archived: folder === "archive" }]
              : [];
          } catch {
            return [];
          }
        });
    });
  }
  status(id) {
    slug(id);
    const archived = existsSync(
      this.path(`.workflow/archive/${id}/state.json`),
    );
    if (archived) {
      const state = json(this.path(`.workflow/archive/${id}/state.json`));
      return { ...state, approval_current: true, archived: true };
    }
    return this.engineCommand(["status", id, "--compact"]);
  }
  plan(id) {
    return this.engineCommand(["plan", slug(id)]).plan;
  }
  registry(id) {
    return json(this.path(`.workflow/.runtime/${slug(id)}/state.json`), null);
  }
  helpers() {
    const value = json(this.path(".workflow/.runtime/helpers.json"), {
      version: 1,
      jobs: [],
    });
    invariant(
      value?.version === 1 && Array.isArray(value.jobs),
      "Invalid private helper registry",
    );
    const ids = new Set();
    for (const job of value.jobs) {
      invariant(
        job?.kind === "helper" &&
          typeof job.helper_id === "string" &&
          job.helper_id === job.run_id &&
          job.helper_id === job.task_id &&
          !ids.has(job.helper_id) &&
          ["explore", "validate"].includes(job.phase) &&
          Array.isArray(job.write_paths) &&
          job.write_paths.length === 0 &&
          typeof job.parent_id === "string" &&
          job.parent_id.startsWith("ses_") &&
          (job.session_id === null ||
            (typeof job.session_id === "string" &&
              job.session_id.startsWith("ses_"))) &&
          [
            "starting",
            "running",
            "waiting",
            "unknown",
            "returned",
            "failed",
            "cancelled",
          ].includes(job.status),
        "Invalid read-only helper record",
      );
      slug(job.role);
      invariant(
        !["coordinator", "discovery"].includes(job.role),
        "Invalid helper role",
      );
      ids.add(job.helper_id);
    }
    return value;
  }
  mutateHelpers(fn) {
    return locked(this.root, () => {
      const next = fn(this.helpers());
      atomic(this.path(".workflow/.runtime/helpers.json"), next);
      return next;
    });
  }
  mutate(id, fn) {
    return locked(this.root, () => {
      const p = this.path(`.workflow/.runtime/${slug(id)}/state.json`);
      const state = json(p, null);
      const next = fn(state);
      atomic(p, next);
      return next;
    });
  }
  bind(parentID, id) {
    invariant(
      typeof parentID === "string" && parentID.startsWith("ses_"),
      "Invalid native session",
    );
    slug(id);
    return locked(this.root, () => {
      const p = this.path(".workflow/.runtime/bindings.json");
      const v = json(p, {});
      v[parentID] = id;
      atomic(p, v);
    });
  }
  binding(parentID) {
    return json(this.path(".workflow/.runtime/bindings.json"), {})[parentID];
  }
  readArtifact(id, kind) {
    slug(id);
    const names = {
      proposal: "proposal.md",
      spec: "spec.md",
      evidence: "evidence.md",
      tasks: "tasks.md",
      "execution-plan": "execution-plan.json",
    };
    invariant(names[kind], "Unknown artifact");
    const folder = existsSync(this.path(`.workflow/archive/${id}`))
      ? "archive"
      : "changes";
    const p = this.path(`.workflow/${folder}/${id}/${names[kind]}`);
    return {
      path: p,
      text: existsSync(p)
        ? readFileSync(p, "utf8").slice(0, 100000)
        : "This artifact has not been created.",
    };
  }
}
