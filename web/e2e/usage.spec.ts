// usage.spec.ts — the Usage view over the seeded data: the five seeded requests plus the usage
// load land in the window's tiles as exact token totals, the 7-day window answers with the same
// totals, a click on the seeded day shows that day's split (today carries only the three glm
// calls), a project's drill-down offers the resume command for the seeded session, and the By
// agent table splits the window over Claude and the seeded Codex session.

import { expect, test } from "@playwright/test";
import { CODEX, ConsoleErrors, USAGE, signIn, theSetup } from "./helpers.ts";

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

  // the daily chart drew stacked bars, one per day, never a smoothed line
  const plot = page.locator("[data-usage-plot]");
  await expect(plot).toBeVisible();
  await expect(plot.locator("path.chart-line")).toHaveCount(0);
  await expect(plot.locator("rect").first()).toBeVisible();

  // hovering the chart carries the day and every series' value in a tooltip
  const plotBox = await plot.boundingBox();
  if (plotBox === null) throw new Error("the usage plot never took its size");
  await page.mouse.move(plotBox.x + (plotBox.width * 6.5) / 7, plotBox.y + plotBox.height / 2);
  const tip = page.locator("[data-usage-plot] .chart-tip");
  await expect(tip).toBeVisible();
  await expect(tip.locator(".tip-head")).toContainText(/^\d{1,2} [A-Z][a-z]{2}$/);
  await expect(tip).toContainText("GLM 5.3");
  await page.mouse.move(0, 0);
  await expect(tip).toHaveCount(0);

  // the 7-day window answers with the same requests as the default 30: the load shares the
  // seeded requests' twenty-five-hours-back instant, inside both windows
  await page.getByRole("group", { name: "Window" }).getByRole("button", { name: "7 days" }).click();
  await expect(tiles).toContainText(USAGE.processed.toLocaleString("en-US"));

  // a click on the last day (today) shows its split: the three glm calls, no flash row
  const box = await plot.boundingBox();
  if (box === null) throw new Error("the usage plot never took its size");
  await page.mouse.click(box.x + (box.width * 6.5) / 7, box.y + box.height / 2);
  const card = page.locator("[data-usage-day]");
  await expect(card).toBeVisible();
  // the day is a UTC date, the way the providers' usage pages count it, and the card says so
  await expect(card.getByRole("heading")).toHaveText(/^\d{1,2} [A-Z][a-z]{2} UTC$/);
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
  await expect(drill.locator("rect").first()).toBeVisible();

  // the seeded session's resume id sits one copy click away
  const copy = page.getByRole("button", { name: /Copy the resume command/ }).first();
  await expect(copy).toHaveCount(1);
  await expect(page.locator("[data-key='usage-sessions']")).toContainText("e2e-seed");
});

test("the By agent table splits the window over the agent CLIs, each resuming its own way", async ({
  page,
}) => {
  const errors = new ConsoleErrors(page);
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/usage`);

  // claude's requests (the seeded calls and the usage load) and the seeded codex turn, opencode absent
  const agents = page.locator("[data-key='usage-harness']");
  await expect(agents).toBeVisible();
  await expect(agents).toContainText("Claude");
  await expect(agents).toContainText("Codex");
  expect(await agents.textContent()).not.toContain("Opencode");

  // the codex row opens its own daily series
  await agents.getByRole("row", { name: /Codex/ }).click();
  await expect(page.getByRole("heading", { name: "Agent · Codex" })).toBeVisible();

  // the project's codex session copies its own CLI's resume command
  await page.locator("[data-key='usage-project'] tbody tr").first().click();
  const sessions = page.locator("[data-key='usage-sessions']");
  await expect(sessions).toContainText(CODEX.session.slice(0, 8));
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: new URL(theSetup().rootUrl).origin,
  });
  await sessions
    .getByRole("row", { name: new RegExp(CODEX.session.slice(0, 8)) })
    .getByRole("button", { name: /Copy the resume command/ })
    .click();
  const copied = page.evaluate(() => navigator.clipboard.readText());
  await expect(copied).resolves.toBe(`codex resume ${CODEX.session}`);
  expect(errors.errors()).toEqual([]);
});
