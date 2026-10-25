// css-sources.test.ts — app.css turns Tailwind's automatic scan off (source(none)), so the
// folders its @source lines name are the only places classes come from. A path that points at
// nothing drops every class only that folder uses from the shipped CSS without an error, so each
// one must resolve on disk, and the shared kit in web/ui/src must be one of them.
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { describe, expect, it } from "vite-plus/test";

// import.meta.url is an http URL under happy-dom, so the stylesheet comes by directory name
const appCss = join(import.meta.dirname, "../../src/app.css");
const kit = resolve(import.meta.dirname, "../../../ui/src");

const sources = [...readFileSync(appCss, "utf8").matchAll(/^@source\s+"([^"]+)"\s*;/gm)].map(
  ([, path]) => path ?? "",
);

describe("app.css @source", () => {
  it("names at least one folder", () => {
    expect(sources.length).toBeGreaterThan(0);
  });

  it.each(sources)("%s exists next to app.css", (path) => {
    expect(existsSync(resolve(dirname(appCss), path))).toBe(true);
  });

  it("scans the shared kit in web/ui/src", () => {
    expect(sources.map((path) => resolve(dirname(appCss), path))).toContain(kit);
    expect(sources).toContain("../../ui/src");
  });
});
