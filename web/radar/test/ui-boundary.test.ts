import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

const ROOT = fileURLToPath(new URL("..", import.meta.url));

/** The directories whose code runs only on the server; the dashboard and its tests never import them. */
const BACKEND_DIRS = "(?:store|alerts|budget|history|router|server|ingest|hook|lifecycle|cli)";
const BACKEND_FROM_SRC_UI = new RegExp(`from "(?:\\.\\./)+${BACKEND_DIRS}/`);
const BACKEND_FROM_TESTS = new RegExp(`from "(?:\\.\\./)+src/${BACKEND_DIRS}/`);
const COST_FROM_SRC_UI = /from "(?:\.\.\/)+cost\//;
const COST_FROM_TESTS = /from "(?:\.\.\/)+src\/cost\//;
const PRICES_FROM_SRC_UI = /from "(?:\.\.\/)+cost\/prices\.ts";/;
const PRICES_FROM_TESTS = /from "(?:\.\.\/)+src\/cost\/prices\.ts";/;

/** Every TypeScript source under `dir`, sorted so a failure lists files in a stable order. */
function sourcesUnder(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...sourcesUnder(path));
    else if (/\.(ts|tsx)$/.test(entry.name)) found.push(path);
  }
  return found.sort();
}

/** `path:line: text` for every line a pattern hits, so a failure names exactly what to fix. */
function offendingLines(paths: string[], pattern: RegExp): string[] {
  const hits: string[] = [];
  for (const path of paths) {
    const lines = readFileSync(path, "utf8").split("\n");
    for (const [index, line] of lines.entries()) {
      if (pattern.test(line)) hits.push(`${path.slice(ROOT.length + 1)}:${index + 1}: ${line.trim()}`);
    }
  }
  return hits;
}

/** The test files that exercise dashboard code: the named suites in test/ and everything under test/ui. */
function uiTestFiles(): string[] {
  const named = [
    "app-controller",
    "chart",
    "export",
    "fmt",
    "hero",
    "icons",
    "inspector",
    "palette",
    "state",
  ].map((name) => join(ROOT, "test", `${name}.test.ts`));
  return [...named, ...sourcesUnder(join(ROOT, "test", "ui"))];
}

describe("the UI boundary", () => {
  const srcUi = sourcesUnder(join(ROOT, "src", "ui"));

  it("has src/ui import no backend module", () => {
    expect(offendingLines(srcUi, BACKEND_FROM_SRC_UI)).toEqual([]);
  });

  it("has src/ui reach into cost/ only for prices.ts", () => {
    const reach = offendingLines(srcUi, COST_FROM_SRC_UI);
    expect(reach.filter((line) => !PRICES_FROM_SRC_UI.test(line))).toEqual([]);
  });

  it("has the UI tests import no backend module", () => {
    expect(offendingLines(uiTestFiles(), BACKEND_FROM_TESTS)).toEqual([]);
  });

  it("has the UI tests reach into cost/ only for prices.ts", () => {
    const reach = offendingLines(uiTestFiles(), COST_FROM_TESTS);
    expect(reach.filter((line) => !PRICES_FROM_TESTS.test(line))).toEqual([]);
  });

  it("keeps prices and its table importing nothing outside shared and cost", () => {
    const files = [join(ROOT, "src", "cost", "prices.ts"), join(ROOT, "src", "cost", "price-table.ts")];
    expect(offendingLines(files, /from "(\.\.\/)+(?!shared\/|cost\/)/)).toEqual([]);
  });
});
