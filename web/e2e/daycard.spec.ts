// daycard.spec.ts — the Radar Overview's two today-cards: Radar's own day card, the peak number
// of sessions alive at once today and the cards moved to done, read from /api/radar/api/day over
// the day of the hub's zone, and the hub's Radar card the sessions tab gave up, today's seeded
// cost and requests from the usage report. The fixture answer pins the figures the day card
// draws; the live answer proves it shows what the hub counts; a zone the owner picks names the
// card's day.

import { expect, type Page, test } from "@playwright/test";
import { openTab, SEEDED, signIn, theSetup, TOTALS } from "./helpers.ts";

/** The hub's zone, as GET /api/settings answers it. */
async function hubZone(page: Page): Promise<string> {
  const answer = await page.request.get(`${theSetup().rootUrl}/api/settings`);
  expect(answer.status()).toBe(200);
  return ((await answer.json()) as { zone: string }).zone;
}

/** Sets the hub's zone the way the owner's Settings pick does: PUT /api/settings {zone}. */
async function putZone(page: Page, zone: string): Promise<void> {
  const answer = await page.request.put(`${theSetup().rootUrl}/api/settings`, {
    data: { zone },
    headers: { "x-huddle-action": "/api/settings", "x-huddle-token": theSetup().token },
  });
  expect(answer.status()).toBe(200);
}

const DAY_ROUTE = "**/api/radar/api/day";

/** The day the fixture names: three sessions alive at once, two cards done. */
const FIXTURE = { peakSessions: 3, cardsDone: 2 };

/** Today's three seeded glm-5.3 calls at the price table's 1.40 / 4.40 / 0.26 USD per million. */
const TODAY_USD = (TOTALS.input * 1.4 + TOTALS.output * 4.4 + TOTALS.cacheRead * 0.26) / 1e6;

test("the Radar card is the Overview's first card, and the sessions tab has none", async ({ page }) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/overview`);
  const radar = page.getByRole("region", { name: "Today in Radar" });
  await expect(radar).toBeVisible();
  // it stands above every other card the Overview draws
  const first = await page.evaluate(() => document.querySelector("main .ui-card")?.getAttribute("aria-label"));
  expect(first).toBe("Today in Radar");

  await page.goto(`${theSetup().rootUrl}/#/sessions`);
  await expect(page.getByRole("region", { name: "Today in Radar" })).toHaveCount(0);
});

test("the Radar card shows today's seeded cost and requests", async ({ page }) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/overview`);
  const radar = page.getByRole("region", { name: "Today in Radar" });
  const cost = radar.locator("[data-radar-cost]");
  await expect(cost).toHaveText("$0.0073");
  expect(Number(await cost.getAttribute("data-radar-cost"))).toBeCloseTo(TODAY_USD, 9);
  await expect(radar.locator("[data-radar-requests]")).toHaveText(String(SEEDED.requests.length));
});

test("the day card shows the fixture's peak sessions and cards done", async ({ page }) => {
  await page.route(DAY_ROUTE, (route) => route.fulfill({ json: FIXTURE }));
  await signIn(page);
  const zone = await hubZone(page);
  await openTab(page, "Radar");
  const card = page.locator("[data-day-card]");
  await expect(card).toContainText(`Today, ${zone === "" ? "UTC" : zone}`);
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

test("the day card follows the zone the owner picks", async ({ page }) => {
  await page.route(DAY_ROUTE, (route) => route.fulfill({ json: FIXTURE }));
  await signIn(page);
  const seeded = await hubZone(page);
  // UTC+14: its day is never the UTC one, nor the browser's
  await putZone(page, "Pacific/Kiritimati");
  try {
    await openTab(page, "Radar");
    const card = page.locator("[data-day-card]");
    await expect(card).toContainText("Today, Pacific/Kiritimati");
  } finally {
    await putZone(page, seeded === "" ? "UTC" : seeded);
  }
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
