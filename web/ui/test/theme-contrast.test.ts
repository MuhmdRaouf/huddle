// theme-contrast.test.ts — every variant meets WCAG AA for body text: base-content on each base
// shade, and every colour's *-content on the colour itself. The ratios come from the WCAG
// relative-luminance formula, written here; no dependency is pulled in for it.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";

/** The twelve variants the shared css ships: six families, a dark and a light one each. */
const THEMES = [
  "mocha",
  "latte",
  "gruvbox-dark",
  "gruvbox-light",
  "tokyo-night",
  "tokyo-day",
  "rose-pine",
  "rose-pine-dawn",
  "nord",
  "nord-light",
  "solarized-dark",
  "solarized-light",
] as const;

/** The tokens every variant declares: the bases, their ink, and the eight status colours with theirs. */
const TOKENS = [
  "base-100",
  "base-200",
  "base-300",
  "base-content",
  ...["primary", "secondary", "accent", "neutral", "info", "success", "warning", "error"].flatMap((c) => [
    c,
    `${c}-content`,
  ]),
] as const;

/** One variant's declarations, read straight from the css: token name to #rrggbb. */
type Tokens = Partial<Record<(typeof TOKENS)[number], string>>;

/** The daisyUI theme blocks of theme.css, keyed by the name each declares. */
function readThemes(): Map<string, Tokens> {
  const css = readFileSync("src/theme.css", "utf8");
  const out = new Map<string, Tokens>();
  for (const block of css.matchAll(/@plugin "daisyui\/theme"\s*\{([^}]*)\}/g)) {
    const body = block[1] ?? "";
    const name = body.match(/name:\s*"([^"]+)"/)?.[1];
    if (!name) continue;
    const tokens: Tokens = {};
    for (const decl of body.matchAll(/--color-([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})/g))
      tokens[decl[1] as (typeof TOKENS)[number]] = decl[2] ?? "";
    out.set(name, tokens);
  }
  return out;
}

/** WCAG relative luminance: the channel linearised, then the three weights. */
function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5]
    .map((i) => Number.parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * (r ?? 0) + 0.7152 * (g ?? 0) + 0.0722 * (b ?? 0);
}

/** WCAG contrast: the lighter luminance over the darker, both lifted by 0.05. */
function contrast(a: string, b: string): number {
  const [hi = 0, lo = 0] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

describe("theme.css", () => {
  it("ships the twelve variants: six families, a dark and a light one each", () => {
    expect([...readThemes().keys()].sort()).toEqual([...THEMES].sort());
  });

  it("declares every base, ink and status token in each variant", () => {
    for (const [name, tokens] of readThemes())
      for (const token of TOKENS) expect(tokens[token], `${name}: ${token}`).toBeDefined();
  });
});

describe.each(THEMES)("%s", (id) => {
  const tokens = readThemes().get(id) ?? {};

  it("keeps body text at WCAG AA on every base shade", () => {
    for (const base of ["base-100", "base-200", "base-300"] as const)
      expect(
        contrast(tokens["base-content"] ?? "", tokens[base] ?? ""),
        `${id} on ${base}`,
      ).toBeGreaterThanOrEqual(4.5);
  });

  it("keeps every colour's ink at WCAG AA on the colour itself", () => {
    for (const colour of [
      "primary",
      "secondary",
      "accent",
      "neutral",
      "info",
      "success",
      "warning",
      "error",
    ] as const)
      expect(
        contrast(tokens[`${colour}-content`] ?? "", tokens[colour] ?? ""),
        `${id}: ${colour}`,
      ).toBeGreaterThanOrEqual(4.5);
  });
});
