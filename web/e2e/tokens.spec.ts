// tokens.spec.ts — the design system's own page, #/dev/tokens: every web/ui component on the
// tokens, checked on the phone and the desktop project. It must fit the viewport with no
// horizontal scroll, keep every tap target at least 44 px, pass axe in the light and the dark
// scheme, follow the OS scheme, and keep the switch's override across a reload.

import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { signIn, theSetup } from "./helpers.ts";

/** The page's blocks, one per token group and component. */
const BLOCKS = [
  "Colors",
  "Type",
  "Spacing",
  "Buttons",
  "Badges",
  "Card",
  "List",
  "Tabs",
  "Table",
  "Field",
  "Chart frame",
  "Sheet",
] as const;

/** Opens the tokens page in a signed-in browser. The authenticator set-up the first sign-in offers
 *  is not this page's subject, so the browser starts with its "Later" already kept. */
async function openTokens(page: Page): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/dev/tokens`);
  await expect(page.getByRole("heading", { level: 1, name: "Design tokens" })).toBeVisible();
}

test.describe("the design tokens page", () => {
  test("renders every component", async ({ page }) => {
    await openTokens(page);
    for (const name of BLOCKS) await expect(page.getByRole("heading", { level: 2, name })).toBeVisible();
    await expect(page.getByRole("button", { name: "Approve" })).toBeVisible();
    await expect(page.getByRole("table", { name: "Cost by model" })).toBeVisible();
    await expect(page.getByRole("tablist", { name: "Range" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Channel name" })).toBeVisible();
    await expect(page.getByRole("figure", { name: /Tokens/ })).toBeVisible();
  });

  test("fits the viewport with no horizontal scroll", async ({ page }) => {
    await openTokens(page);
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over).toBeLessThanOrEqual(0);
  });

  test("keeps every tap target at least 44 px", async ({ page }) => {
    await openTokens(page);
    const small = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>("main button, main input, main [role=tab], main a")]
        .map((el) => ({ el, r: el.getBoundingClientRect() }))
        .filter(({ r }) => r.width > 0 && (r.width < 44 || r.height < 44))
        .map(({ el, r }) => {
          const name = el.textContent?.trim() || el.getAttribute("aria-label");
          return `${el.tagName} "${name}" ${r.width}x${r.height}`;
        }),
    );
    expect(small).toEqual([]);
  });

  for (const scheme of ["light", "dark"] as const) {
    test(`passes axe in the ${scheme} scheme`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme });
      await openTokens(page);
      await expect(page.locator("html")).toHaveAttribute("data-scheme", scheme);
      await expectAccessible(page);
    });
  }

  test("the switch overrides the OS and keeps the choice across a reload", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await openTokens(page);
    await page.getByRole("button", { name: "Dark" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-scheme", "dark");
    await page.reload();
    await expect(page.getByRole("heading", { level: 1, name: "Design tokens" })).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("data-scheme", "dark");
    await expect(page.getByRole("button", { name: "Dark" })).toHaveAttribute("aria-pressed", "true");
    await page.getByRole("button", { name: "System" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-scheme", "light");
  });

  test("the sheet opens over the page and closes on Escape", async ({ page }) => {
    await openTokens(page);
    await page.getByRole("button", { name: "Open sheet" }).click();
    const sheet = page.getByRole("dialog", { name: "Task 42" });
    await expect(sheet).toBeVisible();
    const box = await sheet.locator(".ui-sheet-panel").boundingBox();
    const view = page.viewportSize();
    expect(box && view && box.x >= 0 && box.x + box.width <= view.width + 0.5).toBe(true);
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
  });
});
