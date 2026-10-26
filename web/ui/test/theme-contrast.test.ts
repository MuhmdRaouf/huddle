// theme-contrast.test.ts — the colour the pages paint keeps WCAG AA. tokens.css comes first: in
// every scheme it paints (each registered theme's side, and the Catppuccin flavour's latte and
// mocha) every ink is 4.5:1 on every surface and on its own soft fill laid over each surface, and
// every mark is 3:1 on every surface. A translucent soft is judged as composited: the fill over
// the surface under it, because that is the colour the eye reads. Then the looks theme.css ships:
// base-content on each base shade, and each colour's *-content on the colour. The ratios come from
// the WCAG relative-luminance formula, written here; no dependency.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";
import { THEMES, type ThemeSide } from "../src/themes.ts";

/** One colour as the WCAG formula reads it: 0-255 channels and the alpha it paints with. */
type Rgba = { r: number; g: number; b: number; a: number };

/** Reads the colour forms tokens.css declares: #rrggbb, and rgba() for translucent fills. */
function parse(colour: string): Rgba | undefined {
  const hex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/.exec(colour);
  if (hex) {
    const [r, g, b] = hex.slice(1).map((c) => Number.parseInt(c ?? "00", 16));
    return { r: r ?? 0, g: g ?? 0, b: b ?? 0, a: 1 };
  }
  const rgba = /^rgba\((\d+),\s*(\d+),\s*(\d+),\s*(\d+(?:\.\d+)?)\)$/.exec(colour);
  if (rgba) {
    const [r, g, b, a] = rgba.slice(1).map((c) => Number(c));
    return { r: r ?? 0, g: g ?? 0, b: b ?? 0, a: a ?? 1 };
  }
  return undefined;
}

/** WCAG relative luminance: the channel linearised, then the three weights. */
function luminance(c: Rgba): number {
  const [r, g, b] = [c.r, c.g, c.b]
    .map((v) => v / 255)
    .map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * (r ?? 0) + 0.7152 * (g ?? 0) + 0.0722 * (b ?? 0);
}

/** WCAG contrast: the lighter luminance over the darker, both lifted by 0.05. */
function contrast(a: Rgba, b: Rgba): number {
  const [hi = 0, lo = 0] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** A fill laid over the opaque colour under it: the colour the eye reads through the fill's alpha. */
function over(fill: Rgba, under: Rgba): Rgba {
  const mix = (f: number, u: number) => Math.round(f * fill.a + u * (1 - fill.a));
  return { r: mix(fill.r, under.r), g: mix(fill.g, under.g), b: mix(fill.b, under.b), a: 1 };
}

/* ---- tokens.css ---- */

/** One colour declaration: a light-dark(light, dark) pair, or another token it takes its colour from. */
type Colour = { kind: "pair"; light: string; dark: string } | { kind: "alias"; of: string };

/** The colour halves a pair holds: an opaque #rrggbb or a translucent rgba(). */
const HALF = "(#[0-9a-f]{6}|rgba\\(\\d+,\\s*\\d+,\\s*\\d+,\\s*\\d+(?:\\.\\d+)?\\))";

/** One scheme tokens.css paints: a registered theme's side, or the Catppuccin flavour over the plain. */
type Scheme = { id: string; side: ThemeSide; flavour: boolean };

/** Every registered theme's every side, then the flavour's two. */
const SCHEMES: readonly Scheme[] = [
  ...THEMES.flatMap((t): Scheme[] =>
    t.sides.map((side) => ({ id: `${t.id} ${side}`, side, flavour: false })),
  ),
  { id: "latte", side: "light", flavour: true },
  { id: "mocha", side: "dark", flavour: true },
];

/** The colour declarations of one rule body, keyed by token name without the --ui- prefix. */
function colours(body: string): Map<string, Colour> {
  const out = new Map<string, Colour>();
  const pair = new RegExp(`--ui-([a-z0-9-]+):\\s*light-dark\\(\\s*${HALF}\\s*,\\s*${HALF}\\s*\\)`, "g");
  for (const m of body.matchAll(pair))
    out.set(m[1] ?? "", { kind: "pair", light: m[2] ?? "", dark: m[3] ?? "" });
  for (const m of body.matchAll(/--ui-([a-z0-9-]+):\s*var\(--ui-([a-z0-9-]+)\)/g))
    out.set(m[1] ?? "", { kind: "alias", of: m[2] ?? "" });
  return out;
}

/** The body of the one rule the selector opens, or "" when tokens.css has none. */
function rule(css: string, selector: string): string {
  const at = css.indexOf(`${selector} {`);
  if (at < 0) return "";
  return css.slice(at, css.indexOf("}", at));
}

const CSS = readFileSync("src/tokens.css", "utf8");
const BASE = colours(rule(CSS, ":root"));
const FLAVOUR = colours(rule(CSS, ':root[data-flavour="catppuccin"]'));

/** A token's colour in a scheme: the flavour's declaration over the plain one, aliases followed. */
function colour(scheme: Scheme, token: string, seen: readonly string[] = []): Rgba | undefined {
  const decl = (scheme.flavour ? FLAVOUR.get(token) : undefined) ?? BASE.get(token);
  if (!decl || seen.includes(token)) return undefined;
  if (decl.kind === "alias") return colour(scheme, decl.of, [...seen, token]);
  return parse(decl[scheme.side]);
}

/** The surfaces every ink and mark sits on, and the backdrops a soft fill is composited over. */
const SURFACES = ["bg", "surface", "surface-2"] as const;
/** The four status tones: <tone> marks, <tone>-soft fills, <tone>-ink text. */
const TONES = ["ok", "warn", "fail", "info"] as const;
/** The states a page shows, each a mark, a soft fill and an ink like the tones. */
const ROLES = ["needs-you", "turn-done", "working", "idle", "failed", "cancelled"] as const;
const FILLED = [...TONES, ...ROLES] as const;

/** Text: 4.5:1 on every surface. */
const INKS = ["text", "muted", "accent", ...FILLED.map((t) => `${t}-ink`)] as const;
/** Marks and borders: 3:1 on every surface. */
const MARKS = ["line-strong", ...FILLED] as const;

/** Every pair the schemes keep, with its minimum. */
const PAIRS: readonly (readonly [fg: string, bg: string, min: number])[] = [
  ...INKS.flatMap((ink) => SURFACES.map((s) => [ink, s, 4.5] as const)),
  ...MARKS.flatMap((mark) => SURFACES.map((s) => [mark, s, 3] as const)),
  ...FILLED.map((t) => [`${t}-ink`, `${t}-soft`, 4.5] as const),
  ["on-accent", "accent", 4.5],
];

describe("tokens.css", () => {
  it("declares the surfaces, the inks, the marks, the tones and every state role", () => {
    const want = [
      ...SURFACES,
      "line",
      "line-strong",
      "muted",
      "text",
      "accent",
      "on-accent",
      ...FILLED.flatMap((t) => [t, `${t}-soft`, `${t}-ink`]),
    ];
    for (const token of want) expect(BASE.has(token), token).toBe(true);
  });

  it("reads every colour pair and alias the file declares, translucent or not", () => {
    const body = rule(CSS, ":root");
    const pairs = [...body.matchAll(/--ui-([a-z0-9-]+):\s*light-dark\(/g)].map((m) => m[1] ?? "");
    const aliases = [...body.matchAll(/--ui-([a-z0-9-]+):\s*var\(--ui-/g)].map((m) => m[1] ?? "");
    expect(pairs.length + aliases.length).toBeGreaterThan(0);
    for (const token of [...pairs, ...aliases]) {
      expect(BASE.has(token), `${token} is parsed`).toBe(true);
      for (const scheme of SCHEMES)
        expect(colour(scheme, token), `${token} resolves in ${scheme.id}`).toBeDefined();
    }
  });

  it("gives every state role its colour from a tone or a neutral, so a flavour remaps it once", () => {
    for (const role of ROLES)
      for (const token of [role, `${role}-soft`, `${role}-ink`])
        expect(BASE.get(token)?.kind, token).toBe("alias");
  });

  it("maps the Catppuccin flavour onto the plain tokens only, as light-dark pairs", () => {
    expect(FLAVOUR.size).toBeGreaterThan(0);
    for (const [token, decl] of FLAVOUR) {
      expect(BASE.has(token), `${token} is a plain token`).toBe(true);
      expect(decl.kind, token).toBe("pair");
    }
  });

  it.each(SCHEMES)("keeps WCAG AA in the $id scheme", (scheme) => {
    const misses: string[] = [];
    for (const [fg, bg, min] of PAIRS) {
      const ink = colour(scheme, fg);
      expect(ink, `${fg} in ${scheme.id}`).toBeDefined();
      if (!ink) continue;
      const fill = colour(scheme, bg);
      expect(fill, `${bg} in ${scheme.id}`).toBeDefined();
      if (!fill) continue;
      // a soft fill is translucent, so the pair it holds depends on the surface under it: judge
      // the ink against the fill laid over each surface it may sit on
      for (const s of bg.endsWith("-soft") ? SURFACES : [bg]) {
        const under = colour(scheme, s);
        expect(under, `${s} in ${scheme.id}`).toBeDefined();
        if (!under) continue;
        const ratio = contrast(ink, over(fill, under));
        if (ratio < min) misses.push(`${fg} on ${bg} over ${s}: ${ratio.toFixed(3)} < ${min}`);
      }
    }
    expect(misses, `${scheme.id}: every pair at its minimum`).toEqual([]);
  });
});

/* ---- theme.css ---- */

/** The twelve looks the shared css ships: six families, a dark and a light one each; latte's tokens
 *  come from the light side rule, the rest from daisyUI theme blocks. */
const VARIANTS = [
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

/** The daisyUI theme blocks of theme.css, keyed by the name each declares, plus the light side rule
 *  under "latte", whose tokens it carries. */
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
  const light = css.match(/:root\[data-scheme="light"\]\s*\{([^}]*)\}/)?.[1];
  if (light) {
    const tokens: Tokens = {};
    for (const decl of light.matchAll(/--color-([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})/g))
      tokens[decl[1] as (typeof TOKENS)[number]] = decl[2] ?? "";
    out.set("latte", tokens);
  }
  return out;
}

describe("theme.css", () => {
  it("ships the twelve variants: six families, a dark and a light one each", () => {
    expect([...readThemes().keys()].sort()).toEqual([...VARIANTS].sort());
  });

  it("declares every base, ink and status token in each variant", () => {
    for (const [name, tokens] of readThemes())
      for (const token of TOKENS) expect(tokens[token], `${name}: ${token}`).toBeDefined();
  });
});

describe.each(VARIANTS)("%s", (id) => {
  const tokens = readThemes().get(id) ?? {};

  it("keeps body text at WCAG AA on every base shade", () => {
    for (const base of ["base-100", "base-200", "base-300"] as const)
      expect(
        contrast(
          parse(tokens["base-content"] ?? "") ?? { r: 0, g: 0, b: 0, a: 1 },
          parse(tokens[base] ?? "") ?? { r: 0, g: 0, b: 0, a: 1 },
        ),
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
        contrast(
          parse(tokens[`${colour}-content`] ?? "") ?? { r: 0, g: 0, b: 0, a: 1 },
          parse(tokens[colour] ?? "") ?? { r: 0, g: 0, b: 0, a: 1 },
        ),
        `${id}: ${colour}`,
      ).toBeGreaterThanOrEqual(4.5);
  });
});
