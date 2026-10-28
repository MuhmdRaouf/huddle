// themes.test.ts — the theme registry: the id this browser keeps under "huddle:theme" (a missing,
// unknown or unreadable one answers the default), the write that keeps registered ids only, the
// side a theme paints when Appearance resolves the scheme, and the apply that paints a pick the
// way theme-boot.js paints the kept one at boot.
import { describe, expect, it } from "vite-plus/test";
import { applyTheme, THEMES, THEME_KEY, readTheme, sideOf, themeOf, writeTheme } from "../src/themes.ts";

/** The slice of localStorage a theme is read and written through. */
type ThemeStore = Pick<Storage, "getItem" | "setItem" | "removeItem">;

/** An in-memory store the way localStorage answers. */
function memory(seed: Record<string, string> = {}): ThemeStore & { data: Map<string, string> } {
  const data = new Map(Object.entries(seed));
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => {
      data.set(k, v);
    },
    removeItem: (k) => {
      data.delete(k);
    },
  };
}

/** A store whose every call throws, the way blocked site data does. */
const blocked: ThemeStore = {
  getItem: () => {
    throw new Error("blocked");
  },
  setItem: () => {
    throw new Error("blocked");
  },
  removeItem: () => {
    throw new Error("blocked");
  },
};

describe("themes", () => {
  it("registers Huddle first, then the twelve community themes with the sides each has", () => {
    expect(THEMES.map((t) => t.id)).toEqual([
      "huddle",
      "catppuccin",
      "tokyo-night",
      "monokai",
      "dracula",
      "nord",
      "gruvbox",
      "solarized",
      "rose-pine",
      "one-dark",
      "github",
      "everforest",
      "kanagawa",
    ]);
    expect(THEMES.map((t) => t.label)).toEqual([
      "Huddle",
      "Catppuccin",
      "Tokyo Night",
      "Monokai",
      "Dracula",
      "Nord",
      "Gruvbox",
      "Solarized",
      "Rosé Pine",
      "One Dark",
      "GitHub",
      "Everforest",
      "Kanagawa",
    ]);
    expect(THEMES.map((t) => t.sides)).toEqual([
      ["light", "dark"],
      ["light", "dark"],
      ["light", "dark"],
      ["dark"],
      ["dark"],
      ["light", "dark"],
      ["light", "dark"],
      ["light", "dark"],
      ["light", "dark"],
      ["dark"],
      ["light", "dark"],
      ["light", "dark"],
      ["light", "dark"],
    ]);
  });

  it("carries each theme's status bar colour for every side it paints", () => {
    expect(THEMES.map((t) => t.bar)).toEqual([
      { light: "#f4f5f7", dark: "#0b0c0f" },
      { light: "#e6e9ef", dark: "#11111b" },
      { light: "#d0d5e3", dark: "#16161e" },
      { dark: "#1e1f1c" },
      { dark: "#21222c" },
      { light: "#e5e9f0", dark: "#2e3440" },
      { light: "#f2e5bc", dark: "#1d2021" },
      { light: "#eee8d5", dark: "#00212b" },
      { light: "#f2e9e1", dark: "#191724" },
      { dark: "#21252b" },
      { light: "#f6f8fa", dark: "#010409" },
      { light: "#efebd4", dark: "#232a2e" },
      { light: "#e5ddb0", dark: "#16161d" },
    ]);
  });

  it("answers the theme an id names, and the default for an unknown one", () => {
    expect(themeOf("catppuccin")).toBe(THEMES[1]);
    expect(themeOf("dracula")).toBe(THEMES[4]);
    expect(themeOf("rose-pine")).toBe(THEMES[8]);
    expect(themeOf("one-dark")).toBe(THEMES[9]);
    expect(themeOf("kanagawa")).toBe(THEMES[12]);
    expect(themeOf("nope")).toBe(THEMES[0]);
  });

  it("answers the default when nothing, an unknown id or blocked storage is kept", () => {
    expect(readTheme(memory())).toBe("huddle");
    expect(readTheme(memory({ [THEME_KEY]: "huddle" }))).toBe("huddle");
    expect(readTheme(memory({ [THEME_KEY]: "nope" }))).toBe("huddle");
    expect(readTheme(blocked)).toBe("huddle");
  });

  it("reads the deployed build's flavour key as the catppuccin theme when no theme is kept", () => {
    expect(readTheme(memory({ "huddle:flavour": "catppuccin" }))).toBe("catppuccin");
    // a theme key wins over the flavour key, and a flavour of another name says nothing
    expect(readTheme(memory({ "huddle:flavour": "catppuccin", [THEME_KEY]: "nord" }))).toBe("nord");
    expect(readTheme(memory({ "huddle:flavour": "frappe" }))).toBe("huddle");
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

  it("retires the deployed build's flavour key when a pick is kept", () => {
    const store = memory({ "huddle:flavour": "catppuccin" });
    writeTheme(store, "tokyo-night");
    expect(store.data.has("huddle:flavour")).toBe(false);
    expect(store.data.get(THEME_KEY)).toBe("tokyo-night");
  });

  it("writes nothing on blocked storage", () => {
    expect(() => writeTheme(blocked, "huddle")).not.toThrow();
  });

  it("answers a one-sided theme its own side, and the resolved scheme otherwise", () => {
    expect(sideOf("huddle", "light")).toBe("light");
    expect(sideOf("huddle", "dark")).toBe("dark");
    expect(sideOf("catppuccin", "light")).toBe("light");
    expect(sideOf("catppuccin", "dark")).toBe("dark");
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
    expect(sideOf("one-dark", "light")).toBe("dark");
    expect(sideOf("one-dark", "dark")).toBe("dark");
    expect(sideOf("github", "light")).toBe("light");
    expect(sideOf("github", "dark")).toBe("dark");
    expect(sideOf("everforest", "light")).toBe("light");
    expect(sideOf("everforest", "dark")).toBe("dark");
    expect(sideOf("kanagawa", "light")).toBe("light");
    expect(sideOf("kanagawa", "dark")).toBe("dark");
    expect(sideOf("nope", "light")).toBe("light");
    expect(sideOf("nope", "dark")).toBe("dark");
  });

  describe("applyTheme", () => {
    /** The two theme-color metas index.html ships, rebuilt clean for each test. */
    function metas(): HTMLMetaElement[] {
      for (const m of document.head.querySelectorAll('meta[name="theme-color"]')) m.remove();
      return (["dark", "light"] as const).map((s) => {
        const m = document.createElement("meta");
        m.name = "theme-color";
        m.media = `(prefers-color-scheme: ${s})`;
        m.content = "#000000";
        document.head.append(m);
        return m;
      });
    }

    it("keeps the pick and answers System with the OS-keyed bar pair on a two-sided theme", () => {
      const store = memory();
      const ms = metas();
      applyTheme(document.documentElement, "nord", "system", false, store);
      expect(store.data.get(THEME_KEY)).toBe("nord");
      expect(document.documentElement.dataset.theme).toBe("nord");
      expect(document.documentElement.dataset.scheme).toBe("light");
      expect(ms.map((m) => m.content)).toEqual(["#2e3440", "#e5e9f0"]);
      expect(ms.map((m) => m.media)).toEqual([
        "(prefers-color-scheme: dark)",
        "(prefers-color-scheme: light)",
      ]);
    });

    it("paints one side's bar on every meta when Appearance holds a scheme", () => {
      const store = memory();
      const ms = metas();
      applyTheme(document.documentElement, "nord", "dark", false, store);
      expect(store.data.get(THEME_KEY)).toBe("nord");
      expect(document.documentElement.dataset.theme).toBe("nord");
      expect(document.documentElement.dataset.scheme).toBe("dark");
      expect(ms.map((m) => m.content)).toEqual(["#2e3440", "#2e3440"]);
    });

    it("paints a dark-only theme its own side whatever Appearance asks", () => {
      const store = memory();
      const ms = metas();
      applyTheme(document.documentElement, "monokai", "light", false, store);
      expect(document.documentElement.dataset.theme).toBe("monokai");
      expect(document.documentElement.dataset.scheme).toBe("dark");
      expect(ms.map((m) => m.content)).toEqual(["#1e1f1c", "#1e1f1c"]);
    });

    it("paints a dark-only theme one bar while Appearance is System", () => {
      const store = memory();
      const ms = metas();
      applyTheme(document.documentElement, "dracula", "system", true, store);
      expect(document.documentElement.dataset.scheme).toBe("dark");
      expect(ms.map((m) => m.content)).toEqual(["#21222c", "#21222c"]);
    });

    it("reads the OS's dark side while Appearance is System", () => {
      const store = memory();
      const ms = metas();
      applyTheme(document.documentElement, "catppuccin", "system", true, store);
      expect(document.documentElement.dataset.scheme).toBe("dark");
      expect(ms.map((m) => m.content)).toEqual(["#11111b", "#e6e9ef"]);
      expect(ms.map((m) => m.media)).toEqual([
        "(prefers-color-scheme: dark)",
        "(prefers-color-scheme: light)",
      ]);
    });

    it("changes nothing for an unknown id", () => {
      const store = memory({ [THEME_KEY]: "huddle" });
      const ms = metas();
      delete document.documentElement.dataset.theme;
      delete document.documentElement.dataset.scheme;
      applyTheme(document.documentElement, "nope", "dark", true, store);
      expect(store.data.get(THEME_KEY)).toBe("huddle");
      expect(document.documentElement.dataset.theme).toBeUndefined();
      expect(document.documentElement.dataset.scheme).toBeUndefined();
      expect(ms.map((m) => m.content)).toEqual(["#000000", "#000000"]);
      expect(ms.map((m) => m.media)).toEqual([
        "(prefers-color-scheme: dark)",
        "(prefers-color-scheme: light)",
      ]);
    });
  });
});
