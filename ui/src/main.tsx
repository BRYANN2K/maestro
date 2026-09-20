import React from "react";
import { createCliRenderer } from "@opentui/core";
import { createRoot } from "@opentui/react";
import { Bridge } from "./bridge";
import { App } from "./app";
import { T } from "./theme";
if (process.argv.includes("--version")) {
  console.log("maestro-ui / OpenTUI 0.5.11 / protocol 1");
  process.exit(0);
}
const address = process.env.MAESTRO_UI_ADDRESS || "",
  token = process.env.MAESTRO_UI_TOKEN || "";
if (!address || !token) {
  console.error("Launch the interface with maestro.");
  process.exit(2);
}
const bridge = new Bridge(address, token);
await bridge.connected();
delete process.env.MAESTRO_UI_TOKEN;
const renderer = await createCliRenderer({
  screenMode: "alternate-screen",
  backgroundColor: T.bg,
  targetFps: 30,
  maxFps: 30,
  useMouse: true,
  enableMouseMovement: true,
  exitOnCtrlC: false,
  consoleMode: "disabled",
  openConsoleOnError: false,
});
let closing = false;
function quit(code = 0) {
  if (closing) return;
  closing = true;
  renderer.destroy();
  bridge.close();
  process.exit(code);
}
process.on("SIGTERM", () => quit());
process.on("SIGINT", () => quit());
process.on("uncaughtException", () => quit(1));
process.on("unhandledRejection", () => quit(1));
createRoot(renderer).render(<App client={bridge} quit={() => quit()} />);
