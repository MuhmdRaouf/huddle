// @vitest-environment node
// design-guard.test.ts — the workspace guard: every .ts, .tsx and .css file under web/hub/src (except
// legacy.css, whose own definitions leave with the file) is scanned for the old system's marks, and each
// file and rule ratchets against design-guard.baseline.json: a count may fall and never rise, and a page
// migration lowers the baselines it emptied. On failure the message holds every problem line, then the
// offences of the files over their baselines, then the whole current counts as JSON, so a migration can
// copy its new baseline straight out of it. It reads files, so it runs in Node, not happy-dom.
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";
import { legacyWords, ratchet, scanFile } from "./design-guard/scan.ts";
import type { Counts, Rule } from "./design-guard/scan.ts";

const SRC = fileURLToPath(new URL("../src", import.meta.url));
const BASELINE = JSON.parse(
  readFileSync(fileURLToPath(new URL("./design-guard.baseline.json", import.meta.url)), "utf8"),
) as Counts;
const SKIP = new Set(["radar/legacy.css"]);

/** Every .ts, .tsx and .css file under the directory, sorted, as paths relative to src. */
function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) => (a.name > b.name ? 1 : -1))) {
    const path = `${dir}/${entry.name}`;
    if (entry.isDirectory()) walk(path, out);
    else if (/\.(?:ts|tsx|css)$/.test(entry.name)) out.push(path.slice(SRC.length + 1));
  }
  return out;
}

/** The counts with their keys sorted, as the baseline file and the failure message print them. */
function sorted(counts: Counts): Counts {
  const out: Counts = {};
  for (const path of Object.keys(counts).sort()) {
    const rules = counts[path] ?? {};
    const sortedRules: Partial<Record<Rule, number>> = {};
    for (const rule of Object.keys(rules).sort() as Rule[]) {
      const n = rules[rule];
      if (n !== undefined) sortedRules[rule] = n;
    }
    out[path] = sortedRules;
  }
  return out;
}

describe("hub design guard", () => {
  it("keeps every old-system count at or under its baseline", () => {
    const legacy = legacyWords(readFileSync(`${SRC}/radar/legacy.css`, "utf8"));
    const current: Counts = {};
    const offences: string[] = [];
    for (const rel of walk(SRC)) {
      if (SKIP.has(rel)) continue;
      const found = scanFile(rel, readFileSync(`${SRC}/${rel}`, "utf8"), legacy);
      if (found.length === 0) continue;
      const counts: Partial<Record<Rule, number>> = {};
      for (const o of found) {
        offences.push(`${rel}:${o.line} ${o.rule} ${o.text}`);
        counts[o.rule] = (counts[o.rule] ?? 0) + 1;
      }
      current[rel] = counts;
    }
    const problems = ratchet(BASELINE, current);
    const over = new Set(
      Object.keys(current).filter((path) =>
        Object.entries(current[path] ?? {}).some(([rule, n]) => n > (BASELINE[path]?.[rule as Rule] ?? 0)),
      ),
    );
    const lines = offences.filter((line) => over.has(line.slice(0, line.indexOf(":"))));
    const message = [
      problems.length > 0 ? problems.join("\n") : "(no counts over their baseline)",
      lines.length > 0 ? lines.join("\n") : "(no offences in the files over their baseline)",
      JSON.stringify(sorted(current), null, 2),
    ].join("\n\n");
    expect(problems, message).toEqual([]);
  });
});
