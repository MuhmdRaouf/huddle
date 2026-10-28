// pair.spec.ts — pairing a device the way the owner does it: the link `huddle pair` prints
// opens on the device, the name is confirmed, the code on the screen matches the terminal's,
// and the passkey is created against a virtual authenticator. The paired device then shows in
// Settings, and a browser that never paired stays on the landing page. Both stages of the
// screen hold their three widths with every control a target and axe clean.

import { devices, expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import {
  expectTapTargets,
  mintPairLink,
  openTab,
  pairThroughLink,
  theSetup,
  useVirtualAuthenticator,
} from "./helpers.ts";

const DEVICE = "E2E Phone";

test("a pair link pairs the device with a passkey and it appears in Settings", async ({
  page,
}) => {
  await useVirtualAuthenticator(page);
  await pairThroughLink(page, DEVICE);

  // the device is listed for removal; the count is whatever this run's earlier specs paired
  await openTab(page, "Settings");
  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("link", { name: "Security" })
    .click();
  await expect(page.getByRole("heading", { name: /^Devices · \d+$/ })).toBeVisible();
  await expect(page.getByText(DEVICE, { exact: true })).toBeVisible();

  // the session the pairing earned survives a fresh load, no link needed
  await page.reload();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});

test("an unpaired browser stays signed out", async ({ page }) => {
  await theSetup(); // the server is up; this browser simply holds no credential
  await page.goto(`${theSetup().rootUrl}/`);
  await expect(page.locator("[data-landing]")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Main" })).toHaveCount(0);
  // the tour stands beside the sign-in card whether the home holds a passkey or not
  await expect(page.getByRole("heading", { name: "See how it works" })).toBeVisible();
});

// The pair screen's layers: both stages hold their three widths — inside the viewport, no
// sideways scroll, one layer (the page opens none of its own), controls at the pointer's target
// size, axe clean — and the passkey the browser makes lands the shell.
test.describe("the pair screen's layers", () => {
  /** One width of the sweep: the touch widths carry the iPhone's and the iPad's own emulation,
   *  the desk runs the plain pointer whose controls stand 40 px. */
  const WIDTHS = [
    { name: "iphone", touch: true, viewport: { width: 390, height: 844 }, device: "iPhone 15" },
    { name: "tablet", touch: true, viewport: { width: 820, height: 1180 }, device: "iPad Pro 11" },
    { name: "desktop", touch: false, viewport: { width: 1440, height: 900 } },
  ] as const;

  /** How far the page scrolls sideways: above zero is a page the thumb drags left and right. */
  function sideways(page: Page): Promise<number> {
    return page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
  }

  /** Every visible control inside `scope` under the desk's 40 px control height, named for the
   *  failure. The 44 px floor is a touch rule; under a pointer the control stands 40 px. */
  function deskTargets(page: Page, scope = "main"): Promise<string[]> {
    return page.evaluate(
      (sel) =>
        [...document.querySelectorAll<HTMLElement>(`${sel} :is(button, a, input, [role=tab])`)]
          .map((el) => ({ el, r: el.getBoundingClientRect() }))
          .filter(({ el, r }) => r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden")
          .filter(({ r }) => r.width < 40 || r.height < 40)
          .map(({ el, r }) => {
            const name = el.getAttribute("aria-label") || el.textContent?.trim() || el.tagName;
            return `${el.tagName.toLowerCase()} "${name.slice(0, 40)}" ${Math.round(r.width)}x${Math.round(r.height)}`;
          }),
      scope,
    );
  }

  for (const w of WIDTHS) {
    test(`the name and code stages hold the ${w.name} width, targets and axe clean`, async ({
      browser,
    }) => {
      test.setTimeout(60_000);
      const context = w.device
        ? await browser.newContext({ ...devices[w.device], viewport: w.viewport })
        : await browser.newContext({ viewport: w.viewport });
      const page = await context.newPage();
      await useVirtualAuthenticator(page);
      const { link, code } = mintPairLink(`Pair ${w.name}`);

      // the name stage: one column, every control a target, axe clean
      await page.goto(link);
      await expect(page.locator("[data-pair-stage='name']")).toBeVisible();
      expect(await sideways(page), "the name stage scrolls sideways").toBeLessThanOrEqual(0);
      if (w.touch) await expectTapTargets(page);
      else expect(await deskTargets(page), "controls under 40 px").toEqual([]);
      await expectAccessible(page);
      expect(await page.getByRole("dialog").count(), "the page opens no layer of its own").toBe(0);

      // the code stage: the code card ends inside the viewport, its actions stay targets
      await page.getByLabel("Device name").fill(`Pair ${w.name}`);
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.locator("[data-pair-stage='code']")).toBeVisible();
      await expect(page.getByText(code)).toBeVisible();
      expect(await sideways(page), "the code stage scrolls sideways").toBeLessThanOrEqual(0);
      const card = await page.getByText("Pairing code").boundingBox();
      expect(card, "the code card is on the page").toBeTruthy();
      expect(card?.x, "the code card starts inside the viewport").toBeGreaterThanOrEqual(0);
      expect(
        (card?.x ?? 0) + (card?.width ?? 0),
        "the code card ends inside the viewport",
      ).toBeLessThanOrEqual(w.viewport.width + 0.5);
      if (w.touch) await expectTapTargets(page);
      else expect(await deskTargets(page), "controls under 40 px").toEqual([]);
      await expectAccessible(page);

      // and the passkey lands the shell, the way the owner's screen ends
      await page.getByRole("button", { name: "It matches" }).click();
      await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
      await context.close();
    });
  }
});
