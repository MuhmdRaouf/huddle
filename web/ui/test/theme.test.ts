import { describe, expect, it } from "vite-plus/test";
import {
  SCHEME_KEY,
  applyScheme,
  followScheme,
  readScheme,
  resolveScheme,
  type SchemeQuery,
  type SchemeStore,
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
    expect(applyScheme(root, "light", true)).toBe("light");
    expect(root.dataset.scheme).toBe("light");
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
});
