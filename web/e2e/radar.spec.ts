// radar.spec.ts — the Radar tab over seeded data: every view loads without a console error,
// the overview's Day cards carry the fixture's exact totals, and the requests table lists the
// three seeded calls with the inspector showing one request's numbers. Each view is reached by
// its own page load, the way a link or a bookmark opens it.

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
  for (const [view, label, role, loaded] of VIEWS) {
    // one document per view: the query differs per view, so each is a real page load that
    // boots straight onto its view
    await page.goto(`${theSetup().rootUrl}/?view=${view}#/radar/${view}`);
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

  // the owner's Day range: the requests the window holds are the three seeded ones
  await page.getByRole("group", { name: "Range" }).getByRole("button", { name: "Day" }).click();

  // tokens: the fixture's four totals summed, and the cache's share of them
  const total = TOTALS.input + TOTALS.output + TOTALS.cacheRead + TOTALS.cacheWrite;
  const share = Math.round((TOTALS.cacheRead / total) * 100);
  const tokens = page.locator("[data-stat='tokens']");
  await expect(tokens).toBeVisible();
  await expect(tokens).toContainText("10k"); // the 10,000-token total, as the card prints it
  await expect(tokens).toContainText(`${share}% from cache`); // 5,000 of 10,000

  // requests: three in the window, none failed
  const requests = page.locator("[data-stat='requests']");
  await expect(requests).toContainText("3");
  await expect(requests).toContainText("0 failed");
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
