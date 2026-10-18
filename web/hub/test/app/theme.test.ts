// theme.test.ts — the theme choice: a family (six, each a dark and a light variant) plus a mode,
// what each resolves to on <html>, and the migration of a choice kept under the old single-palette
// names ("mocha", "latte", "system", "dark", "light").
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import {
  applyTheme,
  currentTheme,
  DEFAULT_PREF,
  FAMILIES,
  MODES,
  resolvedTheme,
  setTheme,
} from "../../src/app/theme.ts";

/** A storage over a map, like the browser's but per test; only what readPref/writePref call. */
function store(seed: Record<string, string> = {}): Storage {
  const m = new Map(Object.entries(seed));
  return {
    getItem: (k) => (m.has(k) ? (m.get(k) as string) : null),
    setItem: (k, v) => {
      m.set(k, v);
    },
  } as Storage;
}

afterEach(() => {
  document.documentElement.dataset.theme = "";
});

describe("FAMILIES", () => {
  it("offers the six families in picker order, each with its label, variants and ids", () => {
    expect(FAMILIES.map((f) => [f.id, f.label, f.variants, f.dark.id, f.light.id])).toEqual([
      ["catppuccin", "Catppuccin", "Mocha · Latte", "mocha", "latte"],
      ["gruvbox", "Gruvbox", "Dark · Light", "gruvbox-dark", "gruvbox-light"],
      ["tokyo", "Tokyo", "Night · Day", "tokyo-night", "tokyo-day"],
      ["rose-pine", "Rosé Pine", "Main · Dawn", "rose-pine", "rose-pine-dawn"],
      ["nord", "Nord", "Polar · Snow", "nord", "nord-light"],
      ["solarized", "Solarized", "Dark · Light", "solarized-dark", "solarized-light"],
    ]);
  });

  it("names the twelve variants the shared theme.css ships, once each", () => {
    const ids = FAMILIES.flatMap((f) => [f.dark.id, f.light.id]);
    expect(new Set(ids).size).toBe(ids.length);
    expect([...ids].sort()).toEqual(
      [
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
      ].sort(),
    );
  });

  it("carries the swatch tokens the picker's halves show", () => {
    for (const f of FAMILIES)
      for (const v of [f.dark, f.light])
        for (const token of ["base", "content", "primary", "success", "warning"] as const)
          expect(v[token], `${f.id}.${v.id}.${token}`).toMatch(/^#[0-9a-f]{6}$/);
  });
});

describe("MODES", () => {
  it("offers System, Dark and Light, in segment order, with their icons", () => {
    expect(MODES.map(([k, l, i]) => [k, l, i])).toEqual([
      ["system", "System", "monitor"],
      ["dark", "Dark", "moon"],
      ["light", "Light", "sun"],
    ]);
  });
});

describe("resolvedTheme", () => {
  it("resolves every family's dark and light variant", () => {
    for (const f of FAMILIES) {
      expect(resolvedTheme({ family: f.id, mode: "dark" }, false)).toBe(f.dark.id);
      expect(resolvedTheme({ family: f.id, mode: "light" }, true)).toBe(f.light.id);
    }
  });

  it("follows the system only in system mode", () => {
    for (const f of FAMILIES) {
      expect(resolvedTheme({ family: f.id, mode: "system" }, true)).toBe(f.dark.id);
      expect(resolvedTheme({ family: f.id, mode: "system" }, false)).toBe(f.light.id);
    }
  });
});

describe("currentTheme", () => {
  it("reads the kept choice, falling back to Catppuccin following the system", () => {
    expect(DEFAULT_PREF).toEqual({ family: "catppuccin", mode: "system" });
    expect(currentTheme(store())).toEqual(DEFAULT_PREF);
    expect(currentTheme(store({ "huddle:theme": '{"family":"nord","mode":"light"}' }))).toEqual({
      family: "nord",
      mode: "light",
    });
    expect(currentTheme(store({ "huddle:theme": "nonsense" }))).toEqual(DEFAULT_PREF);
  });

  it("migrates every legacy single-palette name to its Catppuccin pref in the same read", () => {
    const cases: [string, string][] = [
      ['"mocha"', '{"family":"catppuccin","mode":"dark"}'],
      ['"dark"', '{"family":"catppuccin","mode":"dark"}'],
      ['"latte"', '{"family":"catppuccin","mode":"light"}'],
      ['"light"', '{"family":"catppuccin","mode":"light"}'],
      ['"system"', '{"family":"catppuccin","mode":"system"}'],
    ];
    for (const [kept, moved] of cases) {
      const s = store({ "huddle:theme": kept });
      expect(currentTheme(s), kept).toEqual(JSON.parse(moved));
      expect(s.getItem("huddle:theme"), kept).toBe(moved);
    }
  });

  it("falls back to the default on a malformed pref", () => {
    expect(currentTheme(store({ "huddle:theme": '{"family":"nope","mode":"dark"}' }))).toEqual(DEFAULT_PREF);
    expect(currentTheme(store({ "huddle:theme": '{"family":"nord"}' }))).toEqual(DEFAULT_PREF);
    expect(currentTheme(store({ "huddle:theme": '{"family":"nord","mode":"nope"}' }))).toEqual(DEFAULT_PREF);
  });
});

describe("applyTheme / setTheme", () => {
  it("puts the resolved variant's id on <html>", () => {
    applyTheme({ family: "tokyo", mode: "light" }, true);
    expect(document.documentElement.dataset.theme).toBe("tokyo-day");
    applyTheme({ family: "solarized", mode: "dark" }, false);
    expect(document.documentElement.dataset.theme).toBe("solarized-dark");
    applyTheme({ family: "gruvbox", mode: "system" }, true);
    expect(document.documentElement.dataset.theme).toBe("gruvbox-dark");
    applyTheme({ family: "gruvbox", mode: "system" }, false);
    expect(document.documentElement.dataset.theme).toBe("gruvbox-light");
  });

  it("keeps the choice and applies it in one step", () => {
    const s = store();
    setTheme(s, { family: "rose-pine", mode: "dark" }, false);
    expect(s.getItem("huddle:theme")).toBe('{"family":"rose-pine","mode":"dark"}');
    expect(document.documentElement.dataset.theme).toBe("rose-pine");
  });
});

describe("prefersDark", () => {
  it("reads the media query's answer", async () => {
    const { prefersDark } = await import("../../src/app/theme.ts");
    expect(prefersDark({ matches: true })).toBe(true);
    expect(prefersDark({ matches: false })).toBe(false);
    vi.restoreAllMocks();
  });
});
