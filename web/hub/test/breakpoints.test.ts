// @vitest-environment node
// breakpoints.test.ts — the hub has one breakpoint set: the theme's `tablet` (768 px) and `desk` (1200 px)
// variants, and the two queries in src/app/layout.ts. No file under web/hub/src or web/ui/src writes a
// Tailwind default breakpoint prefix (sm, md, lg, xl, 2xl, or their max- forms), an arbitrary width variant
// (`min-[60rem]:`, `max-[40em]:`) or a px or rem width in an @media query. radar/legacy.css keeps its own 520 px query. It reads files, so it runs in Node.
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

const ROOTS = ["../src", "../../ui/src"].map((p) => fileURLToPath(new URL(p, import.meta.url)));

/** Files whose width queries are their own and stay as written. */
const OWN_QUERIES = new Set(["hub/src/radar/legacy.css"]);

/** A Tailwind default breakpoint prefix on a utility: `md:grid-cols-2`, `max-sm:hidden`. A key such as
 *  `md: string` has a space after the colon and does not match. */
const PREFIX = /(?<![\w-])(?:max-)?(?:sm|md|lg|xl|2xl):(?=\S)/g;

/** An arbitrary width variant: `min-[60rem]:grid-cols-2`, `max-[640px]:hidden`. */
const ARBITRARY = /(?<![\w-])(?:min|max)-\[\s*[\d.]+(?:rem|em|px)\s*\]:(?=\S)/g;

/** An @media query that cuts on a width written in px or rem. */
const MEDIA_WIDTH = /@media[^{]*\((?:min|max)-width:\s*[\d.]+(?:px|rem)\)/g;

/** Every .ts, .tsx and .css file under the directory, as absolute paths. */
function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = `${dir}/${entry.name}`;
    if (entry.isDirectory()) walk(path, out);
    else if (/\.(?:ts|tsx|css)$/.test(entry.name)) out.push(path);
  }
  return out.sort();
}

/** One line per offence: `file:line  text`. */
function offences(pattern: RegExp, skip: ReadonlySet<string>): string[] {
  const found: string[] = [];
  for (const root of ROOTS) {
    const base = root.slice(0, root.lastIndexOf("/src"));
    const label = base.slice(base.lastIndexOf("/web/") + 5);
    for (const file of walk(root)) {
      const name = `${label}/src${file.slice(root.length)}`;
      if (skip.has(name)) continue;
      const text = readFileSync(file, "utf8");
      for (const m of text.matchAll(pattern)) {
        const line = text.slice(0, m.index).split("\n").length;
        found.push(`${name}:${line}  ${m[0].trim()}`);
      }
    }
  }
  return found;
}

describe("breakpoints", () => {
  it("no file writes a default Tailwind breakpoint prefix", () => {
    const found = offences(PREFIX, new Set());
    expect(found, `${found.length} uses of sm: md: lg: xl: 2xl:\n${found.join("\n")}`).toEqual([]);
  });

  it("no file writes an arbitrary width variant", () => {
    const found = offences(ARBITRARY, new Set());
    expect(found, `${found.length} uses of min-[..]: or max-[..]:\n${found.join("\n")}`).toEqual([]);
  });

  it("no @media query writes its own px or rem width", () => {
    const found = offences(MEDIA_WIDTH, OWN_QUERIES);
    expect(found, `${found.length} width queries\n${found.join("\n")}`).toEqual([]);
  });
});
