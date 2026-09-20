import { expect, test } from "bun:test";
import { browserURL, linkParts } from "../src/links";
test("browser links preserve destinations and leave unsafe schemes as text", () => {
  const parts = linkParts("[Docs](https://example.test/a?b=1&c=2) and (https://example.test/page). javascript:alert(1) file:///tmp/a");
  expect(parts.filter(p => p.href).map(p => p.href)).toEqual(["https://example.test/a?b=1&c=2", "https://example.test/page"]);
  for (const value of ["javascript:alert(1)", "file:///tmp/a", "https://user:pass@example.test", "https://example.test/\x1b", "https://example.test/ a", "--exec"]) expect(browserURL(value)).toBeNull();
});
test("balanced URL parentheses survive Markdown and surrounding prose", () => {
  expect(linkParts("[Reference](https://example.test/a_(b)) and (https://example.test/a_(b)).").filter(p => p.href).map(p => p.href)).toEqual(["https://example.test/a_(b)", "https://example.test/a_(b)"]);
});
