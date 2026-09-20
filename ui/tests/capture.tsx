// Explicit synthetic design fixture. Production never imports this module.
import React, { act } from "react";
import { testRender } from "@opentui/react/test-utils";
import { App } from "../src/app";
import { FakeClient } from "./fixtures";
const out = process.argv[2] || "/tmp/maestro-opentui-fixture";
let ui!: Awaited<ReturnType<typeof testRender>>;
await act(async () => {
  ui = await testRender(<App client={new FakeClient()} quit={() => {}} />, {
    width: 160,
    height: 48,
  });
  await Bun.sleep(50);
});
await ui.renderOnce();
await Bun.write(out + ".txt", ui.captureCharFrame());
const frame = ui.captureSpans();
await Bun.write(
  out + ".json",
  JSON.stringify({
    ...frame,
    lines: frame.lines.map((l) => ({
      spans: l.spans.map((s) => ({
        ...s,
        fg: s.fg.toInts(),
        bg: s.bg.toInts(),
      })),
    })),
  }),
);
await act(async () => ui.renderer.destroy());
