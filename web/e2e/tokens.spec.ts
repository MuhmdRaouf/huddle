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
  "Status tones",
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
  "Switch",
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

  test("the components draw variant B's shape", async ({ page }) => {
    const fine = test.info().project.name === "desktop";
    await openTokens(page);
    const got = await page.evaluate(() => {
      // The dashboard's CSP allows no style attributes, so a token probe takes its declaration
      // through the CSSOM, which the browser does not strip.
      const fill = (token: string): string => {
        const el = document.createElement("div");
        el.style.backgroundColor = token;
        document.body.append(el);
        const v = getComputedStyle(el).backgroundColor;
        el.remove();
        return v;
      };
      const pick = (sel: string): CSSStyleDeclaration => {
        const el = document.querySelector(sel);
        if (!el) throw new Error(`missing ${sel}`);
        return getComputedStyle(el);
      };
      // the gallery's first secondary button is the pressed scheme button; measure an unpressed one
      const rest = [...document.querySelectorAll<HTMLElement>(".ui-btn-secondary")].find(
        (b) => b.getAttribute("aria-pressed") !== "true",
      );
      if (!rest) throw new Error("missing an unpressed .ui-btn-secondary");
      const px = (v: string): number => Number.parseFloat(v);
      const card = pick(".ui-card");
      const tab = pick('.ui-tabs [role="tab"]');
      const picked = pick('.ui-tabs [role="tab"][aria-selected="true"]');
      return {
        secondaryBorder: px(getComputedStyle(rest).borderTopWidth),
        secondaryFill: getComputedStyle(rest).backgroundColor,
        surface2: fill("var(--ui-surface-2)"),
        btnMin: px(pick(".ui-btn").minHeight),
        dangerBorder: px(pick(".ui-btn-danger").borderTopWidth),
        dangerFill: pick(".ui-btn-danger").backgroundColor,
        failSoft: fill("var(--ui-fail-soft)"),
        cardBorder: px(card.borderTopWidth),
        cardRadius: px(card.borderTopLeftRadius),
        cardShadow: card.boxShadow,
        rowMin: px(pick(".ui-list-row").minHeight),
        trackW: px(pick(".ui-switch-track").width),
        trackH: px(pick(".ui-switch-track").height),
        trackBorder: px(pick(".ui-switch-track").borderTopWidth),
        thumbW: px(pick(".ui-switch-thumb").width),
        thumbFill: pick(".ui-switch-thumb").backgroundColor,
        inputBorder: px(pick(".ui-input").borderTopWidth),
        inputMin: px(pick(".ui-input").minHeight),
        inputFill: pick(".ui-input").backgroundColor,
        tabMin: px(tab.minHeight),
        tabFill: picked.backgroundColor,
        surface: fill("var(--ui-surface)"),
        tabShadow: picked.boxShadow,
        thTransform: pick(".ui-table th").textTransform,
      };
    });
    expect(got.btnMin, `button min-height ${got.btnMin}px`).toBe(fine ? 40 : 48);
    expect(got.secondaryBorder, `secondary border ${got.secondaryBorder}px`).toBe(0);
    expect(got.secondaryFill, `secondary fill ${got.secondaryFill}, surface-2 ${got.surface2}`).toBe(
      got.surface2,
    );
    expect(got.dangerBorder, `danger border ${got.dangerBorder}px`).toBe(0);
    expect(got.dangerFill, `danger fill ${got.dangerFill}, fail-soft ${got.failSoft}`).toBe(got.failSoft);
    expect(got.cardBorder, `card border ${got.cardBorder}px`).toBe(0);
    expect(got.cardRadius, `card radius ${got.cardRadius}px`).toBe(12);
    expect(got.cardShadow, "the card lifts on a shadow, no border").not.toBe("none");
    expect(got.rowMin, `list row min-height ${got.rowMin}px`).toBe(52);
    expect(got.trackW, `switch track width ${got.trackW}px`).toBe(51);
    expect(got.trackH, `switch track height ${got.trackH}px`).toBe(31);
    expect(got.trackBorder, `switch track border ${got.trackBorder}px`).toBe(0);
    expect(got.thumbW, `switch thumb width ${got.thumbW}px`).toBe(27);
    expect(got.thumbFill, `switch thumb fill ${got.thumbFill}`).toBe("rgb(255, 255, 255)");
    expect(got.inputBorder, `input border ${got.inputBorder}px`).toBe(0);
    expect(got.inputMin, `input min-height ${got.inputMin}px`).toBe(fine ? 40 : 48);
    expect(got.inputFill, `input fill ${got.inputFill}, surface-2 ${got.surface2}`).toBe(got.surface2);
    expect(got.tabMin, `tab min-height ${got.tabMin}px`).toBe(fine ? 36 : 44);
    expect(got.tabFill, `selected tab fill ${got.tabFill}, surface ${got.surface}`).toBe(got.surface);
    expect(got.tabShadow, `selected tab shadow ${got.tabShadow}`).not.toContain("inset");
    expect(got.thTransform, `th text-transform ${got.thTransform}`).toBe("none");
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
    // the grab handle sits at the panel's top, 36×5, and the sheet shadow lifts the panel
    const handle = await sheet.locator(".ui-sheet-handle").boundingBox();
    expect(handle && Math.abs(handle.width - 36) <= 0.5 && Math.abs(handle.height - 5) <= 0.5).toBe(true);
    const shadow = await sheet
      .locator(".ui-sheet-panel")
      .evaluate((el) => getComputedStyle(el).boxShadow);
    expect(shadow).not.toBe("none");
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

  test("the banner and empty state sections render and fit the viewport", async ({ page }) => {
    await openTokens(page);
    await expect(page.getByRole("heading", { level: 2, name: "Banners" })).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "Empty states" })).toBeVisible();
    for (const tone of ["info", "warn", "fail"] as const) {
      await expect(page.locator(`.ui-banner-${tone}`)).toHaveCount(1);
    }
    await expect(page.locator(".ui-empty").first()).toBeVisible();
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over).toBeLessThanOrEqual(0);
    // the 44 px floor is a touch rule, as in the screens suite
    if (test.info().project.use.hasTouch) {
      const { expectTapTargets } = await import("./helpers.ts");
      await expectTapTargets(page);
    }
    await expectAccessible(page);
  });

  test("the slide-over is a bottom sheet on the phone and the right-hand pane on the desktop", async ({
    page,
  }) => {
    await openTokens(page);
    await page.getByRole("button", { name: "Open slide-over" }).click();
    const over = page.getByRole("dialog", { name: "Request details" });
    await expect(over).toBeVisible();
    await expectAccessible(page);
    // the element is the whole click-away; the box that moves is the panel inside it
    const box = await over.locator("[data-slide-over]").boundingBox();
    const view = page.viewportSize();
    expect(box && view).toBeTruthy();
    if (!box || !view) return;
    if (test.info().project.name === "desktop") {
      // the pane: the full height at the right edge, the page still showing beside it
      expect(box.y).toBe(0);
      expect(Math.abs(box.y + box.height - view.height)).toBeLessThanOrEqual(1);
      expect(Math.abs(box.x + box.width - view.width)).toBeLessThanOrEqual(1);
      expect(box.x).toBeGreaterThan(0);
    } else {
      // the bottom sheet: full width at the bottom edge, inside the viewport, and nothing of the
      // page or the tab bar left uncovered under it
      expect(box.x).toBe(0);
      expect(box.width).toBe(view.width);
      expect(Math.abs(box.y + box.height - view.height)).toBeLessThanOrEqual(1);
      expect(box.height).toBeLessThanOrEqual(view.height * 0.85 + 1);
      const atTabBar = await page.evaluate(
        ([x, y]) => document.elementFromPoint(x ?? 0, y ?? 0)?.closest("[data-slide-over]") !== null,
        [Math.round(view.width / 2), view.height - 10],
      );
      expect(atTabBar).toBe(true);
    }
    await page.keyboard.press("Escape");
    await expect(over).toBeHidden();
  });

  test("a dialog opened from inside the slide-over covers it, and closing hands the focus back", async ({
    page,
  }) => {
    await openTokens(page);
    await page.getByRole("button", { name: "Open slide-over" }).click();
    const over = page.getByRole("dialog", { name: "Request details" });
    await expect(over).toBeVisible();
    await over.getByRole("button", { name: "Open dialog" }).click();
    const dlg = page.getByRole("dialog", { name: "Detach session" });
    await expect(dlg).toBeVisible();
    if (test.info().project.name === "desktop") {
      await expect(over).toBeVisible();
    } else {
      await expect(over).toBeHidden();
    }
    await page.keyboard.press("Escape");
    await expect(dlg).toBeHidden();
    await expect(over).toBeVisible();
    await expect(over.getByRole("button", { name: "Open dialog" })).toBeFocused();
  });
});
