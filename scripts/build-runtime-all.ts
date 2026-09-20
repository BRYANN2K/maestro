const uiInstall = Bun.spawn(
  [
    "bun",
    "install",
    "--cwd",
    "ui",
    "--frozen-lockfile",
    "--ignore-scripts",
    "--os=*",
    "--cpu=*",
  ],
  { stdout: "inherit", stderr: "inherit" },
);
if (await uiInstall.exited) throw Error("UI dependencies unavailable");
const install = Bun.spawn(
  [
    "bun",
    "install",
    "--cwd",
    "runtime",
    "--frozen-lockfile",
    "--ignore-scripts",
    "--os=*",
    "--cpu=*",
  ],
  { stdout: "inherit", stderr: "inherit" },
);
if (await install.exited) throw new Error("Runtime dependencies unavailable");
for (const [os, arch] of [
  ["darwin", "amd64"],
  ["darwin", "arm64"],
  ["linux", "amd64"],
  ["linux", "arm64"],
  ["windows", "amd64"],
]) {
  const child = Bun.spawn(
    ["bun", "scripts/build-runtime.ts", os, arch, `dist/runtime/${os}_${arch}`],
    { stdout: "inherit", stderr: "inherit" },
  );
  if (await child.exited)
    throw new Error(`Runtime build failed for ${os}/${arch}`);
  const ui = Bun.spawn(
    ["bun", "scripts/build-ui.ts", os, arch, `dist/runtime/${os}_${arch}`],
    { stdout: "inherit", stderr: "inherit" },
  );
  if (await ui.exited) throw Error(`UI build failed for ${os}/${arch}`);
}
