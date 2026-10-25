import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";
import {
  FLAVOUR_KEY,
  SCHEME_KEY,
  applyFlavour,
  applyScheme,
  followScheme,
  readFlavour,
  readScheme,
  resolveScheme,
  type SchemeQuery,
  type SchemeStore,
  writeFlavour,
  writeScheme,
} from "../src/theme.ts";

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

/** A prefers-color-scheme query the test flips by hand. */
function query(dark: boolean): SchemeQuery & { flip: (to: boolean) => void; listeners: number } {
  const fns = new Set<() => void>();
  const q = {
    matches: dark,
    listeners: 0,
    addEventListener: (_: "change", fn: () => void) => {
      fns.add(fn);
      q.listeners = fns.size;
    },
    removeEventListener: (_: "change", fn: () => void) => {
      fns.delete(fn);
      q.listeners = fns.size;
    },
    flip: (to: boolean) => {
      q.matches = to;
      for (const fn of fns) fn();
    },
  };
  return q;
}

/** The --ui-bg pair tokens.css paints the shell with, the colour under the status bar. */
function shellBg(): { light: string; dark: string } {
  const css = readFileSync("src/tokens.css", "utf8");
  const m = /--ui-bg: light-dark\((#[0-9a-f]{6}), (#[0-9a-f]{6})\);/.exec(css);
  if (!m?.[1] || !m[2]) throw new Error("tokens.css names no --ui-bg pair");
  return { light: m[1], dark: m[2] };
}

/** Puts the two theme-color metas index.html ships into the document's head. */
function themeColours(): HTMLMetaElement[] {
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

describe("theme", () => {
  it("follows the OS when nothing is stored", () => {
    expect(readScheme(memory())).toBe("system");
    expect(resolveScheme("system", true)).toBe("dark");
    expect(resolveScheme("system", false)).toBe("light");
  });

  it("takes a stored override over the OS", () => {
    expect(resolveScheme("light", true)).toBe("light");
    expect(resolveScheme("dark", false)).toBe("dark");
    expect(readScheme(memory({ [SCHEME_KEY]: "dark" }))).toBe("dark");
    expect(readScheme(memory({ [SCHEME_KEY]: "light" }))).toBe("light");
  });

  it("reads an unknown value or blocked storage as the OS choice", () => {
    expect(readScheme(memory({ [SCHEME_KEY]: "sepia" }))).toBe("system");
    expect(readScheme(blocked)).toBe("system");
    expect(() => writeScheme(blocked, "dark")).not.toThrow();
  });

  it("keeps the override across reads", () => {
    const store = memory();
    writeScheme(store, "light");
    expect(store.data.get(SCHEME_KEY)).toBe("light");
    expect(readScheme(store)).toBe("light");
    writeScheme(store, "system");
    expect(readScheme(store)).toBe("system");
  });

  it("puts the resolved scheme on the root element", () => {
    const root = document.createElement("html");
    expect(applyScheme(root, "system", true)).toBe("dark");
    expect(root.dataset.scheme).toBe("dark");
    expect(root.dataset.theme).toBe("mocha");
    expect(applyScheme(root, "light", true)).toBe("light");
    expect(root.dataset.scheme).toBe("light");
    expect(root.dataset.theme).toBe("latte");
  });

  it("paints the status bar in the chosen scheme, whatever the OS, and gives System back the OS pair", () => {
    const bg = shellBg();
    const metas = themeColours();
    const root = document.documentElement;
    applyScheme(root, "dark", false);
    for (const m of metas) {
      expect(m.content).toBe(bg.dark);
      expect(m.hasAttribute("media")).toBe(false);
    }
    applyScheme(root, "light", true);
    for (const m of metas) {
      expect(m.content).toBe(bg.light);
      expect(m.hasAttribute("media")).toBe(false);
    }
    applyScheme(root, "system", true);
    expect(metas.map((m) => [m.media, m.content])).toEqual([
      ["(prefers-color-scheme: dark)", bg.dark],
      ["(prefers-color-scheme: light)", bg.light],
    ]);
  });

  it("re-applies when the OS flips, until stopped, and an override ignores the flip", () => {
    const root = document.createElement("html");
    const store = memory();
    const q = query(false);
    const stop = followScheme(root, store, q);
    expect(root.dataset.scheme).toBe("light");
    q.flip(true);
    expect(root.dataset.scheme).toBe("dark");
    writeScheme(store, "light");
    q.flip(false);
    q.flip(true);
    expect(root.dataset.scheme).toBe("light");
    stop();
    expect(q.listeners).toBe(0);
  });

  it("keeps the flavour: plain unless Catppuccin is stored, and plain on blocked storage", () => {
    const store = memory();
    expect(readFlavour(store)).toBe("plain");
    writeFlavour(store, "catppuccin");
    expect(store.data.get(FLAVOUR_KEY)).toBe("catppuccin");
    expect(readFlavour(store)).toBe("catppuccin");
    expect(readFlavour(memory({ [FLAVOUR_KEY]: "frappe" }))).toBe("plain");
    expect(readFlavour(blocked)).toBe("plain");
    expect(() => writeFlavour(blocked, "catppuccin")).not.toThrow();
  });

  it("puts the flavour on the root element as data-flavour, and plain takes it off", () => {
    const root = document.createElement("html");
    applyFlavour(root, "catppuccin");
    expect(root.dataset.flavour).toBe("catppuccin");
    applyFlavour(root, "plain");
    expect(root.hasAttribute("data-flavour")).toBe(false);
  });

  it("applies the kept flavour with the scheme, whatever the scheme", () => {
    const root = document.createElement("html");
    const stop = followScheme(
      root,
      memory({ [FLAVOUR_KEY]: "catppuccin", [SCHEME_KEY]: "light" }),
      query(true),
    );
    expect(root.dataset.flavour).toBe("catppuccin");
    expect(root.dataset.scheme).toBe("light");
    stop();
  });
});
