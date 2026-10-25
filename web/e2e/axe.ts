// axe.ts — the accessibility gate every page spec runs: axe-core, read from node_modules and
// evaluated in the page (an evaluate is not a script tag, so the hub's CSP stays as it is), then
// every violation at serious or critical impact, reduced to what a failing assertion should print.

import { expect, type Page } from "@playwright/test";
import type { AxeResults, ImpactValue } from "axe-core";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

/** axe-core's own bundle, read once per run. */
const source = readFileSync(createRequire(import.meta.url).resolve("axe-core/axe.min.js"), "utf8");

/** The impacts that fail a page. */
const BLOCKING: readonly ImpactValue[] = ["serious", "critical"];

/** One blocking violation: the rule, its impact, and the selectors of the nodes it found. */
export type Violation = { id: string; impact: ImpactValue | undefined; nodes: string[] };

/** Runs axe over the page as it stands, less the parts `exclude` names (CSS selectors), and
 *  answers the serious and critical violations. */
export async function axeViolations(page: Page, exclude: readonly string[] = []): Promise<Violation[]> {
  await page.evaluate(source);
  const results = await page.evaluate(
    (ex) =>
      (
        window as unknown as {
          axe: { run: (context: { exclude: string[][] } | Document) => Promise<AxeResults> };
        }
      ).axe.run(ex.length > 0 ? { exclude: ex.map((s) => [s]) } : document),
    [...exclude],
  );
  return results.violations
    .filter((v) => v.impact != null && BLOCKING.includes(v.impact))
    .map((v) => ({ id: v.id, impact: v.impact ?? undefined, nodes: v.nodes.map((n) => n.target.join(" ")) }));
}

/** Fails the test when axe finds any serious or critical violation on the page, less the parts
 *  `exclude` names. */
export async function expectAccessible(page: Page, exclude: readonly string[] = []): Promise<void> {
  expect(await axeViolations(page, exclude)).toEqual([]);
}
