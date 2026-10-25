// radar.spec.ts — the Radar tab over seeded data: every view loads without a console error,
// the overview's Day cards carry the fixture's exact totals, and the requests table lists the
// three seeded calls with the inspector showing one request's numbers, and the overview's four graphs
// carry the fixture's series. The views are walked through the sub-tab row in one document, the way
// owners switch views. Every view passes axe on both projects; on the phone none scrolls sideways
// and each of its targets is at least 44 px.

import { expect, test, type Locator, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { ConsoleErrors, n, openTab, signIn, SEEDED, theSetup, TOTALS, USAGE } from "./helpers.ts";
import { LOAD_REQUESTS } from "./seed.ts";

/** The views in their sub-tab order, each with the one thing only it shows once loaded. */
const VIEWS: readonly (readonly [
  view: string,
  label: string,
  role: "heading" | "row" | "region",
  loaded: RegExp,
])[] = [
  ["overview", "Overview", "heading", /Top sessions by spend/],
  ["costs", "Costs", "heading", /Cost attribution/],
  ["usage", "Usage", "heading", /Daily usage/],
  ["blocks", "Blocks", "heading", /Metering blocks/],
  ["models", "Models", "heading", /^Models$/],
  ["requests", "Requests", "row", /open details/],
  ["tools", "Tools", "heading", /Most used tools/],
  ["agents", "Agents", "region", /^Agents$/],
  ["routers", "Routers", "heading", /Router health/],
  ["alerts", "Alerts", "heading", /All clear|^Alerts$/],
  ["timeline", "Timeline", "heading", /^Timeline$/],
];

// the authenticator set-up the first sign-in offers is not radar's subject, so every browser
// starts with its "Later" already kept
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

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
  await page
    .getByRole("group", { name: "Model scope" })
    .getByRole("button", { name: "All models" })
    .click();
  await expect(card).toContainText("left");
  await expect(card).toContainText("3");
  await expect(card).toContainText("10k"); // the fixture's three glm calls summed
  await expect(card.locator("[data-block-left]")).toHaveText(/\dh\d{2}m/);
  const rows = page.locator("[data-blocks-table] tbody tr");
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText("3");
  // yesterday's window holds the flash call, the codex turn and the twelve load requests
  await expect(rows.nth(1)).toContainText(`${2 + LOAD_REQUESTS}`);

  // the overview's card carries the same widened scope
  await page
    .getByRole("navigation", { name: "Radar views" })
    .getByRole("link", { name: "Overview" })
    .click();
  await expect(page.locator("[data-block-card]")).toContainText("10k");
});

test("the requests table lists the seeded rows and the inspector opens one", async ({ page }) => {
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

test("the request sheet paints an opaque background over the table", async ({ page }) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/requests`);
  const first = SEEDED.requests[0];
  await page
    .getByRole("row")
    .filter({ hasText: n(first.input) })
    .filter({ hasText: n(first.output) })
    .click();

  // the shared kit's slide-over: its classes come from web/ui/src, so a fill means Tailwind scanned it
  const sheet = page.locator("dialog[open] [data-slide-over]");
  await expect(sheet).toBeVisible();
  const alpha = await sheet.evaluate((el) => {
    const probe = document.createElement("canvas").getContext("2d");
    if (!probe) return 0;
    probe.fillStyle = getComputedStyle(el).backgroundColor;
    probe.fillRect(0, 0, 1, 1);
    return probe.getImageData(0, 0, 1, 1).data[3] ?? 0;
  });
  expect(alpha).toBe(255);
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

test("the self-check badge is green when Radar matches the transcripts", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Radar");

  // today's three seeded calls, as Radar's history holds them and as the transcript wrote them
  const badge = page.locator("[data-selfcheck]");
  await expect(badge).toHaveAttribute("data-tone", "ok");
  await expect(badge).toContainText("Matches transcripts");
  const seeded = `3 requests, ${n(TOTALS.input)} in, ${n(TOTALS.output)} out, ${n(TOTALS.cacheRead)} cache read, ${n(TOTALS.cacheWrite)} cache write`;
  await expect(badge).toHaveAttribute("title", new RegExp(`Radar: ${seeded}\\.\\s+Transcripts: ${seeded}\\.`));
});

test("the requests table marks the call a router sent to its fallback model", async ({ page }) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/requests`);
  // yesterday's flash call is the one the seeded spool line says fell back from glm-5.3
  await page.getByRole("group", { name: "Range" }).getByRole("button", { name: "Week" }).click();
  const table = page.locator("[data-key='requests-table']");
  const marks = table.locator(".model-fallback");
  await expect(marks).toHaveCount(1);
  await expect(marks).toHaveText("from glm-5.3");
  await expect(marks).toHaveAttribute(
    "title",
    "glm-5.3 failed 3 times in a row; the router sent this to glm-5.3-flash",
  );
});

test("the costs table marks a cost priced at its family's rate", async ({ page }) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/costs`);
  // yesterday's usage load carries qwen3.8-max-0115-preview, an id the catalog does not know:
  // it prices at qwen3.8-max's rate, and its cost cell says the figure is approximate
  await page.getByRole("group", { name: "Period" }).getByRole("button", { name: "7 days" }).click();
  const row = page
    .locator("tr.attr-row")
    .filter({ has: page.locator("[title='qwen3.8-max-0115-preview']") });
  await expect(row).toHaveCount(1);
  await expect(row.locator("[data-approximate]")).toHaveText("≈");
  // a catalog model's cost stays exact
  const exact = page.locator("tr.attr-row").filter({ has: page.locator("[title='glm-5.3']") });
  await expect(exact.first()).toBeVisible();
  await expect(exact.first().locator("[data-approximate]")).toHaveCount(0);
});

/** Today's three glm-5.3 calls at the price table's 1.40 / 4.40 / 0.26 USD per million tokens. */
const TODAY_USD = (TOTALS.input * 1.4 + TOTALS.output * 4.4 + TOTALS.cacheRead * 0.26) / 1e6;

/** The flash call twenty-five hours back, 10 in and 5 out at glm-5.3-flash's 0.15 / 0.50. */
const FLASH_USD = (10 * 0.15 + 5 * 0.5) / 1e6;

/** One overview graph's series, as its root's data-series attribute carries them. */
async function graphSeries(graph: Locator): Promise<Record<string, number[]>> {
  await expect(graph).toHaveAttribute("data-series", /\S/);
  return JSON.parse((await graph.getAttribute("data-series")) ?? "{}") as Record<string, number[]>;
}

const sum = (values: readonly number[] | undefined): number =>
  (values ?? []).reduce((total, value) => total + value, 0);

/** The overview's graph by its data-graph name, once its chart has drawn. */
async function overviewGraph(page: Page, name: string): Promise<Locator> {
  const graph = page.locator(`[data-graph='${name}']`);
  await expect(graph.locator("svg").first()).toBeVisible();
  return graph;
}

test("the requests graph counts today's seeded calls by the hour", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Radar");
  const graph = await overviewGraph(page, "requests");
  await expect(graph).toContainText("Requests per hour");
  const series = await graphSeries(graph);
  expect(series.requests).toHaveLength(24);
  expect(sum(series.requests)).toBe(SEEDED.requests.length);
});

test("the tokens graph sums each kind over the seven days", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Radar");
  const graph = await overviewGraph(page, "tokens");
  await expect(graph).toContainText("Tokens by kind");
  const series = await graphSeries(graph);
  for (const kind of ["input", "output", "cacheRead", "cacheWrite"]) expect(series[kind]).toHaveLength(7);
  expect(sum(series.input)).toBe(USAGE.uncached);
  expect(sum(series.output)).toBe(USAGE.output);
  expect(sum(series.cacheRead)).toBe(USAGE.cached);
  expect(sum(series.cacheWrite)).toBe(TOTALS.cacheWrite);
});

test("the cost graph carries thirty days with today's seeded spend", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Radar");
  const graph = await overviewGraph(page, "cost");
  await expect(graph).toContainText("Cost per day");
  const series = await graphSeries(graph);
  expect(series.cost).toHaveLength(30);
  expect(series.cost?.at(-1)).toBeCloseTo(TODAY_USD, 9);
  // the days before hold the flash call twenty-five hours back, plus the load and the codex turn
  // beside it, whose unknown ids price at their families' rates
  expect(sum(series.cost?.slice(0, -1))).toBeGreaterThan(FLASH_USD);
});

test("the sessions graph names today's session, its machine and its cost", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Radar");
  const graph = page.locator("[data-graph='sessions']");
  await expect(graph).toContainText("Cost per session");
  await expect(graph).toContainText(theSetup().sessionTitle);
  await expect(graph).toContainText("e2e-machine");
  const series = await graphSeries(graph);
  expect(series.cost).toHaveLength(1);
  expect(series.cost?.[0]).toBeCloseTo(TODAY_USD, 9);
});

test("the four graphs stack in one column on a phone", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await signIn(page);
  await openTab(page, "Radar");
  const lefts: number[] = [];
  for (const name of ["requests", "tokens", "cost", "sessions"]) {
    const graph = page.locator(`[data-graph='${name}']`);
    await graph.scrollIntoViewIfNeeded();
    await expect(graph).toBeVisible();
    const box = await graph.boundingBox();
    expect(box?.width ?? 0).toBeLessThanOrEqual(393);
    lefts.push(Math.round(box?.x ?? -1));
    // the chart itself fits its card: nothing drawn past the phone's edge
    const chart = await graph.locator("svg, ol").first().boundingBox();
    expect((chart?.x ?? 0) + (chart?.width ?? 0)).toBeLessThanOrEqual(393);
  }
  expect(new Set(lefts).size).toBe(1);
  expect(sum((await graphSeries(page.locator("[data-graph='requests']"))).requests)).toBe(SEEDED.requests.length);
});

/** Opens one radar view by its address in a signed-in browser, once its panels settled: the
 *  skeletons are gone, or three seconds passed. */
async function openView(page: Page, view: string): Promise<void> {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/${view}`);
  await expect(page.locator("[data-radar-view]")).toBeVisible();
  await page
    .waitForFunction(() => document.querySelectorAll(".skeleton").length === 0, undefined, { timeout: 3000 })
    .catch(() => {});
}

for (const [view, label] of VIEWS) {
  test(`the ${label} view passes axe`, async ({ page }) => {
    await openView(page, view);
    await expectAccessible(page);
  });
}

test.describe("radar on the phone", () => {
  test.skip(({ isMobile }) => !isMobile, "the phone's width and touch");

  for (const [view, label] of VIEWS) {
    test(`the ${label} view scrolls nothing sideways and its targets are 44 px`, async ({ page }) => {
      await openView(page, view);
      // the page and the pane that scrolls the view: neither is wider than the phone; a wide
      // table scrolls inside its own frame
      const over = await page.evaluate(() => {
        const pane = document.querySelector("[data-radar-view]")?.parentElement;
        const edge = pane ? pane.getBoundingClientRect().left + pane.clientWidth : 0;
        // the parts that reach past the pane's edge, less those a scroll box of their own holds
        const past = pane
          ? [...pane.querySelectorAll<HTMLElement>("*")]
              .filter((el) => el.getBoundingClientRect().right > edge + 0.5)
              .filter((el) => {
                for (let p = el.parentElement; p && p !== pane; p = p.parentElement) {
                  if (getComputedStyle(p).overflowX !== "visible") return false;
                }
                return true;
              })
              .map((el) => `${el.tagName.toLowerCase()}.${[...el.classList].slice(0, 3).join(".")}`)
          : [];
        return {
          page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          pane: pane ? pane.scrollWidth - pane.clientWidth : 0,
          past,
        };
      });
      expect(over, `${label} scrolls sideways`).toEqual({ page: 0, pane: 0, past: [] });
      // every button, link and tab the view draws is a thumb's target
      const small = await page.locator("[data-radar-view]").evaluate((root) =>
        [...root.querySelectorAll<HTMLElement>("button, a[href], [role='tab'], select, input")]
          .filter((el) => {
            const r = el.getBoundingClientRect();
            return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden";
          })
          .filter((el) => {
            const r = el.getBoundingClientRect();
            return r.width < 44 || r.height < 44;
          })
          .map((el) => {
            const r = el.getBoundingClientRect();
            const name = (el.getAttribute("aria-label") ?? el.textContent ?? "").trim().slice(0, 30);
            return `${el.tagName.toLowerCase()} "${name}" ${Math.round(r.width)}x${Math.round(r.height)}`;
          }),
      );
      expect(small, `${label}: targets under 44 px`).toEqual([]);
    });
  }
});
