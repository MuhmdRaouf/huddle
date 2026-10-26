// @vitest-environment node
// tailwind-scale.test.ts — Tailwind's scales are the design system's: the built hub stylesheet points
// Tailwind's text steps, spacing unit and radii at the --ui-* tokens of web/ui/src/tokens.css, and no
// default palette colour or off-scale type step reaches the built CSS. daisyUI's own colours and radii
// (--color-base-100, --radius-field, …) are not Tailwind defaults and stay. The stylesheet is build
// output, so this file runs in Node, not happy-dom, and reads the one app.<hash>.css scripts/build.mjs
// writes into internal/huddle/web/dist.
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

const DIST = fileURLToPath(new URL("../../../internal/huddle/web/dist", import.meta.url));

/** The text of the one built app stylesheet. */
function appCss(): string {
  const names = readdirSync(DIST).filter((name) => /^app\.[\w-]+\.css$/.test(name));
  if (names.length !== 1) throw new Error(`want one app.<hash>.css in ${DIST}, found ${names.length}`);
  return readFileSync(`${DIST}/${names[0]}`, "utf8");
}

/** Tailwind's default colour families; each must be all `initial` in the theme, so none builds. */
const FAMILIES = [
  "amber",
  "blue",
  "cyan",
  "emerald",
  "fuchsia",
  "gray",
  "green",
  "indigo",
  "lime",
  "mauve",
  "mist",
  "neutral",
  "olive",
  "orange",
  "pink",
  "purple",
  "red",
  "rose",
  "sky",
  "slate",
  "stone",
  "taupe",
  "teal",
  "violet",
  "yellow",
  "zinc",
] as const;

describe("the built stylesheet's Tailwind scales", () => {
  it("points every text step at the type scale", () => {
    const css = appCss();
    // Tailwind's `base` step is the scale's md; the five steps keep Tailwind's own line heights.
    const steps = [
      ["xs", "--ui-text-xs"],
      ["sm", "--ui-text-sm"],
      ["base", "--ui-text-md"],
      ["lg", "--ui-text-lg"],
      ["xl", "--ui-text-xl"],
    ] as const;
    for (const [step, token] of steps)
      expect(css.includes(`--text-${step}:var(${token})`), `--text-${step} in the built CSS`).toBe(true);
  });

  it("keeps every off-scale text step out of the CSS", () => {
    const css = appCss();
    for (const off of ["2xl", "3xl", "4xl", "5xl"]) {
      expect(css.includes(`--text-${off}:`), `--text-${off} in the built CSS`).toBe(false);
      expect(css.includes(`.text-${off}{`), `.text-${off} in the built CSS`).toBe(false);
    }
  });

  it("points the spacing unit at the spacing ladder", () => {
    expect(appCss().includes("--spacing:var(--ui-space-1)"), "--spacing in the built CSS").toBe(true);
  });

  it("points every radius step at the radii", () => {
    const css = appCss();
    // Tailwind emits a step's variable only when a utility uses it; xl has no rounded-xl use yet,
    // so the built CSS holds the four steps the pages reach. xl maps in the theme regardless.
    const steps = [
      ["sm", "--ui-radius-sm"],
      ["md", "--ui-radius-sm"],
      ["lg", "--ui-radius"],
      ["2xl", "--ui-radius-lg"],
    ] as const;
    for (const [step, token] of steps)
      expect(css.includes(`--radius-${step}:var(${token})`), `--radius-${step} in the built CSS`).toBe(true);
  });

  it("keeps no default palette colour", () => {
    const css = appCss();
    for (const family of FAMILIES)
      expect(new RegExp(`--color-${family}-\\d`).test(css), `--color-${family}-* in the built CSS`).toBe(
        false,
      );
    // the two bare anchors count too; the spelling demands a declaration, because daisyUI's own
    // status component keeps a var() reference whose variable no longer exists
    for (const bare of ["--color-white:", "--color-black:"])
      expect(css.includes(bare), `${bare} in the built CSS`).toBe(false);
  });
});
