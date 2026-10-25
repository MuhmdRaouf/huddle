// settings.spec.ts — the Settings tab on the phone and the desktop project: its four groups in order
// (Appearance, Notifications, Devices and sign-in, Hub and Radar), each group page with its members,
// a member's own address opening the group that holds it, the appearance switch that overrides the
// OS scheme and keeps the choice across a reload, the push switch that says what the browser
// allows, the finished-turn push a paired device turns on and keeps across a reload, the hub's own facts, the hub's time zone (seeded by the first signed-in browser, picked
// again and kept across a reload), and the phone guide in a sheet. Every group fits the viewport
// with no horizontal scroll, keeps its tap targets at least 44 px and passes axe.

import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { pairThroughLink, signIn, theSetup, useVirtualAuthenticator } from "./helpers.ts";

/** The four groups, in row order, each with its address and the members its page draws. */
const GROUPS: readonly (readonly [name: string, hash: string, members: readonly string[]])[] = [
  ["Appearance", "#/settings/appearance", ["appearance"]],
  ["Notifications", "#/settings/notifications", ["notifications"]],
  ["Devices and sign-in", "#/settings/devices", ["devices", "phone", "signin"]],
  ["Hub and Radar", "#/settings/hub", ["hub", "zone", "radar"]],
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
    expect(names.map((n) => n.trim())).toEqual(GROUPS.map(([name]) => name));
    await expect(section(page, "Appearance")).toHaveAttribute("aria-current", "page");
  });

  test("each group page draws its members, and a member's address opens its group", async ({ page }) => {
    await openSettings(page);
    for (const [name, hash, members] of GROUPS) {
      await page.goto(`${theSetup().rootUrl}/${hash}`);
      await expect(section(page, name)).toHaveAttribute("aria-current", "page");
      await expect(page.locator("[data-setting]")).toHaveCount(members.length);
      const drawn = await page
        .locator("[data-setting]")
        .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.setting));
      expect(drawn, name).toEqual([...members]);
    }
    for (const [member, name] of [
      ["radar", "Hub and Radar"],
      ["signin", "Devices and sign-in"],
      ["phone", "Devices and sign-in"],
    ] as const) {
      await page.goto(`${theSetup().rootUrl}/#/settings/${member}`);
      await expect(section(page, name), member).toHaveAttribute("aria-current", "page");
      await expect(page.locator(`[data-setting="${member}"]`)).toBeVisible();
    }
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

  test("a paired device's finished-turn push is off by default and its choice survives a reload", async ({
    page,
  }) => {
    // the choice is a paired device's own: this browser pairs under its own name first
    await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
    await useVirtualAuthenticator(page);
    await pairThroughLink(page, `E2E Turn ${test.info().project.name}`);
    await page.goto(`${theSetup().pairOrigin}/#/settings/notifications`);
    const turn = page.getByRole("switch", { name: "Push when a turn finishes" });
    await expect(turn).toBeEnabled();
    await expect(turn).toHaveAttribute("aria-checked", "false");
    await turn.click();
    await expect(turn).toHaveAttribute("aria-checked", "true");
    await page.reload();
    await expect(turn).toBeEnabled();
    await expect(turn).toHaveAttribute("aria-checked", "true");
    // the asks keep their own choice beside it
    await expect(page.getByRole("radio", { name: /Needs you only/ })).toBeChecked();
    await turn.click();
    await expect(turn).toHaveAttribute("aria-checked", "false");
    await page.reload();
    await expect(turn).toHaveAttribute("aria-checked", "false");
  });

  test("This hub names the hub, its address and its version", async ({ page }) => {
    await openSettings(page, "#/settings/hub");
    const facts = page.locator("[data-hub-facts]");
    await expect(facts).toContainText(theSetup().rootUrl.replace(/\/$/, ""));
    await expect(facts).toContainText(/\d+\.\d+\.\d+/);
  });

  test("the hub's time zone is seeded by a signed-in browser and a pick survives a reload", async ({ page }) => {
    await openSettings(page, "#/settings/hub");
    const heading = page.locator("#zone-h");
    const picker = page.getByRole("combobox", { name: "Time zone" });
    // this browser's first signed-in load sent its zone, so the hub holds one
    await expect(heading).toHaveText(/^Time zone: [A-Za-z_]+(\/[A-Za-z_+-]+)*( \(from your first sign-in\))?$/);
    await expect(picker).toBeEnabled();
    const next = (await picker.inputValue()) === "Asia/Tokyo" ? "Europe/Istanbul" : "Asia/Tokyo";
    await picker.selectOption(next);
    await expect(heading).toHaveText(`Time zone: ${next}`);
    await page.reload();
    await expect(heading).toHaveText(`Time zone: ${next}`);
    await expect(page.getByRole("combobox", { name: "Time zone" })).toHaveValue(next);
    await page.getByRole("combobox", { name: "Time zone" }).selectOption("UTC");
    await expect(heading).toHaveText("Time zone: UTC");
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
          // Radar's own settings view keeps its 30 px controls until HV-TOKENS-SETTINGS moves it
          // (screens.spec.ts lists it under KNOWN)
          .filter((el) => el.closest('[data-setting="radar"]') === null)
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
