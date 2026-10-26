// theme-boot.test.ts — the boot script's exact shipped bytes, run the way a browser runs them: for
// every preference, OS answer and kept theme it writes the theme readTheme keeps and the same
// data-theme and data-scheme applyScheme paints, and its id list is the registry's.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { SCHEME_KEY, applyScheme, readScheme, type SchemePref, type SchemeStore } from "@huddle/ui/theme.ts";
import { THEMES, THEME_KEY, readTheme } from "@huddle/ui/themes.ts";

// the script ships as a plain file with no module form, so the test runs its exact bytes
const source = readFileSync(join(import.meta.dirname, "../public/theme-boot.js"), "utf8");

/** An in-memory store the way localStorage answers. */
function memory(seed: Record<string, string> = {}): SchemeStore {
  const data = new Map(Object.entries(seed));
  return {
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => {
      data.set(k, v);
    },
  };
}

/** The registered ids the script carries, read out of its own bytes. */
function bootIds(): string[] {
  const m = /const THEME_IDS = \[([^\]]*)\]/.exec(source);
  expect(m, "theme-boot.js carries its THEME_IDS list").toBeTruthy();
  return [...(m?.[1] ?? "").matchAll(/"([^"]+)"/g)].map((x) => x[1] ?? "");
}

/** Runs the script's bytes against the current document. */
function boot(): void {
  // oxlint-disable-next-line typescript/no-implied-eval -- the Function constructor is the only way to run those exact bytes
  new Function(source)();
}

/** A matchMedia whose prefers-color-scheme answers `dark`, every other query false. */
function stubSchemeMedia(dark: boolean): void {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: dark && q === "(prefers-color-scheme: dark)",
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("theme-boot.js", () => {
  it("carries the registry's ids", () => {
    expect(bootIds()).toEqual(THEMES.map((t) => t.id));
  });

  it("defines no global: the id list lives inside the IIFE", () => {
    boot();
    expect("THEME_IDS" in globalThis).toBe(false);
    const iife = source.indexOf("(function");
    expect(iife, "theme-boot.js wraps its work in an IIFE").toBeGreaterThan(-1);
    expect(source.indexOf("const THEME_IDS"), "the id list sits inside the IIFE").toBeGreaterThan(iife);
  });

  for (const pref of ["system", "light", "dark"] as const satisfies readonly SchemePref[])
    for (const osDark of [false, true])
      for (const kept of [null, "huddle", "nope"]) {
        it(`paints what applyScheme paints: ${pref}, os ${osDark ? "dark" : "light"}, kept ${kept ?? "none"}`, () => {
          stubSchemeMedia(osDark);
          localStorage.clear();
          if (pref !== "system") localStorage.setItem(SCHEME_KEY, pref);
          if (kept) localStorage.setItem(THEME_KEY, kept);
          boot();
          const seed: Record<string, string> = {};
          if (pref !== "system") seed[SCHEME_KEY] = pref;
          if (kept) seed[THEME_KEY] = kept;
          const store = memory(seed);
          const root = document.createElement("html");
          applyScheme(root, readScheme(store), osDark, store);
          expect(document.documentElement.dataset.theme).toBe(root.dataset.theme);
          expect(document.documentElement.dataset.scheme).toBe(root.dataset.scheme);
          expect(document.documentElement.dataset.theme).toBe(readTheme(store));
        });
      }
});
