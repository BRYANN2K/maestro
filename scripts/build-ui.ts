import { resolve } from "node:path";
const platform = process.argv[2] || process.platform;
const arch = (process.argv[3] || process.arch).replace("amd64", "x64");
const os = platform === "windows" ? "win32" : platform;
const target =
  `bun-${os === "win32" ? "windows" : os}-${arch}${arch === "x64" ? "-baseline" : ""}` as Bun.Build.CompileTarget;
const outfile = resolve(
  process.argv[4] || "bin",
  os === "win32" ? "maestro-ui.exe" : "maestro-ui",
);
const result = await Bun.build({
  entrypoints: ["./ui/src/main.tsx"],
  target: "bun",
  minify: true,
  jsx: {
    runtime: "automatic",
    importSource: "@opentui/react",
    development: false,
  },
  define: {
    "process.env.NODE_ENV": '"production"',
    "process.env.OPENTUI_LIBC": '"glibc"',
  },
  compile: {
    target,
    outfile,
    autoloadDotenv: false,
    autoloadBunfig: false,
    autoloadTsconfig: false,
    autoloadPackageJson: false,
  },
});
if (!result.success)
  throw new AggregateError(result.logs, "Maestro UI build failed");
console.log(outfile);
