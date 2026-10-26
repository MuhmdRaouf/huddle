// tokens.test.ts — the non-colour tokens of tokens.css: the radii, the type scale with its mono
// step, the control heights under each pointer, the tap floors, the tab bar, and the card shadow,
// whose dark half sits under :root[data-scheme="dark"] because a shadow cannot sit in light-dark().
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vite-plus/test";

const css = readFileSync("src/tokens.css", "utf8");

/** The body of the first top-level `selector { … }` in tokens.css, braces matched. */
function block(selector: string): string {
  const start = css.indexOf(`\n${selector} {`);
  if (start < 0) throw new Error(`tokens.css names no ${selector} block`);
  let depth = 0;
  for (let i = start; i < css.length; i++) {
    if (css[i] === "{") depth += 1;
    else if (css[i] === "}") {
      depth -= 1;
      if (depth === 0) return css.slice(start, i + 1);
    }
  }
  throw new Error(`tokens.css never closes ${selector}`);
}

/** The value `name` is declared with in one block's body, its comments stripped. */
function declared(name: string, body: string): string {
  const clean = body.replace(/\/\*[\s\S]*?\*\//g, " ");
  const m = new RegExp(`${name}:\\s*([^;]+);`).exec(clean);
  if (!m?.[1]) throw new Error(`no ${name} declaration in the block`);
  return m[1].trim();
}

describe("the tokens", () => {
  it("cuts the corners at 8, 12 and 20 px, and rounds the pill", () => {
    const root = block(":root");
    expect(declared("--ui-radius-sm", root)).toBe("8px");
    expect(declared("--ui-radius", root)).toBe("12px");
    expect(declared("--ui-radius-lg", root)).toBe("20px");
    expect(declared("--ui-radius-full", root)).toBe("999px");
  });

  it("steps the type on the 15 px root, five text sizes and a mono step", () => {
    const root = block(":root");
    expect(declared("--ui-text-xs", root)).toBe("0.8rem");
    expect(declared("--ui-text-sm", root)).toBe("0.8667rem");
    expect(declared("--ui-text-md", root)).toBe("1rem");
    expect(declared("--ui-text-lg", root)).toBe("1.1333rem");
    expect(declared("--ui-text-xl", root)).toBe("2rem");
    expect(declared("--ui-text-mono", root)).toBe("0.8333rem");
    expect(declared("--ui-leading", root)).toBe("1.45");
  });

  it("keeps the tap floors and sizes a control for the pointer", () => {
    const root = block(":root");
    expect(declared("--ui-tap", root)).toBe("44px");
    expect(declared("--ui-tap-lg", root)).toBe("48px");
    expect(declared("--ui-control-h", root)).toBe("48px");
    expect(declared("--ui-control-h", block("@media (pointer: fine)"))).toBe("40px");
  });

  it("pins the tab bar at 50 px above the bottom inset", () => {
    expect(declared("--ui-tabbar-h", block(":root"))).toBe("calc(50px + var(--ui-safe-bottom))");
  });

  it("lifts a raised card with the scheme's own shadow, out where light-dark() cannot reach", () => {
    expect(declared("--ui-shadow-1", block(":root"))).toBe(
      "0 1px 2px rgb(16 24 40 / 0.06), 0 1px 3px rgb(16 24 40 / 0.04)",
    );
    expect(declared("--ui-shadow-1", block(':root[data-scheme="dark"]'))).toBe(
      "0 1px 0 rgb(255 255 255 / 0.03) inset",
    );
  });
});
