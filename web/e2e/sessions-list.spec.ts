// sessions-list.spec.ts — the desk's Sessions header: the title and the Select toggle stand on
// one line, and no checkbox shows on a row until Select is pressed; the search box and the
// machine chips sit above the list, the chips on one line, and a typed word narrows the rows;
// at phone width the list scrolls nothing sideways. Axe finds nothing serious on either width.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, signIn } from "./helpers.ts";

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

test("the machine chips hold one line at 1440, and the search narrows the rows", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the desk's one-line chips are the desktop project's");
  await page.setViewportSize({ width: 1440, height: 900 });
  await signIn(page);
  await openTab(page, "Sessions");
  // the run starts on an empty registry: report one session so the list and the chips have a
  // machine to name
  await api("/api/registry/report", {
    session_id: "e2e-chips",
    machine: "e2e-chips-machine",
    project: "e2e",
    title: "Chips hold one line",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  await expect(page.locator("[data-row='e2e-chips']")).toBeVisible();

  // one line: the chips row is no taller than one of its chips
  const chipsRow = page.locator("[data-machine-chips]");
  await expect(chipsRow).toBeVisible();
  await expect(chipsRow.getByRole("button", { name: "All" })).toBeVisible();
  await expect(chipsRow.getByRole("button", { name: "e2e-chips-machine" })).toBeVisible();
  const rowBox = await chipsRow.boundingBox();
  const chipBox = await chipsRow.locator("button").first().boundingBox();
  expect(rowBox).not.toBeNull();
  expect(chipBox).not.toBeNull();
  expect(rowBox!.height - chipBox!.height).toBeLessThanOrEqual(1);

  // a typed word narrows the list to nothing, and clearing it gives the rows back
  const pane = page.locator("[data-sessions-pane]");
  const before = await pane.locator("[data-row]").count();
  expect(before).toBeGreaterThan(0);
  const search = page.getByRole("searchbox", { name: "Search sessions" });
  await search.fill("no session matches this");
  expect(await pane.locator("[data-row]").count()).toBe(0);
  await search.fill("");
  expect(await pane.locator("[data-row]").count()).toBe(before);
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
