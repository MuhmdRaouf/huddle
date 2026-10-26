// themes.test.ts — the theme registry: the id this browser keeps under "huddle:theme" (a missing,
// unknown or unreadable one answers the default), the write that keeps registered ids only, and the
// side a theme paints when Appearance resolves the scheme.
import { describe, expect, it } from "vite-plus/test";
import type { SchemeStore } from "../src/theme.ts";
import { THEMES, THEME_KEY, readTheme, sideOf, writeTheme } from "../src/themes.ts";

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
  it("registers Huddle first, with both sides", () => {
    expect(THEMES.map((t) => t.id)).toEqual(["huddle"]);
    expect(THEMES[0]?.label).toBe("Huddle");
    expect(THEMES[0]?.sides).toEqual(["light", "dark"]);
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
    writeTheme(store, "nope");
    expect(store.data.get(THEME_KEY)).toBe("huddle");
  });

  it("writes nothing on blocked storage", () => {
    expect(() => writeTheme(blocked, "huddle")).not.toThrow();
  });

  it("answers a one-sided theme its own side, and the resolved scheme otherwise", () => {
    expect(sideOf("huddle", "light")).toBe("light");
    expect(sideOf("huddle", "dark")).toBe("dark");
    expect(sideOf("nope", "light")).toBe("light");
    expect(sideOf("nope", "dark")).toBe("dark");
  });
});
