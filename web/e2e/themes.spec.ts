// themes.spec.ts — the community themes on the signed-in app: a page opened with a theme kept
// under "huddle:theme" paints <html data-theme> with that id before first paint, paints the
// theme's own background under .ui-shell on every side the theme has, carries the theme's own
// status bar colour in the theme-color meta, and a dark-only theme stays dark when the OS asks
// for light. The bar map doubles as the bg map: a theme's bar colour is its own background.

import { expect, test, type Page } from "@playwright/test";
import { THEMES, type Theme, type ThemeSide, themeOf } from "../ui/src/themes.ts";
import { signIn, theSetup } from "./helpers.ts";

/** A #rrggbb as computed style spells it. */
const rgb = (hex: string): string => {
  const n = Number.parseInt(hex.slice(1), 16);
  return `rgb(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255})`;
};

/** The bar colour a theme carries for one side; every side a theme paints has one. */
const barOf = (theme: Theme, side: ThemeSide): string => theme.bar[side] ?? "";

/** One paint of one theme: the scheme the OS asks for and the side the theme answers with. */
const CASES = THEMES.slice(1).flatMap((theme) =>
  theme.sides.length === 1
    ? [{ theme, emulate: "light" as ThemeSide, side: "dark" as ThemeSide }]
    : theme.sides.map((side) => ({ theme, emulate: side, side })),
);

/** Opens the signed-in app with the theme already kept, so theme-boot.js reads it pre-paint. */
async function openTheme(page: Page, id: string): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await page.addInitScript((t) => localStorage.setItem("huddle:theme", t), id);
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/`);
}

/** Opens the signed-in app with only the deployed build's flavour key kept. */
async function openFlavour(page: Page): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await page.addInitScript((f) => localStorage.setItem(f, "catppuccin"), "huddle:flavour");
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/`);
}

for (const { theme, emulate, side } of CASES) {
  const stays = emulate !== side;
  test(`${theme.label} paints its ${side} side${stays ? " whatever the scheme asks" : ""}`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: emulate });
    await openTheme(page, theme.id);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme.id);
    await expect(page.locator("html")).toHaveAttribute("data-scheme", side);
    const shell = await page.evaluate(
      () => getComputedStyle(document.querySelector(".ui-shell") as Element).backgroundColor,
    );
    expect(shell, `${theme.id}: .ui-shell paints the theme's own background`).toBe(rgb(barOf(theme, side)));
    // System keeps the OS-keyed pair on a two-sided theme; every other paint carries one colour
    // with no media, so the meta answering the active side is the theme's bar either way
    const bars = await page.evaluate((want) => {
      const metas = [...document.querySelectorAll('meta[name="theme-color"]')];
      const keyed = metas.filter((m) => m.media === `(prefers-color-scheme: ${want})`);
      return [...new Set((keyed.length > 0 ? keyed : metas).map((m) => m.content))];
    }, emulate);
    expect(bars).toEqual([barOf(theme, side)]);
  });
}

for (const side of ["light", "dark"] as const) {
  test(`a browser holding only the deployed build's flavour key paints Catppuccin's ${side} side`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: side });
    await openFlavour(page);
    const catppuccin = themeOf("catppuccin");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "catppuccin");
    await expect(page.locator("html")).toHaveAttribute("data-scheme", side);
    const shell = await page.evaluate(
      () => getComputedStyle(document.querySelector(".ui-shell") as Element).backgroundColor,
    );
    expect(shell, "catppuccin: .ui-shell paints the theme's own background").toBe(
      rgb(barOf(catppuccin, side)),
    );
  });
}
