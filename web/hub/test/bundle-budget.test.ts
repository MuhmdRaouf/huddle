// @vitest-environment node
// bundle-budget.test.ts — a size budget on the committed hub bundle: the brotli copies of app.<hash>.js
// and app.<hash>.css that scripts/build.mjs writes into internal/huddle/web/dist, the bytes a browser
// downloads. Each budget is the measured size rounded up by about 10 percent, so growth past it fails
// the check and the budget moves only by a deliberate edit here. The files are build output, so this
// file runs in Node, not happy-dom.
import { readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

const DIST = fileURLToPath(new URL("../../../internal/huddle/web/dist", import.meta.url));

type Budget = { pattern: RegExp; maxBytes: number };

const budgets: Budget[] = [
  // measured 174 012 bytes on 2026-10-29
  { pattern: /^app\.[\w-]+\.js\.br$/, maxBytes: 192_000 },
  // measured 36 051 bytes on 2026-10-25
  { pattern: /^app\.[\w-]+\.css\.br$/, maxBytes: 40_000 },
];

/** The one dist file whose name matches the pattern. */
function onlyMatch(pattern: RegExp): string {
  const names = readdirSync(DIST).filter((name) => pattern.test(name));
  if (names.length !== 1) throw new Error(`want one ${pattern} in ${DIST}, found ${names.length}`);
  return names[0] as string;
}

describe("hub bundle budget", () => {
  for (const budget of budgets) {
    it(`keeps ${budget.pattern} at or under ${budget.maxBytes} bytes`, () => {
      const name = onlyMatch(budget.pattern);
      const size = statSync(`${DIST}/${name}`).size;
      expect(size, `${name} is ${size} bytes, over its ${budget.maxBytes}-byte budget`).toBeLessThanOrEqual(
        budget.maxBytes,
      );
    });
  }
});
