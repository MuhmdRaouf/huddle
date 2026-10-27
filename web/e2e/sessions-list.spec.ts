// sessions-list.spec.ts — the desk's Sessions header: the title and the Select toggle stand on
// one line, and no checkbox shows on a row until Select is pressed; at phone width the list
// scrolls nothing sideways. Axe finds nothing serious on either width.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { openTab, signIn } from "./helpers.ts";

// the authenticator set-up the first sign-in offers is not this page's subject: the browser
// starts with its "Later" already kept
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

test("the desk header holds Sessions and Select on one line, and no checkbox shows until Select", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the desk header is the desktop project's");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await openTab(page, "Sessions");

  const header = page.locator("[data-sessions-pane] header");
  await expect(header.getByRole("heading", { name: "Sessions" })).toBeVisible();
  const toggle = header.getByRole("button", { name: "Select" });
  await expect(toggle).toBeVisible();
  // one line: the title's band and the button's band overlap
  const title = await header.getByRole("heading").boundingBox();
  const btn = await toggle.boundingBox();
  expect(title).not.toBeNull();
  expect(btn).not.toBeNull();
  const gap = Math.abs((title!.y + title!.height / 2) - (btn!.y + btn!.height / 2));
  expect(gap).toBeLessThanOrEqual(Math.max(title!.height, btn!.height) / 2);

  // no checkbox until Select is pressed; then one per row and a pick works
  expect(await page.locator("[data-sessions-pane] input[type='checkbox']").count()).toBe(0);
  await toggle.click();
  await expect(page.getByRole("button", { name: "Done" })).toBeVisible();
  const boxes = page.locator("[data-sessions-pane] [data-select-row] input[type='checkbox']");
  const rows = page.locator("[data-sessions-pane] [data-select-row]");
  expect(await boxes.count()).toBe(await rows.count());
  expect(await boxes.count()).toBeGreaterThan(0);
  await expectAccessible(page);
});

test("at phone width the Sessions list scrolls nothing sideways", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await signIn(page);
  await openTab(page, "Sessions");
  const over = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(over).toBeLessThanOrEqual(0);
  await expectAccessible(page);
});
