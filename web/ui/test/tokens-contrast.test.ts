// tokens-contrast.test.ts — tokens.css keeps WCAG AA in both schemes: body and muted text on every
// neutral surface, the ink on the accent and on each status tint, and the solid status colours and
// the strong line at 3:1 for marks and borders. Each colour token is one light-dark(light, dark)
// pair, read straight from the css; the WCAG formula is written here, no dependency.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";

/** Every colour token tokens.css declares, as its light and its dark hex. */
function readPairs(): Map<string, readonly [string, string]> {
  const css = readFileSync("src/tokens.css", "utf8");
  const out = new Map<string, readonly [string, string]>();
  for (const m of css.matchAll(/--ui-([a-z0-9-]+):\s*light-dark\((#[0-9a-f]{6}),\s*(#[0-9a-f]{6})\)/g))
    out.set(m[1] ?? "", [m[2] ?? "", m[3] ?? ""]);
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

const SURFACES = ["bg", "surface", "surface-2"] as const;
const TONES = ["ok", "warn", "fail", "info"] as const;

/** Text needs 4.5:1; marks and borders 3:1. */
const PAIRS: readonly (readonly [fg: string, bg: string, min: number])[] = [
  ...SURFACES.flatMap((s) => [
    ["text", s, 4.5] as const,
    ["muted", s, 4.5] as const,
    ["accent", s, 4.5] as const,
    ["line-strong", s, 3] as const,
  ]),
  ["on-accent", "accent", 4.5],
  ...TONES.flatMap((t) => [
    [`${t}-ink`, `${t}-soft`, 4.5] as const,
    ...SURFACES.map((s) => [`${t}-ink`, s, 4.5] as const),
    ...SURFACES.map((s) => [t, s, 3] as const),
  ]),
];

describe("tokens.css", () => {
  const pairs = readPairs();

  it("declares the neutral scale, the accent and the four status colours", () => {
    const want = [
      ...SURFACES,
      "line",
      "line-strong",
      "muted",
      "text",
      "accent",
      "on-accent",
      ...TONES.flatMap((t) => [t, `${t}-soft`, `${t}-ink`]),
    ];
    for (const token of want) expect(pairs.has(token), token).toBe(true);
  });

  it.each([
    ["light", 0],
    ["dark", 1],
  ] as const)("keeps WCAG AA in the %s scheme", (_, side) => {
    for (const [fg, bg, min] of PAIRS) {
      const a = pairs.get(fg)?.[side] ?? "";
      const b = pairs.get(bg)?.[side] ?? "";
      expect(contrast(a, b), `${fg} on ${bg}`).toBeGreaterThanOrEqual(min);
    }
  });
});
