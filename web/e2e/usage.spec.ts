// usage.spec.ts — the Usage view over the seeded transcript: the four seeded requests land in the
// window's tiles as exact token totals, the 7-day window answers, a click on the seeded day shows
// that day's split (today carries only the three glm calls), and a project's drill-down offers the
// resume command for the seeded session.

import { expect, test } from "@playwright/test";
import { ConsoleErrors, signIn, theSetup } from "./helpers.ts";

/** The four seeded requests summed: three glm-5.3 today plus one glm-5.3-flash twenty-five hours back. */
const USAGE = {
  processed: 10_015,
  cached: 5_000,
  uncached: 2_410,
  output: 605,
  today: 10_000,
} as const;

test("the usage view shows the seeded totals and splits the picked day", async ({ page }) => {
  const errors = new ConsoleErrors(page);
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/usage`);

  // the five tiles, the token counts exact
  const tiles = page.locator("[data-usage-tiles]");
  await expect(tiles).toBeVisible();
  await expect(tiles).toContainText("Processed tokens");
  await expect(tiles).toContainText(USAGE.processed.toLocaleString("en-US"));
  await expect(tiles).toContainText("Cached input");
  await expect(tiles).toContainText(USAGE.cached.toLocaleString("en-US"));
  await expect(tiles).toContainText("Uncached input");
  await expect(tiles).toContainText(USAGE.uncached.toLocaleString("en-US"));
  await expect(tiles).toContainText("Output");
  await expect(tiles).toContainText(USAGE.output.toLocaleString("en-US"));
  await expect(tiles).toContainText("Cache savings");

  // the daily chart drew one line per model, with the first, middle and last day marked
  const plot = page.locator("[data-usage-plot]");
  await expect(plot).toBeVisible();
  await expect(plot.locator("path.chart-line")).toHaveCount(2);

  // the 7-day window answers with the same requests as the default 30
  await page.getByRole("group", { name: "Window" }).getByRole("button", { name: "7 days" }).click();
  await expect(tiles).toContainText(USAGE.processed.toLocaleString("en-US"));

  // a click on the last day (today) shows its split: the three glm calls, no flash row
  const box = await plot.boundingBox();
  if (box === null) throw new Error("the usage plot never took its size");
  await page.mouse.click(box.x + (box.width * 6.5) / 7, box.y + box.height / 2);
  const card = page.locator("[data-usage-day]");
  await expect(card).toBeVisible();
  await expect(card).toContainText(USAGE.today.toLocaleString("en-US"));
  await expect(card).toContainText("GLM 5.3");
  expect(await card.textContent()).not.toContain("Flash");
  expect(errors.errors()).toEqual([]);
});

test("a project's drill-down opens from its table row and offers the resume command", async ({
  page,
}) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/usage`);
  await expect(page.locator("[data-usage-tiles]")).toBeVisible();

  // the seeded project's row opens its own series and its top sessions
  const row = page.locator("[data-key='usage-project'] tbody tr").first();
  await row.click();
  const drill = page.locator("[data-usage-drill]");
  await expect(drill).toBeVisible();
  await expect(drill.locator("path.chart-line")).toHaveCount(1);

  // the seeded session's resume id sits one copy click away
  const copy = page.getByRole("button", { name: /Copy the resume command/ }).first();
  await expect(copy).toHaveCount(1);
  await expect(page.locator("[data-key='usage-sessions']")).toContainText("e2e-seed");
});
