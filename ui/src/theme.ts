const mono =
  Object.hasOwn(process.env, "NO_COLOR") ||
  process.env.MAESTRO_COLOR === "none";
export const T = {
  bg: "#0B0C10",
  panel: "#101116",
  raised: "#191A22",
  selection: "#292435",
  line: "#30323B",
  ink: "#EEECE5",
  muted: "#9899A5",
  dim: "#727581",
  accent: "#B7A5E6",
  green: "#94D5B0",
  amber: "#DFC08A",
  red: "#E59AAA",
};
if (mono) {
  for (const k of Object.keys(T) as (keyof typeof T)[])
    T[k] = ["bg", "panel", "raised", "selection"].includes(k)
      ? "#000000"
      : "#FFFFFF";
}
export function clean(value: unknown, limit = 262144) {
  return String(value ?? "")
    .replace(/\x1b\][\s\S]*?(?:\x07|\x1b\\)/g, "")
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/[\p{Cc}\p{Cf}]/gu, (c) => (c === "\n" || c === "\t" ? c : ""))
    .slice(0, limit);
}
export function label(id: string) {
  return id.replace(/[-_]/g, " ").replace(/^./, (c) => c.toUpperCase());
}
export const stages = ["Explore", "Specify", "Build", "Verify", "Deliver"];
export function phaseIndex(phase?: string) {
  return phase === "draft"
    ? 1
    : phase === "approved" || phase === "applying"
      ? 2
      : phase === "checked"
        ? 3
        : phase === "documented" || phase === "archived"
          ? 4
          : 0;
}
export function phaseLabel(phase?: string) {
  return (
    (
      {
        draft: "Contract to review",
        approved: "Ready to build",
        applying: "In progress",
        checked: "Evidence reviewed",
        documented: "Ready to deliver",
        archived: "Completed",
      } as Record<string, string>
    )[phase || ""] || "Exploring"
  );
}
