// themes.test.ts — the theme registry: the id this browser keeps under "huddle:theme" (a missing,
// unknown or unreadable one answers the default), the write that keeps registered ids only, and the
// side a theme paints when Appearance resolves the scheme.
import { describe, expect, it } from "vite-plus/test";
import type { SchemeStore } from "../src/theme.ts";
import { THEMES, THEME_KEY, readTheme, sideOf, themeOf, writeTheme } from "../src/themes.ts";

/** An in-memory store the way localStorage answers. */
function memory(seed: Record<string, string> = {}): SchemeStore & { data: Map<string, string> } {
  const data = new Map(Object.entries(seed));
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => {
      data.set(k, v);
    },
  };
}

/** A store whose every call throws, the way blocked site data does. */
const blocked: SchemeStore = {
  getItem: () => {
    throw new Error("blocked");
  },
  setItem: () => {
    throw new Error("blocked");
  },
};

describe("themes", () => {
  it("registers Huddle first, then the seven community themes with the sides each has", () => {
    expect(THEMES.map((t) => t.id)).toEqual([
      "huddle",
      "tokyo-night",
      "monokai",
      "dracula",
      "nord",
      "gruvbox",
      "solarized",
      "rose-pine",
    ]);
    expect(THEMES.map((t) => t.label)).toEqual([
      "Huddle",
      "Tokyo Night",
      "Monokai",
      "Dracula",
      "Nord",
      "Gruvbox",
      "Solarized",
      "Rosé Pine",
    ]);
    expect(THEMES.map((t) => t.sides)).toEqual([
      ["light", "dark"],
      ["light", "dark"],
      ["dark"],
      ["dark"],
      ["light", "dark"],
      ["light", "dark"],
      ["light", "dark"],
      ["light", "dark"],
    ]);
  });

  it("carries each theme's status bar colour for every side it paints", () => {
    expect(THEMES.map((t) => t.bar)).toEqual([
      { light: "#f4f5f7", dark: "#0b0c0f" },
      { light: "#d0d5e3", dark: "#16161e" },
      { dark: "#1e1f1c" },
      { dark: "#21222c" },
      { light: "#e5e9f0", dark: "#2e3440" },
      { light: "#f2e5bc", dark: "#1d2021" },
      { light: "#eee8d5", dark: "#00212b" },
      { light: "#f2e9e1", dark: "#191724" },
    ]);
  });

  it("answers the theme an id names, and the default for an unknown one", () => {
    expect(themeOf("dracula")).toBe(THEMES[3]);
    expect(themeOf("rose-pine")).toBe(THEMES[7]);
    expect(themeOf("nope")).toBe(THEMES[0]);
  });

  it("answers the default when nothing, an unknown id or blocked storage is kept", () => {
    expect(readTheme(memory())).toBe("huddle");
    expect(readTheme(memory({ [THEME_KEY]: "huddle" }))).toBe("huddle");
    expect(readTheme(memory({ [THEME_KEY]: "nope" }))).toBe("huddle");
    expect(readTheme(blocked)).toBe("huddle");
  });

  it("keeps a registered id, and an unknown one writes nothing", () => {
    const store = memory();
    writeTheme(store, "huddle");
    expect(store.data.get(THEME_KEY)).toBe("huddle");
    expect(readTheme(store)).toBe("huddle");
    writeTheme(store, "tokyo-night");
    expect(store.data.get(THEME_KEY)).toBe("tokyo-night");
    expect(readTheme(store)).toBe("tokyo-night");
    writeTheme(store, "nope");
    expect(store.data.get(THEME_KEY)).toBe("tokyo-night");
  });

  it("writes nothing on blocked storage", () => {
    expect(() => writeTheme(blocked, "huddle")).not.toThrow();
  });

  it("answers a one-sided theme its own side, and the resolved scheme otherwise", () => {
    expect(sideOf("huddle", "light")).toBe("light");
    expect(sideOf("huddle", "dark")).toBe("dark");
    expect(sideOf("tokyo-night", "light")).toBe("light");
    expect(sideOf("tokyo-night", "dark")).toBe("dark");
    expect(sideOf("nord", "light")).toBe("light");
    expect(sideOf("nord", "dark")).toBe("dark");
    expect(sideOf("gruvbox", "light")).toBe("light");
    expect(sideOf("gruvbox", "dark")).toBe("dark");
    expect(sideOf("solarized", "light")).toBe("light");
    expect(sideOf("solarized", "dark")).toBe("dark");
    expect(sideOf("rose-pine", "light")).toBe("light");
    expect(sideOf("rose-pine", "dark")).toBe("dark");
    expect(sideOf("monokai", "light")).toBe("dark");
    expect(sideOf("monokai", "dark")).toBe("dark");
    expect(sideOf("dracula", "light")).toBe("dark");
    expect(sideOf("dracula", "dark")).toBe("dark");
    expect(sideOf("nope", "light")).toBe("light");
    expect(sideOf("nope", "dark")).toBe("dark");
  });
});
