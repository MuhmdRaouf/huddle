// theme-contrast.test.ts — the colour the pages paint keeps WCAG AA. tokens.css comes first: in each
// of its four schemes (light, dark, and the Catppuccin flavour's latte and mocha) every ink is 4.5:1
// on every surface and on its own tint, and every mark is 3:1 on every surface. Then the daisyUI
// variants theme.css still ships: base-content on each base shade, and each colour's *-content on
// the colour. The ratios come from the WCAG relative-luminance formula, written here; no dependency.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";

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

/* ---- tokens.css ---- */

/** One colour declaration: a light-dark(light, dark) pair, or another token it takes its colour from. */
type Colour = { kind: "pair"; light: string; dark: string } | { kind: "alias"; of: string };

/** The four schemes: the plain tokens in each scheme, and the Catppuccin flavour over them. */
const SCHEMES = [
  { id: "light", side: "light", flavour: false },
  { id: "dark", side: "dark", flavour: false },
  { id: "latte", side: "light", flavour: true },
  { id: "mocha", side: "dark", flavour: true },
] as const;

type Scheme = (typeof SCHEMES)[number];

/** The colour declarations of one rule body, keyed by token name without the --ui- prefix. */
function colours(body: string): Map<string, Colour> {
  const out = new Map<string, Colour>();
  for (const m of body.matchAll(/--ui-([a-z0-9-]+):\s*light-dark\((#[0-9a-f]{6}),\s*(#[0-9a-f]{6})\)/g))
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

/** A token's hex in a scheme: the flavour's declaration over the plain one, aliases followed. */
function hex(scheme: Scheme, token: string, seen: readonly string[] = []): string {
  const decl = (scheme.flavour ? FLAVOUR.get(token) : undefined) ?? BASE.get(token);
  if (!decl || seen.includes(token)) return "";
  if (decl.kind === "alias") return hex(scheme, decl.of, [...seen, token]);
  return decl[scheme.side];
}

/** The surfaces every ink and mark sits on. */
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
    for (const [fg, bg, min] of PAIRS) {
      const a = hex(scheme, fg);
      const b = hex(scheme, bg);
      expect(a, `${fg} in ${scheme.id}`).toMatch(/^#[0-9a-f]{6}$/);
      expect(b, `${bg} in ${scheme.id}`).toMatch(/^#[0-9a-f]{6}$/);
      expect(contrast(a, b), `${fg} on ${bg} in ${scheme.id}`).toBeGreaterThanOrEqual(min);
    }
  });
});

/* ---- theme.css ---- */

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
