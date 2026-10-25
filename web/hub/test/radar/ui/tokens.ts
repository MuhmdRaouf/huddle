// tokens.ts — what "on the tokens" means for a rendered Radar view: no class from daisyUI or
// legacy.css, and every table drawn in both of its layouts, so each body cell names its column for
// the phone's card. The first group of patterns is the screenshot suite's LEGACY_CLASS
// (web/e2e/screens.spec.ts); the rest are the daisyUI components and the legacy.css hooks the
// Radar views painted with.

import { expect } from "vite-plus/test";

const LEGACY_CLASS: readonly RegExp[] = [
  /^btn(-[\w-]+)?$/,
  /^tabs?(-[\w-]+)?$/,
  /^glass$/,
  /^badge-[\w-]+$/,
  /^(bg|text|border|ring|outline|fill|stroke|divide|from|via|to)-(base-(100|200|300|content)|(primary|secondary|accent|neutral|info|success|warning|error)(-content)?)(\/\d+)?$/,
  /^hairline$/,
  /^(badge|join|join-item|panel|list|list-row|select|input|toggle|label|fieldset|fieldset-legend|status)$/,
  /^(alert|timeline|toggle|status|neon)-[\w-]+$/,
  /^table-(pin-rows|pin-cols|zebra|xs|sm|md|lg|xl)$/,
  /^(table|alert|timeline)$/,
  /^rounded-(field|box|selector)$/,
  /^text-(meta|row)$/,
  /^(dot|dot-pulse|presence|presence-[\w]+|empty|empty-[\w]+|tree-toggle|tree-leaf|tree-note|code|strong|muted|field|field-[\w]+|table-wrap|rate-chart|streaming|provider)$/,
  /var\(--(color-[\w-]+|series-\d|danger|success|warning|primary)\)/,
];

/** Every `tag.class` under root whose class (after any variant prefix) is a legacy one, each once. */
export function legacyClasses(root: ParentNode): string[] {
  const found = new Set<string>();
  for (const el of root.querySelectorAll("[class]")) {
    for (const name of el.classList) {
      const bare = name.split(":").pop() ?? name;
      if (LEGACY_CLASS.some((pattern) => pattern.test(bare)))
        found.add(`${el.tagName.toLowerCase()}.${name}`);
    }
  }
  for (const el of root.querySelectorAll("[style]")) {
    const style = el.getAttribute("style") ?? "";
    if (LEGACY_CLASS.some((pattern) => pattern.source.startsWith("var") && pattern.test(style)))
      found.add(`${el.tagName.toLowerCase()}[style=${style}]`);
  }
  return [...found].sort();
}

/** The body cells of the Radar tables under root that do not name their column for the phone card. */
export function unlabelledCells(root: ParentNode): string[] {
  return [...root.querySelectorAll("table[data-radar-table] tbody td:not([colspan])")]
    .filter((td) => !td.hasAttribute("data-label"))
    .map((td) => (td.textContent ?? "").trim().slice(0, 30));
}

/** Fails when what is rendered under root paints a legacy class or leaves a table cell unlabelled. */
export function expectTokensOnly(root: ParentNode = document.body): void {
  expect(legacyClasses(root), "legacy classes").toEqual([]);
  expect(unlabelledCells(root), "table cells without a data-label").toEqual([]);
}
