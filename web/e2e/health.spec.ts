// health.spec.ts — the hub's health in the shell and on Settings: with /health answering one
// failing part (the real body with the health fields added, every other field left real), a red
// dot shows beside the brand on the desktop and on the Settings tab at the phone's size, the
// phone's hub card reads Failing with the count, and Settings › Hub lists the failing part by
// the name its reader uses with its last error. With the real, unchanged /health no dot shows.

import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { signIn, theSetup } from "./helpers.ts";

/** Answers /health with the real body plus one failing store, so every other field stays real. */
async function failHistory(page: Page): Promise<void> {
  await page.route("**/health", async (route) => {
    const real = await route.fetch();
    const body = (await real.json()) as Record<string, unknown>;
    await route.fulfill({
      response: real,
      json: {
        ...body,
        healthy: false,
        components: [
          {
            name: "history.db",
            kind: "store",
            state: "failing",
            lastOkAt: 0,
            lastErrAt: Date.now() - 12 * 3_600_000,
            lastError: "database disk image is malformed (11)",
            errors: 3,
          },
        ],
      },
    });
  });
}

/** Signs in and opens a hash; the route must stand before the first load. */
async function open(page: Page, hash = "#/sessions"): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/${hash}`);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

test.describe("with a part failing, on the desktop", () => {
  test("the dot stands beside the brand and Settings › Hub lists the part", async ({ page }) => {
    await failHistory(page);
    await open(page);
    const sidebar = page.locator("[data-sidebar]");
    const brand = sidebar.locator("a[href='#/channels']");
    await expect(brand.locator("[data-health-dot]")).toBeVisible();
    // the word rides out of sight, for a reader alone
    await expect(brand.getByText("A part of the hub is failing")).toHaveClass(/sr-only/);
    await page.goto(`${theSetup().rootUrl}/#/settings/hub`);
    const health = page.locator("[data-health]");
    await expect(health).toBeVisible();
    await expect(health).toContainText("Radar history");
    await expect(health).toContainText("Failing");
    await expect(health).toContainText("database disk image is malformed (11)");
    await expectAccessible(page);
  });
});

test.describe("with a part failing, at the phone's size", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the dot rides the Settings tab, the hub card reads Failing, and Hub lists the part", async ({
    page,
  }) => {
    await failHistory(page);
    await open(page);
    const tab = page.locator("[data-nav='bottom'] a[data-tab='settings']");
    await expect(tab.locator("[data-health-dot]")).toBeVisible();
    // Settings: the hub card's pill and its line under the name
    await page.goto(`${theSetup().rootUrl}/#/settings`);
    const card = page.locator("a[data-hub-card]");
    await expect(card.locator("[data-hub-healthy]")).toHaveText("Failing");
    await expect(card.locator("[data-hub-subtitle]")).toHaveText("1 part failing");
    // Settings › Hub: the Health section lists the failing store
    await page.goto(`${theSetup().rootUrl}/#/settings/hub`);
    const health = page.locator("[data-health]");
    await expect(health).toContainText("Radar history");
    await expect(health).toContainText("database disk image is malformed (11)");
    await expectAccessible(page);
  });
});

test.describe("with the real health answer", () => {
  test("no dot shows while the hub is well", async ({ page }) => {
    await open(page);
    await expect(page.locator("[data-health-dot]")).toHaveCount(0);
    await page.goto(`${theSetup().rootUrl}/#/settings/hub`);
    await expect(page.locator("[data-health-dot]")).toHaveCount(0);
    // the run's hub reports no parts yet, so there is no Health card to draw
    await expect(page.locator("[data-health]")).toHaveCount(0);
    await expectAccessible(page);
  });
});
