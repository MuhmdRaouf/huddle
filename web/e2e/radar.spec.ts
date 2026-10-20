// radar.spec.ts — the Radar tab over seeded data: every view loads without a console error,
// the overview's Day cards carry the fixture's exact totals, and the requests table lists the
// three seeded calls with the inspector showing one request's numbers. The views are walked
// through the sub-tab row in one document, the way owners switch views.

import { expect, test } from "@playwright/test";
import { ConsoleErrors, n, openTab, signIn, SEEDED, theSetup, TOTALS } from "./helpers.ts";

/** The views in their sub-tab order, each with the one thing only it shows once loaded. */
const VIEWS: readonly (readonly [
  view: string,
  label: string,
  role: "heading" | "row",
  loaded: RegExp,
])[] = [
  ["overview", "Overview", "heading", /Top sessions by spend/],
  ["costs", "Costs", "heading", /Cost attribution/],
  ["usage", "Usage", "heading", /Daily usage/],
  ["blocks", "Blocks", "heading", /Metering blocks/],
  ["models", "Models", "heading", /^Models$/],
  ["requests", "Requests", "row", /open details/],
  ["tools", "Tools", "heading", /Most used tools/],
  ["agents", "Agents", "row", /Status Agent Model/],
  ["routers", "Routers", "heading", /Router health/],
  ["alerts", "Alerts", "heading", /All clear|^Alerts$/],
  ["timeline", "Timeline", "heading", /^Timeline$/],
];

test("every radar view loads with no console error", async ({ page }) => {
  const errors = new ConsoleErrors(page);
  await signIn(page); // a first signed-in load proves the cookie works before the loop
  await openTab(page, "Radar");
  for (const [, label, role, loaded] of VIEWS) {
    // the sub-tab row's hash link repaints onto the view, no reload
    await page
      .getByRole("navigation", { name: "Radar views" })
      .getByRole("link", { name: label })
      .click();
    await expect(page.getByRole(role, { name: loaded }).first()).toBeVisible();
    await expect(
      page.getByRole("navigation", { name: "Radar views" }).getByRole("link", { name: label }),
    ).toHaveAttribute("aria-current", "page");
  }
  expect(errors.errors()).toEqual([]);
});

test("the overview's Day cards show the seeded totals", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Radar");

  // first load sits on the default hour, and the control says so
  const range = page.getByRole("group", { name: "Range" });
  await expect(range.getByRole("button", { name: "Hour" })).toHaveAttribute("aria-pressed", "true");

  // the owner's Day range: the requests the window holds are the three seeded ones
  await range.getByRole("button", { name: "Day" }).click();
  await expect(range.getByRole("button", { name: "Day" })).toHaveAttribute("aria-pressed", "true");

  // tokens: the fixture's four totals summed, and the cache's share of them
  const total = TOTALS.input + TOTALS.output + TOTALS.cacheRead + TOTALS.cacheWrite;
  const share = Math.round((TOTALS.cacheRead / total) * 100);
  const tokens = page.locator("[data-stat='tokens']");
  await expect(tokens).toBeVisible();
  await expect(tokens).toContainText("Tokens · last 24 hours");
  await expect(tokens).toContainText("10k"); // the 10,000-token total, as the card prints it
  await expect(tokens).toContainText(`${share}% from cache`); // 5,000 of 10,000

  // requests: three in the window, none failed, the window named on the tile
  const requests = page.locator("[data-stat='requests']");
  await expect(requests).toContainText("Requests · last 24 hours");
  await expect(requests).toContainText("3");
  await expect(requests).toContainText("0 failed");
});

test("the blocks view counts the seeded block once the scope includes the provider models", async ({
  page,
}) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/blocks`);

  // the fixture holds no Claude-model request, so the Claude-only default says so plainly
  const card = page.locator("[data-block-card]");
  await expect(card).toBeVisible();
  await expect(card).toContainText("No active block");
  await expect(page.getByText("No blocks in this window")).toBeVisible();

  // widening the scope to every model brings the seeded block in: the three glm calls of this
  // morning, one window, ten thousand tokens, and the flash call of yesterday in its own
  await page.getByRole("group", { name: "Model scope" }).getByRole("button", { name: "All models" }).click();
  await expect(card).toContainText("left");
  await expect(card).toContainText("3");
  await expect(card).toContainText("10k"); // the fixture's three glm calls summed
  await expect(card.locator("[data-block-left]")).toHaveText(/\dh\d{2}m/);
  const rows = page.locator("[data-blocks-table] tbody tr");
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText("3");
  await expect(rows.nth(1)).toContainText("1"); // the flash call twenty-five hours back

  // the overview's card carries the same widened scope
  await page
    .getByRole("navigation", { name: "Radar views" })
    .getByRole("link", { name: "Overview" })
    .click();
  await expect(page.locator("[data-block-card]")).toContainText("10k");
});

test("the requests table lists the seeded rows and the inspector opens one", async ({
  page,
}) => {
  await signIn(page);
  // the Requests view by its own address: the model chip narrows it to the three seeded calls
  await page.goto(`${theSetup().rootUrl}/#/radar/requests`);
  await page
    .getByRole("group", { name: "Filter by model" })
    .getByRole("button", { name: /^GLM 5\.3 \d+$/ })
    .click();

  // each seeded call is one row; its input and output cells are exact
  for (const r of SEEDED.requests) {
    const tr = page
      .getByRole("row")
      .filter({ hasText: n(r.input) })
      .filter({ hasText: n(r.output) });
    await expect(tr).toHaveCount(1);
    await expect(tr).toContainText(n(r.cacheRead));
    await expect(tr).toContainText(n(r.cacheWrite));
  }

  // the inspector: the first call's own numbers, input and output exact
  const first = SEEDED.requests[0];
  const firstTotal = first.input + first.output + first.cacheRead + first.cacheWrite;
  await page
    .getByRole("row")
    .filter({ hasText: n(first.input) })
    .filter({ hasText: n(first.output) })
    .click();
  await expect(page.getByText(`${n(firstTotal)} total`)).toBeVisible();
  await expect(page.getByRole("row", { name: /^Input/ })).toContainText(n(first.input));
  await expect(page.getByRole("row", { name: /^Output/ })).toContainText(n(first.output));
});

test("the requests view opens with its two charts and the statuses the spool carried", async ({
  page,
}) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/requests`);

  // the two chart panels over the range, both drawn
  const charts = page.locator("[data-requests-charts]");
  await expect(charts).toBeVisible();
  await expect(charts.locator("[data-chart='requests'] svg rect").first()).toBeVisible();
  await expect(charts.locator("[data-chart='tokens'] svg rect").first()).toBeVisible();

  // every seeded call came back 200, and the status cell says so; nothing failed
  const table = page.locator("[data-key='requests-table']");
  await expect(table.locator(".badge[title='HTTP 200']").first()).toBeVisible();
  await expect(table.locator(".badge-error")).toHaveCount(0);
});
