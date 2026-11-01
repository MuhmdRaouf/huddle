// @vitest-environment node
// api-edge-guard.test.ts — the no-cast guard: every .ts and .tsx file under web/hub/src is scanned for
// `as <Type>` casts on server data, and each file's count ratchets against api-edge.baseline.json. A
// cast counts when its operand holds one of the edge markers (api(, .json(), JSON.parse(,
// fetchJson, ev.data, m.data, or the parameter of a `.then((r)` callback). A count may fall and never
// rise, and the migration of a file to a decoded schema lowers its baseline. On failure the message holds
// every problem line, then the casts of the files over their baseline, then the whole current counts as
// JSON, so a migration can copy its new baseline straight out of it. It reads files, so it runs in Node.
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

type Counts = Record<string, number>;
type Cast = { line: number; text: string };

const SRC = fileURLToPath(new URL("../src", import.meta.url));
const BASELINE = JSON.parse(
  readFileSync(fileURLToPath(new URL("./api-edge.baseline.json", import.meta.url)), "utf8"),
) as Counts;

// A cast ends at `as`, a type name and what follows it: the operand is the text from the start of the
// statement's expression (after `=`, `(`, `,`, `return` or `await`) up to the `as`.
const MARKERS = [/\bapi\(/, /\.json\(\)/, /JSON\.parse\(/, /\bfetchJson\b/, /\bev\.data\b/, /\bm\.data\b/];
const THEN_PARAM = /\.then\(\(r\b/;
const CAST = /\bas\s+(?!const\b|unknown\b)[A-Za-z_{[]/g;

/** Every .ts and .tsx file under the directory, sorted, as paths relative to src. */
function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) => (a.name > b.name ? 1 : -1))) {
    const path = `${dir}/${entry.name}`;
    if (entry.isDirectory()) walk(path, out);
    else if (/\.tsx?$/.test(entry.name)) out.push(path.slice(SRC.length + 1));
  }
  return out;
}

/** The casts in one file whose operand text holds an edge marker. */
function edgeCasts(source: string): Cast[] {
  const found: Cast[] = [];
  const lines = source.split("\n");
  for (const [index, line] of lines.entries()) {
    for (const match of line.matchAll(CAST)) {
      const before = line.slice(0, match.index);
      // an operand may start on an earlier line: join the lines back to the statement's start
      let operand = before;
      for (let up = index - 1; up >= 0 && !/[;{}]\s*$/.test(lines[up] ?? "") && index - up < 4; up--) {
        operand = `${lines[up]}\n${operand}`;
      }
      if (MARKERS.some((marker) => marker.test(operand)) || THEN_PARAM.test(operand)) {
        found.push({ line: index + 1, text: line.trim() });
      }
    }
  }
  return found;
}

/** The counts with their keys sorted, as the baseline file and the failure message print them. */
function sorted(counts: Counts): Counts {
  return Object.fromEntries(Object.entries(counts).sort(([a], [b]) => (a > b ? 1 : -1)));
}

/** One sentence per file whose count differs from its baseline. */
function ratchet(baseline: Counts, current: Counts): string[] {
  const problems: string[] = [];
  for (const path of [...new Set([...Object.keys(baseline), ...Object.keys(current)])].sort()) {
    const was = baseline[path] ?? 0;
    const now = current[path] ?? 0;
    if (now > was) problems.push(`${path}: ${now} casts, over its baseline of ${was}`);
    if (now < was)
      problems.push(`${path}: ${now} casts, under its baseline of ${was}: lower the baseline to ${now}`);
  }
  return problems;
}

describe("hub api edge guard", () => {
  it("keeps every file's casts on server data at or under its baseline", () => {
    const current: Counts = {};
    const casts: string[] = [];
    for (const rel of walk(SRC)) {
      const found = edgeCasts(readFileSync(`${SRC}/${rel}`, "utf8"));
      if (found.length === 0) continue;
      current[rel] = found.length;
      for (const cast of found) casts.push(`${rel}:${cast.line} ${cast.text}`);
    }
    const problems = ratchet(BASELINE, current);
    const over = new Set(Object.keys(current).filter((path) => (current[path] ?? 0) > (BASELINE[path] ?? 0)));
    const lines = casts.filter((line) => over.has(line.slice(0, line.indexOf(":"))));
    const message = [
      problems.length > 0 ? problems.join("\n") : "(no counts over their baseline)",
      lines.length > 0 ? lines.join("\n") : "(no casts in the files over their baseline)",
      JSON.stringify(sorted(current), null, 2),
    ].join("\n\n");
    expect(problems, message).toEqual([]);
  });
});
