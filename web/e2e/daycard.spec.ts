// daycard.spec.ts — the Radar Overview's day card: the peak number of sessions alive at once
// today and the cards moved to done, read from /api/radar/api/day. The fixture answer pins the
// figures the card draws; the live answer proves the card shows what the hub counts.

import { expect, test } from "@playwright/test";
import { openTab, signIn, theSetup } from "./helpers.ts";

const DAY_ROUTE = "**/api/radar/api/day";

/** The day the fixture names: three sessions alive at once, two cards done. */
const FIXTURE = { peakSessions: 3, cardsDone: 2 };

test("the day card shows the fixture's peak sessions and cards done", async ({ page }) => {
  await page.route(DAY_ROUTE, (route) => route.fulfill({ json: FIXTURE }));
  await signIn(page);
  await openTab(page, "Radar");
  const card = page.locator("[data-day-card]");
  await expect(card).toContainText("Today, UTC");
  await expect(card.locator("[data-day='peak-sessions']")).toHaveText("3");
  await expect(card.locator("[data-day='cards-done']")).toHaveText("2");
});

test("the day card shows what the hub counts", async ({ page }) => {
  await signIn(page);
  const answer = await page.request.get(`${theSetup().rootUrl}/api/radar/api/day`);
  expect(answer.status()).toBe(200);
  const day = (await answer.json()) as typeof FIXTURE;
  // the seeded registry session is alive: the day holds at least one
  expect(day.peakSessions).toBeGreaterThanOrEqual(1);
  await openTab(page, "Radar");
  const card = page.locator("[data-day-card]");
  await expect(card.locator("[data-day='peak-sessions']")).toHaveText(String(day.peakSessions));
  await expect(card.locator("[data-day='cards-done']")).toHaveText(String(day.cardsDone));
});

test("the day card fits a phone", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await page.route(DAY_ROUTE, (route) => route.fulfill({ json: FIXTURE }));
  await signIn(page);
  await openTab(page, "Radar");
  const card = page.locator("[data-day-card]");
  await card.scrollIntoViewIfNeeded();
  await expect(card).toBeVisible();
  const box = await card.boundingBox();
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(393);
});
