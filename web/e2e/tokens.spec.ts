// tokens.spec.ts — the design system's own page, #/dev/tokens: every web/ui component on the
// tokens, checked on the phone and the desktop project. It must fit the viewport with no
// horizontal scroll, keep every tap target at least 44 px under a finger, pass axe in the light
// and the dark scheme, follow the OS scheme, and keep the switch's override across a reload. The
// tokens themselves compute for the pointer and the motion setting, and the page runs the system
// font.
// Tailwind's utilities compute from the tokens too: a text-sm element matches --ui-text-sm and a
// p-4 element pads four --ui-space-1.

import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { signIn, theSetup } from "./helpers.ts";

/** The page's blocks, one per token group and component. */
const BLOCKS = [
  "Colors",
  "Type",
  "Spacing",
  "Layers, shadows and motion",
  "Buttons",
  "Badges",
  "Card",
  "List",
  "Tabs",
  "Table",
  "Field",
  "Chart frame",
  "Sheet",
  "Dialog",
] as const;

/** A CSS time in milliseconds: the built stylesheet minifies 180 ms to .18 s and 0 ms to 0 s, so a
 *  duration is compared by value, not by the unit the minifier picked. */
const ms = (time: string): number =>
  time.endsWith("ms") ? Number.parseFloat(time) : Number.parseFloat(time) * 1000;

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
    // B's desk controls stand 40 px, so the 44 px floor holds under a finger only
    test.skip(!test.info().project.use.hasTouch, "the 44 px floor is a touch rule");
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

  test("the tokens compute for the pointer and the motion setting", async ({ page }) => {
    const fine = test.info().project.name === "desktop";
    await page.emulateMedia({ reducedMotion: "reduce" });
    await openTokens(page);
    const token = (name: string): Promise<string> =>
      page.evaluate((n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim(), name);
    expect(await token("--ui-control-h")).toBe(fine ? "40px" : "48px");
    expect(await token("--ui-layer-toast")).toBe("50");
    expect(ms(await token("--ui-dur"))).toBe(0);
    await page.emulateMedia({ reducedMotion: "no-preference" });
    expect(ms(await token("--ui-dur"))).toBe(180);
    const fonts = await page.evaluate(async () => {
      await document.fonts.ready;
      return {
        body: getComputedStyle(document.body).fontFamily,
        families: [...document.fonts].map((face) => face.family),
      };
    });
    expect(fonts.body).not.toMatch(/Plex|IBM/);
    expect(fonts.families.filter((family) => /Plex/i.test(family))).toEqual([]);
  });

  test("a text-sm and a p-4 element compute from the tokens", async ({ page }) => {
    await openTokens(page);
    // The route renders the component blocks alone, so the two Tailwind specimens are injected
    // here and measured against probes that resolve the same token straight from :root.
    const got = await page.evaluate(() => {
      const specimen = (cls: string): HTMLElement => {
        const el = document.createElement("div");
        el.className = cls;
        document.body.append(el);
        return el;
      };
      // The dashboard's CSP allows no style attributes, so the probes take their declarations
      // through the CSSOM, which the browser does not strip.
      const probe = (run: (el: HTMLElement) => void): HTMLElement => {
        const el = document.createElement("div");
        run(el);
        document.body.append(el);
        return el;
      };
      const px = (el: HTMLElement, side: keyof CSSStyleDeclaration): number =>
        Number.parseFloat(String(getComputedStyle(el)[side]));
      const sm = specimen("text-sm");
      const p4 = specimen("p-4");
      const smProbe = probe((el) => {
        el.style.fontSize = "var(--ui-text-sm)";
      });
      const p4Probe = probe((el) => {
        el.style.padding = "calc(var(--ui-space-1) * 4)";
      });
      const out = {
        sm: px(sm, "fontSize"),
        smWant: px(smProbe, "fontSize"),
        p4: px(p4, "paddingTop"),
        p4Want: px(p4Probe, "paddingTop"),
      };
      for (const el of [sm, p4, smProbe, p4Probe]) el.remove();
      return out;
    });
    expect(got.sm, `text-sm is ${got.sm}px, --ui-text-sm is ${got.smWant}px`).toBeCloseTo(got.smWant);
    expect(got.p4, `p-4 pads ${got.p4}px, --ui-space-1 * 4 is ${got.p4Want}px`).toBeCloseTo(got.p4Want);
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

  test("the dialog is a bottom sheet on the phone and centred on the desktop", async ({ page }) => {
    await openTokens(page);
    await page.getByRole("button", { name: "Open dialog" }).click();
    const dlg = page.getByRole("dialog", { name: "Remove channel" });
    await expect(dlg).toBeVisible();
    await expectAccessible(page);
    const box = await dlg.boundingBox();
    const view = page.viewportSize();
    expect(box && view).toBeTruthy();
    if (!box || !view) return;
    if (test.info().project.name === "desktop") {
      expect(Math.abs(box.x * 2 + box.width - view.width)).toBeLessThanOrEqual(1);
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(view.width + 0.5);
    } else {
      expect(box.x).toBe(0);
      expect(box.width).toBe(view.width);
      expect(Math.abs(box.y + box.height - view.height)).toBeLessThanOrEqual(1);
    }
    await page.keyboard.press("Escape");
    await expect(dlg).toBeHidden();
  });

  test("one layer shows at a time on the phone, every layer on the desktop", async ({ page }) => {
    await openTokens(page);
    await page.getByRole("button", { name: "Open sheet" }).click();
    const sheet = page.getByRole("dialog", { name: "Task 42" });
    await expect(sheet).toBeVisible();
    await sheet.getByRole("button", { name: "Open dialog" }).click();
    const dlg = page.getByRole("dialog", { name: "Remove channel" });
    await expect(dlg).toBeVisible();
    if (test.info().project.name === "desktop") {
      await expect(sheet).toBeVisible();
    } else {
      await expect(sheet).toBeHidden();
    }
    await page.keyboard.press("Escape");
    await expect(dlg).toBeHidden();
    await expect(sheet).toBeVisible();
    await expect(sheet.getByRole("button", { name: "Open dialog" })).toBeFocused();
  });
});
