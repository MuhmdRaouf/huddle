// settings.spec.ts — the Settings tab on the phone and the desktop project: its groups in order
// (Appearance, Notifications, Devices, This hub, Phone, then Sign-in, Radar and Linked channels),
// the appearance switch that overrides the OS scheme and keeps the choice across a reload, the
// push switch that says what the browser allows, the hub's own facts, and the phone guide in a
// sheet. Every group fits the viewport with no horizontal scroll, keeps its tap targets at least
// 44 px and passes axe.

import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { signIn, theSetup } from "./helpers.ts";

/** The section row's links, in order. */
const SECTIONS = [
  "Appearance",
  "Notifications",
  "Devices",
  "This hub",
  "Phone",
  "Sign-in",
  "Radar",
  "Linked channels",
] as const;

/** The groups this card builds, each with its address. */
const GROUPS: readonly (readonly [name: string, hash: string])[] = [
  ["Appearance", "#/settings/appearance"],
  ["Notifications", "#/settings/notifications"],
  ["Devices", "#/settings/devices"],
  ["This hub", "#/settings/hub"],
  ["Phone", "#/settings/phone"],
];

/** Opens one Settings address in a signed-in browser. The authenticator set-up the first sign-in
 *  offers is not this page's subject, so the browser starts with its "Later" already kept. */
async function openSettings(page: Page, hash = "#/settings"): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/${hash}`);
  await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible();
}

/** The section row's link by its name. */
const section = (page: Page, name: string) =>
  page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name, exact: true });

test.describe("the Settings tab", () => {
  test("lists its groups in order, Appearance first", async ({ page }) => {
    await openSettings(page);
    const names = await page
      .getByRole("navigation", { name: "Settings sections" })
      .getByRole("link")
      .allTextContents();
    expect(names.map((n) => n.trim())).toEqual([...SECTIONS]);
    await expect(section(page, "Appearance")).toHaveAttribute("aria-current", "page");
  });

  test("the appearance switch overrides the OS and keeps the choice across a reload", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await openSettings(page, "#/settings/appearance");
    const html = page.locator("html");
    await expect(html).toHaveAttribute("data-scheme", "light");
    await page.getByRole("button", { name: "Dark", exact: true }).click();
    await expect(html).toHaveAttribute("data-scheme", "dark");
    await page.reload();
    await expect(page.getByRole("button", { name: "Dark", exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(html).toHaveAttribute("data-scheme", "dark");
    await page.getByRole("button", { name: "System", exact: true }).click();
    await expect(html).toHaveAttribute("data-scheme", "light");
    await page.emulateMedia({ colorScheme: "dark" });
    await expect(html).toHaveAttribute("data-scheme", "dark");
  });

  test.describe("with the app's worker running", () => {
    // the push switch asks the worker's registration, so this case lets it run
    test.use({ serviceWorkers: "allow" });

    test("the notification switches say what this browser allows", async ({ page }) => {
      await openSettings(page, "#/settings/notifications");
      await expect(page.locator("#set-notif")).toBeVisible();
      const push = page.locator("#set-push");
      await expect(push).toHaveAttribute("aria-checked", "false");
      const permission = await page.evaluate(() => Notification.permission);
      await expect(page.locator("#push-h")).toContainText(`Push permission: ${permission}.`);
      // iOS pushes only to the app added to the Home Screen, and this page runs in a tab
      await expect(page.locator("#push-h")).toContainText("add Huddle to the Home Screen");
      // the hub's own choice of what wakes a device stands beside the switches
      await expect(page.getByRole("radiogroup", { name: "Notifications" })).toBeVisible();
    });
  });

  test("This hub names the hub, its address and its version", async ({ page }) => {
    await openSettings(page, "#/settings/hub");
    const facts = page.locator("[data-hub-facts]");
    await expect(facts).toContainText(theSetup().rootUrl.replace(/\/$/, ""));
    await expect(facts).toContainText(/\d+\.\d+\.\d+/);
  });

  test("the phone guide opens in a sheet", async ({ page }) => {
    await openSettings(page, "#/settings/phone");
    await page.getByRole("button", { name: "Add a phone" }).click();
    const sheet = page.getByRole("dialog", { name: "Add a phone" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByRole("img", { name: "QR code: the phone's trust guide" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
  });

  for (const [name, hash] of GROUPS) {
    test(`${name}: no horizontal scroll, 44 px targets, axe clean`, async ({ page }) => {
      await openSettings(page, hash);
      await expect(section(page, name)).toHaveAttribute("aria-current", "page");
      await page
        .waitForFunction(() => !document.body.textContent?.includes("Loading…"), undefined, { timeout: 3000 })
        .catch(() => {});
      const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      expect(over).toBeLessThanOrEqual(0);
      const small = await page.evaluate(() =>
        [...document.querySelectorAll<HTMLElement>("[data-settings] button, [data-settings] input, [data-settings] a")]
          // a switch or a radio is tapped through the label around it
          .map((el) => ({ el, r: (el.closest("label") ?? el).getBoundingClientRect() }))
          .filter(({ r }) => r.width > 0 && (r.width < 44 || r.height < 44))
          .map(({ el, r }) => {
            const label = el.textContent?.trim() || el.getAttribute("aria-label") || el.id;
            return `${el.tagName} "${label}" ${r.width}x${r.height}`;
          }),
      );
      expect(small).toEqual([]);
      await expectAccessible(page);
    });
  }
});
