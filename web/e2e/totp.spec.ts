// totp.spec.ts — the authenticator's whole path over the real binary: a link sign-in on an
// un-enrolled hub offers the set-up before the shell (skippable with Later), Settings enrols it
// (the QR and the secret the page shows, the code computed here with RFC 6238 over node:crypto,
// the recovery codes the answer carries), the landing page signs a cookie-less browser in with
// a code, a recovery code works the same way, and a phone browser with a platform authenticator
// is offered its lock as a passkey whose Turn on makes the next sign-in one touch. The
// authenticator's layers — the first-run gate, the Settings sheet, the passkey offer, the
// removal toast — hold their three widths: inside the viewport, clear of the tab bar, one at a
// time, every control at the pointer's target size, axe clean.

import { devices, expect, test, type Browser, type BrowserContext, type Locator, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import {
  expectSheetGutter,
  expectTapTargets,
  loginCode,
  signIn,
  theSetup,
  totpCode,
  totpSecret,
  typeCode,
  useVirtualAuthenticator,
} from "./helpers.ts";

/** What this run enrolled: the secret and the recovery codes, read off the set-up page the only
 *  time they show. One worker runs this file's tests in order, so the later tests read what the
 *  earlier ones captured. */
let secret = "";
let recovery: string[] = [];

/** The newest TOTP step this file spent on the live enrolment: a code is one use, so the next
 *  sign-in types a code for a step beside every spent one. */
let spentThroughStep = -1;

/** The code for the next fresh step: the first step after every spent one, inside the hub's
 *  one-step skew of the current step, waiting out a step boundary when it must. */
async function freshCode(page: Page, enrolment: string): Promise<string> {
  for (;;) {
    const step = Math.floor(Date.now() / 1000 / 30);
    const want = Math.max(step, spentThroughStep + 1);
    if (want <= step + 1) {
      spentThroughStep = want;
      return totpCode(enrolment, want - step);
    }
    await page.waitForTimeout(1_000);
  }
}

/** One width of the layer sweep: the touch widths carry the iPhone's and the iPad's own
 *  emulation, the desk runs the plain pointer whose controls stand 40 px. */
type Width = {
  name: string;
  touch: boolean;
  viewport: { width: number; height: number };
  device?: "iPhone 15" | "iPad Pro 11";
};

const WIDTHS: readonly Width[] = [
  { name: "iphone", touch: true, viewport: { width: 390, height: 844 }, device: "iPhone 15" },
  { name: "tablet", touch: true, viewport: { width: 820, height: 1180 }, device: "iPad Pro 11" },
  { name: "desktop", touch: false, viewport: { width: 1440, height: 900 } },
];

/** One browser at one sweep width. */
function widthContext(browser: Browser, w: Width): Promise<BrowserContext> {
  return w.device !== undefined
    ? browser.newContext({ ...devices[w.device], viewport: w.viewport })
    : browser.newContext({ viewport: w.viewport });
}

/** How far the page scrolls sideways: above zero is a page the thumb drags left and right. */
function sideways(page: Page): Promise<number> {
  return page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
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

/** One open layer's box: wholly inside the viewport, then in the form its width owns — the
 *  bottom sheet spans the phone and anchors to its bottom edge, the desk centres the card. */
async function expectLayerInside(page: Page, layer: Locator, w: Width): Promise<void> {
  const box = await layer.boundingBox();
  const view = page.viewportSize();
  expect(box, "the layer is on the page").toBeTruthy();
  expect(view, "the viewport has a size").toBeTruthy();
  if (box === null || view === null) return;
  expect(box.x, "the layer starts inside the viewport").toBeGreaterThanOrEqual(0);
  expect(box.y, "the layer starts inside the viewport").toBeGreaterThanOrEqual(0);
  expect(box.x + box.width, "the layer ends inside the viewport").toBeLessThanOrEqual(view.width + 0.5);
  expect(box.y + box.height, "the layer ends inside the viewport").toBeLessThanOrEqual(view.height + 0.5);
  if (view.width < 768) {
    expect(box.x, "the phone's layer spans the viewport").toBe(0);
    expect(box.width, "the phone's layer spans the viewport").toBe(view.width);
    expect(Math.abs(box.y + box.height - view.height), "the layer anchors to the bottom edge").toBeLessThanOrEqual(1);
  } else {
    expect(Math.abs(box.x * 2 + box.width - view.width), "the layer centres in the viewport").toBeLessThanOrEqual(1);
  }
}

/** The layer checks one open page owes: no sideways scroll, one layer at a time, controls at the
 *  pointer's target size, axe clean. */
async function expectLayerDiscipline(page: Page, w: Width): Promise<void> {
  expect(await page.getByRole("dialog").count(), "one layer stands at a time").toBeLessThanOrEqual(1);
  if (w.touch) await expectTapTargets(page);
  else expect(await deskTargets(page), "controls under 40 px").toEqual([]);
  await expectAccessible(page);
}

/** Enrols the authenticator from Settings › Security, capturing the secret and the codes. The
 *  set-up stands in the row's sheet: the row opens it, the steps run inside it. */
async function enrolFromSettings(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Authenticator app" }).click();
  const sheet = page.getByRole("dialog", { name: "Authenticator app" });
  await expect(sheet).toBeVisible();
  await sheet.getByRole("button", { name: "Set up" }).click();
  await expect(page.locator("[data-qr]")).toBeVisible();
  secret = await totpSecret(page);
  await typeCode(page, totpCode(secret));
  spentThroughStep = Math.floor(Date.now() / 1000 / 30);
  await expect(page.getByText("Save your recovery codes")).toBeVisible();
  recovery = (await page.locator("[data-recovery] li").allTextContents()).map((c) => c.trim());
  expect(recovery).toHaveLength(10);
  await page.getByRole("button", { name: "I saved them" }).click();
  await expect(page.getByText("A code from your app signs any browser in.")).toBeVisible();
}

test("the first-run gate holds its three widths: inside the viewport, targets at the pointer's size, axe clean", async ({
  browser,
}) => {
  const { pairOrigin } = theSetup();
  for (const w of WIDTHS) {
    const context = await widthContext(browser, w);
    const page = await context.newPage();
    const code = await loginCode();
    await page.goto(`${pairOrigin}/?code=${code}`);
    await expect(page.locator("[data-totp-first-run]")).toBeVisible();
    await expect(page.locator("[data-qr]")).toBeVisible();

    expect(await sideways(page), "the gate scrolls sideways").toBeLessThanOrEqual(0);
    for (const sel of ["[data-qr]", "[data-secret]"]) {
      const box = await page.locator(sel).boundingBox();
      expect(box, `${sel} is on the page`).toBeTruthy();
      expect(box?.x, `${sel} starts inside the viewport`).toBeGreaterThanOrEqual(0);
      expect(
        (box?.x ?? 0) + (box?.width ?? 0),
        `${sel} ends inside the viewport`,
      ).toBeLessThanOrEqual(w.viewport.width + 0.5);
    }
    await expectLayerDiscipline(page, w);

    await page.getByRole("button", { name: /Later/ }).click();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    await context.close();
  }
});

test("a link sign-in offers the set-up, Settings enrols it, and a code signs the landing page in", async ({
  page,
}) => {
  const { pairOrigin } = theSetup();

  // the gate: no enrolment yet, so the link lands on the set-up before the shell; Later skips
  const code = await loginCode();
  await page.goto(`${pairOrigin}/?code=${code}`);
  await expect(page.getByText("Set up your authenticator app")).toBeVisible();
  await page.getByRole("button", { name: /Later/ }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // Settings › Security's Sign-in pane enrols, and the recovery codes show this one time
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Settings" }).click();
  await page.getByRole("link", { name: "Security" }).click();
  await enrolFromSettings(page);

  // the landing page: cookies gone, the code field is the way in, the shell lands
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByText("Enter the code from your authenticator app")).toBeVisible();
  await typeCode(page, totpCode(secret));
  spentThroughStep = Math.floor(Date.now() / 1000 / 30);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  // a code sign-in may earn the biometrics offer on this desktop too: close it if it stood
  const notNow = page.getByRole("button", { name: "Not now" });
  if (await notNow.isVisible().catch(() => false)) await notNow.click();

  // a recovery code works the same way, once
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await page.getByRole("button", { name: "Use a recovery code instead" }).click();
  await page.getByLabel("Recovery code").fill(recovery[0] ?? "");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
});

test("the passkey offer stands inside its three widths, one dialog, axe clean", async ({ browser }) => {
  test.setTimeout(90_000);
  const { pairOrigin } = theSetup();
  // the offer stands only when this browser's device row holds no passkey, and the desk
  // project runs this file first on the shared hub: its phone test turns one on for the row
  // the iPhone's agent names. The row starts removed; the code sign-in below re-creates it
  // without one
  const row = await fetch(`${theSetup().rootUrl}/api/devices/iPhone`, {
    method: "DELETE",
    headers: { "x-huddle-action": "/api/devices/iPhone", "x-huddle-token": theSetup().token },
  });
  if (!row.ok && row.status !== 404) throw new Error(`DELETE /api/devices/iPhone answered ${row.status}`);
  for (const w of WIDTHS) {
    const context = await widthContext(browser, w);
    const page = await context.newPage();
    await useVirtualAuthenticator(page);
    await signIn(page, pairOrigin);

    // a code sign-in on a browser whose device row holds no passkey yet earns the offer, over
    // the shell; answering Not now leaves every row as it stands, so the phone test below still
    // earns its own offer and turns it on
    await page.context().clearCookies();
    await page.goto(`${pairOrigin}/`);
    await expect(page.getByText("Enter the code from your authenticator app")).toBeVisible();
    await typeCode(page, await freshCode(page, secret));
    const offer = page.locator("[data-passkey-offer]");
    await expect(offer).toBeVisible();
    await expectSheetGutter(offer);
    await expect(page.getByRole("dialog")).toHaveCount(1);
    await expectLayerInside(page, page.getByRole("dialog"), w);
    await expectLayerDiscipline(page, w);

    await page.getByRole("button", { name: "Not now" }).click();
    await expect(offer).toHaveCount(0);
    await context.close();
  }
});

test("a phone browser is offered its lock as a passkey after a code sign-in", async ({ browser }) => {
  const { pairOrigin } = theSetup();

  // the phone: its own context with the iPhone's user agent and screen, and its own
  // authenticator — a second browser beside the desktop one, not a reconfigured first
  const phone = await browser.newContext({ ...devices["iPhone 15"] });
  const page = await phone.newPage();
  await useVirtualAuthenticator(page);

  // replace the enrolment so this test reads its own secret: remove (the link session's own
  // machine waives the step-up on loopback), then set up again. The remove stands in the row's
  // sheet too, and the sheet closes before the enrolment below re-opens the row
  await signIn(page, pairOrigin);
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Settings" }).click();
  await page.getByRole("link", { name: "Security" }).click();
  await page.getByRole("button", { name: "Authenticator app" }).click();
  const sheet = page.getByRole("dialog", { name: "Authenticator app" });
  await expect(sheet).toBeVisible();
  await sheet.getByRole("button", { name: "Remove" }).click();
  await expect(sheet.getByText(/Not set up/)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();
  await enrolFromSettings(page);

  // the landing page on the phone: the code field, then the offer standing over the shell —
  // this browser holds no passkey and the platform has one to give. The offer is a modal
  // dialog, so the shell behind it is inert to every role query until it is answered
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByText("Enter the code from your authenticator app")).toBeVisible();
  await expect(page.locator("[data-landing]")).toBeVisible();
  await typeCode(page, totpCode(secret));
  await expect(page.getByText("Use Face ID next time?")).toBeVisible();
  await page.getByRole("button", { name: "Turn on" }).click();
  await expect(page.getByText("Use Face ID next time?")).toHaveCount(0);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // the landing leads with the lock the platform names, and the code under it stays the fallback
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByRole("button", { name: "Sign in with Face ID" })).toBeVisible();
  await expect(page.getByText("Or enter your authenticator code")).toBeVisible();

  // the fallback runs in a browser that holds no passkey of its own here (the field's autofill
  // offer is what a saved passkey earns): the code field is the way in, and a code signs in — the
  // step past the one the first code spent, a code being one use. The hub's one-step skew accepts
  // that code the moment the first is spent, so no step boundary is waited out
  await page.evaluate(() => localStorage.removeItem("huddle:passkey-here"));
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await expect(page.getByText("Enter the code from your authenticator app")).toBeVisible();
  await typeCode(page, totpCode(secret, 1));
  spentThroughStep = Math.floor(Date.now() / 1000 / 30) + 1;
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();

  // and one touch is the next sign-in, the mark its own sign-in leaves in this browser
  await page.evaluate(() => localStorage.setItem("huddle:passkey-here", "1"));
  await page.context().clearCookies();
  await page.goto(`${pairOrigin}/`);
  await page.getByRole("button", { name: "Sign in with Face ID" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  await phone.close();
});

test("the Settings sheet holds its three widths with the enrolment's row, one dialog, axe clean", async ({
  browser,
}) => {
  test.setTimeout(90_000);
  const { pairOrigin } = theSetup();
  for (const w of WIDTHS) {
    const context = await widthContext(browser, w);
    const page = await context.newPage();
    await signIn(page, pairOrigin);
    await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Settings" }).click();
    await page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name: "Security" }).click();
    await page.getByRole("button", { name: "Authenticator app" }).click();
    const sheet = page.getByRole("dialog", { name: "Authenticator app" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByRole("button", { name: "Remove" })).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(1);
    await expectLayerInside(page, sheet, w);
    await expectLayerDiscipline(page, w);
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
    await context.close();
  }
});

test("the removal toast and the set-up hold their three widths in the sheet", async ({ browser }) => {
  test.setTimeout(120_000);
  const { pairOrigin } = theSetup();
  // the enrolment the earlier tests left stands only once: the first width removes it, the
  // widths after it enrol their own from the sheet before removing it again
  let enrolled = true;
  for (const w of WIDTHS) {
    const context = await widthContext(browser, w);
    const page = await context.newPage();
    await signIn(page, pairOrigin);
    await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Settings" }).click();
    await page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name: "Security" }).click();
    if (!enrolled) {
      // enrolling leaves the row's sheet open, the enrolled row and its Remove inside it
      await enrolFromSettings(page);
    } else {
      await page.getByRole("button", { name: "Authenticator app" }).click();
    }
    const sheet = page.getByRole("dialog", { name: "Authenticator app" });
    await expect(sheet).toBeVisible();

    // the removal says itself as a toast: above the phone's tab bar, at the desk's foot. The
    // tab bar stands under 768 px only; the wider widths read the toast at the foot alone
    await sheet.getByRole("button", { name: "Remove" }).click();
    enrolled = false;
    const toast = page.getByText("Authenticator sign-in removed; its sessions ended");
    await expect(toast).toBeVisible();
    const toastBox = await toast.boundingBox();
    const view = page.viewportSize();
    expect(toastBox, "the toast is on the page").toBeTruthy();
    if (toastBox !== null && view !== null) {
      expect(toastBox.y + toastBox.height, "the toast ends inside the viewport").toBeLessThanOrEqual(view.height);
      const bar = page.locator(".ui-tabbar");
      if ((await bar.count()) > 0) {
        const barBox = await bar.boundingBox();
        expect(toastBox.y + toastBox.height, "the toast clears the tab bar").toBeLessThanOrEqual(
          (barBox?.y ?? 0) + 1,
        );
      }
    }
    await expectAccessible(page);

    // the sheet reads not set up now, and its set-up runs to the QR inside the same sheet
    await expect(sheet.getByText(/Not set up/)).toBeVisible();
    await sheet.getByRole("button", { name: "Set up" }).click();
    await expect(page.locator("[data-qr]")).toBeVisible();
    await expectLayerDiscipline(page, w);
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
    await context.close();
  }
});
