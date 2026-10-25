// radar.spec.ts — the Radar tab over seeded data: every view loads without a console error,
// the overview's Day cards carry the fixture's exact totals, and the requests table lists the
// three seeded calls and yesterday's router-only call with its streaming latencies, the inspector
// showing one request's numbers, and the overview's four graphs
// carry the fixture's series. The views are walked through the views nav in one document, the way
// owners switch views: the phone's nav holds four views and the Overview's More list the rest, the
// tablet and the desktop list every view in a sidebar. Every view passes axe on both projects; on
// the phone none scrolls sideways and each of its targets is at least 44 px.

import { expect, test, type Locator, type Page } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import {
  ConsoleErrors,
  cutStreams,
  cuttableStreams,
  flowingStreams,
  n,
  openTab,
  returnAfter,
  signIn,
  SEEDED,
  theSetup,
  TOTALS,
  USAGE,
} from "./helpers.ts";
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

/** Opens one view the way an owner does: its entry in the views nav, or — for a view the phone's
 *  nav leaves out — the Overview first (its entry or the back link), then the More list's row.
 *  Each is a hash link, so the same document repaints onto the view, no reload. */
async function pickView(page: Page, label: string): Promise<void> {
  const nav = page.getByRole("navigation", { name: "Radar views" });
  const back = page.getByRole("link", { name: "Back to Overview" });
  // the nav or the back link paints first: the counts below read what the pane shows
  await page.locator("[data-radar-views]").waitFor();
  if ((await back.count()) > 0) {
    await back.click();
    await expect(nav.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");
  }
  const entry = nav.getByRole("link", { name: label });
  if ((await entry.count()) > 0) {
    await entry.click();
    await expect(entry).toHaveAttribute("aria-current", "page");
    return;
  }
  await nav.getByRole("link", { name: "Overview" }).click();
  await page.getByRole("navigation", { name: "More views" }).getByRole("link", { name: label }).click();
  await expect(page.locator("[data-radar-view]")).toHaveAttribute("aria-label", label);
}

test("every radar view loads with no console error", async ({ page }) => {
  const errors = new ConsoleErrors(page);
  await signIn(page); // a first signed-in load proves the cookie works before the loop
  await openTab(page, "Radar");
  for (const [, label, role, loaded] of VIEWS) {
    await pickView(page, label);
    await expect(page.getByRole(role, { name: loaded }).first()).toBeVisible();
  }
  expect(errors.errors()).toEqual([]);
});

/** The views the phone's nav holds; every other view opens from the Overview's More list. */
const PHONE_NAV: readonly string[] = ["Overview", "Requests", "Costs", "Alerts"];

/** The Overview's More list on the phone, in sub-tab order. */
const MORE: readonly string[] = ["Models", "Usage", "Blocks", "Tools", "Agents", "Routers", "Timeline"];

/** Every view in the sidebar's sub-tab order. */
const SIDEBAR: readonly string[] = [
  "Overview",
  "Costs",
  "Models",
  "Usage",
  "Blocks",
  "Requests",
  "Tools",
  "Agents",
  "Routers",
  "Alerts",
  "Timeline",
];

test.describe("radar's view navigation", () => {
  test("on a 390 px phone the nav holds four views that fit, and no chip row", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page);
    await page.goto(`${theSetup().rootUrl}/#/radar/overview`);
    const nav = page.getByRole("navigation", { name: "Radar views" });
    await expect(nav.getByRole("link")).toHaveText(PHONE_NAV.map((label) => new RegExp(`^${label}`)));
    // nothing scrolls inside the nav, and every entry sits inside the phone's width
    expect(await nav.evaluate((el) => el.scrollWidth - el.clientWidth)).toBe(0);
    for (const box of await nav.getByRole("link").evaluateAll((links) =>
      links.map((link) => link.getBoundingClientRect().right),
    )) {
      expect(box).toBeLessThanOrEqual(390);
    }
  });

  test("on the phone Models opens from the Overview's More list, and its back control lands on the Overview", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page);
    await page.goto(`${theSetup().rootUrl}/#/radar/overview`);
    const more = page.getByRole("navigation", { name: "More views" });
    await expect(more.getByRole("link")).toHaveText(MORE);
    await more.getByRole("link", { name: "Models" }).click();
    await expect(page).toHaveURL(/#\/radar\/models$/);
    await expect(page.getByRole("heading", { name: /^Models$/ }).first()).toBeVisible();
    await page.getByRole("link", { name: "Back to Overview" }).click();
    await expect(page).toHaveURL(/#\/radar\/overview$/);
    await expect(
      page.getByRole("navigation", { name: "Radar views" }).getByRole("link", { name: "Overview" }),
    ).toHaveAttribute("aria-current", "page");
  });

  test("on the phone a deep link to Models shows the view and its back control", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page);
    await page.goto(`${theSetup().rootUrl}/#/radar/models`);
    await expect(page.getByRole("heading", { name: /^Models$/ }).first()).toBeVisible();
    await expect(page.getByRole("link", { name: "Back to Overview" })).toHaveAttribute(
      "href",
      "#/radar/overview",
    );
  });

  test("at 1024 px a sidebar lists every view beside the pane", async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 768 });
    await signIn(page);
    await page.goto(`${theSetup().rootUrl}/#/radar/models`);
    const nav = page.getByRole("navigation", { name: "Radar views" });
    await expect(nav.getByRole("link")).toHaveText(SIDEBAR.map((label) => new RegExp(`^${label}`)));
    await expect(nav.getByRole("link", { name: "Models" })).toHaveAttribute("aria-current", "page");
    // a column: every entry starts at one left edge, one under the other, left of the view
    const boxes = await nav.getByRole("link").evaluateAll((links) =>
      links.map((link) => {
        const r = link.getBoundingClientRect();
        return { x: Math.round(r.x), top: r.top };
      }),
    );
    expect(new Set(boxes.map((b) => b.x)).size).toBe(1);
    for (let i = 1; i < boxes.length; i += 1) {
      expect(boxes[i]?.top ?? 0).toBeGreaterThan(boxes[i - 1]?.top ?? 0);
    }
    const navBox = await nav.boundingBox();
    const paneBox = await page.locator("[data-radar-view]").boundingBox();
    expect((navBox?.x ?? 0) + (navBox?.width ?? 0)).toBeLessThanOrEqual(paneBox?.x ?? 0);
  });
});

test("the overview's Day cards show the seeded totals", async ({ page, isMobile }) => {
  await signIn(page);
  await openTab(page, "Radar");

  // first load sits on the default hour, and the control says so
  const range = page.getByRole("group", { name: "Range" });
  await expect(range.getByRole("button", { name: "Hour" })).toHaveAttribute("aria-pressed", "true");

  // the owner's Day range: the requests the window holds are the three seeded ones
  await range.getByRole("button", { name: "Day" }).click();
  await expect(range.getByRole("button", { name: "Day" })).toHaveAttribute("aria-pressed", "true");

  if (isMobile) {
    // the phone's tiles name the window once: three requests in it
    const tiles = page.locator("[data-phone-tiles]");
    await expect(tiles.locator("#ovw-window")).toHaveText("Last 24 hours");
    await expect(tiles.locator("[data-stat='requests'] .num")).toHaveText("3");
    return;
  }

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

test("the overview's tiles count live sessions, the router's health and HTTP failures only", async ({
  page,
  isMobile,
}) => {
  // two calls beside the seeded ones, inside the default hour: a 502 the router answered with no
  // stop reason, and a 499 its client cancelled
  await page.route("**/api/radar/api/requests?*", async (route) => {
    const answer = await route.fetch();
    const body = (await answer.json()) as { requests: Record<string, unknown>[] };
    const seed = body.requests[0];
    if (seed === undefined) throw new Error("the seeded requests are missing");
    const at = Date.now() - 30_000;
    const call = (status: string) => ({
      ...seed,
      id: `e2e-${status}`,
      ts: at,
      status,
      stopReason: null,
      tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    });
    await route.fulfill({
      response: answer,
      json: { requests: [...body.requests, call("502"), call("499")] },
    });
  });
  await signIn(page);
  await openTab(page, "Radar");

  const requests = page.locator("[data-stat='requests']");
  await expect(requests).toContainText("1 failed");
  // the phone's requests tile carries the failures alone; the desk's says the cancelled apart
  if (!isMobile) await expect(requests).toContainText("1 cancelled");

  // sessions: the live ones the store holds, not every session it ever saw
  const sessions = await page.evaluate(async () => {
    const r = await fetch("/api/radar/api/sessions");
    return ((await r.json()) as { sessions: { live: boolean }[] }).sessions;
  });
  const live = sessions.filter((s) => s.live).length;
  expect(sessions.length).toBeGreaterThan(live);
  const tile = page.locator("[data-stat='sessions']");
  await expect(tile).toContainText(isMobile ? "Sessions live" : "live now");
  await expect(tile.locator(".num").first()).toHaveText(String(live));

  // routers: this run sets zai up and starts no huddle-router, so the list names the one enabled
  // provider as down instead of counting plugins; the phone leaves the routers tile to that list
  await expect(page.locator("[aria-labelledby='ovw-routers'] [data-router='zai']")).toContainText("down");
  await expect(page.locator("[aria-labelledby='ovw-routers']")).not.toContainText(":1878");
  if (!isMobile) await expect(page.locator("[data-stat='routers'] .num")).toHaveText("down");
  await expectAccessible(page);
});

test("the overview leads with a dash for Anthropic's limits while no claude-* answer carried them, and spend reads list price", async ({
  page,
  isMobile,
}) => {
  // the seeded spool holds provider calls only, so no answer has reported the unified windows
  await openView(page, "overview");
  const limits = page.locator('[data-stat="limits"]');
  await expect(limits).toBeVisible();
  await expect(limits.locator(".num").first()).toHaveText("–");
  await expect(limits).toContainText("no Anthropic answer yet");
  // the first tile on the desk's row and the first on the phone's screen
  await expect(page.locator("[data-stat]").first()).toHaveAttribute("data-stat", "limits");
  await expect(page.locator(isMobile ? '[data-stat="spend"]' : '[data-stat="spend today"]')).toContainText(
    "list price",
  );
  await expectAccessible(page);
});

test("the overview's first tile shows 5 h and 7 d used with their resets once an answer reported them", async ({
  page,
}) => {
  // the radar stream's summary as it reads once a claude-* answer carried the unified headers:
  // the page's own stream, with the newest windows set on every summary it delivers
  await page.addInitScript(() => {
    const now = Date.now() / 1000;
    const utilization = {
      five_hour: { used: 0.42, resets_at: Math.floor(now + 2 * 3600 + 30 * 60) },
      seven_day: { used: 0.61, resets_at: Math.floor(now + 3 * 86_400 + 5 * 3600 + 30 * 60) },
    };
    const Native = window.EventSource;
    class Seeded extends Native {
      get onmessage(): ((this: EventSource, event: MessageEvent) => unknown) | null {
        return super.onmessage;
      }
      set onmessage(handler: ((this: EventSource, event: MessageEvent) => unknown) | null) {
        if (handler === null || !this.url.includes("/api/radar/api/stream")) {
          super.onmessage = handler;
          return;
        }
        super.onmessage = function (event: MessageEvent) {
          const data: unknown = typeof event.data === "string" ? JSON.parse(event.data) : null;
          if (typeof data !== "object" || data === null) return handler.call(this, event);
          const message = data as { type?: string; summary?: Record<string, unknown> };
          if ((message.type !== "snapshot" && message.type !== "sessions") || message.summary === undefined)
            return handler.call(this, event);
          message.summary.utilization = utilization;
          return handler.call(this, new MessageEvent("message", { data: JSON.stringify(message) }));
        };
      }
    }
    window.EventSource = Seeded;
  });
  await openView(page, "overview");
  const limits = page.locator('[data-stat="limits"]');
  const fiveHour = limits.locator('[data-limit-window="5 h"]');
  const sevenDay = limits.locator('[data-limit-window="7 d"]');
  await expect(fiveHour.locator(".num")).toHaveText("42%");
  await expect(fiveHour).toContainText("resets in 2h");
  await expect(sevenDay.locator(".num")).toHaveText("61%");
  await expect(sevenDay).toContainText("resets in 3d 5h");
  await expect(limits).not.toContainText("no Anthropic answer yet");
  await expectAccessible(page);
});

test("the phone's overview leads with three one-line tiles under one window label; the desk keeps six cards", async ({
  page,
  isMobile,
}) => {
  // a call inside the default hour on a model no price table knows, so the spend tile has an
  // unpriced count to show
  await page.route("**/api/radar/api/requests?*", async (route) => {
    const answer = await route.fetch();
    const body = (await answer.json()) as { requests: Record<string, unknown>[] };
    const seed = body.requests[0];
    if (seed === undefined) throw new Error("the seeded requests are missing");
    const call = { ...seed, id: "e2e-unpriced", ts: Date.now() - 30_000, model: "mystery-model" };
    await route.fulfill({ response: answer, json: { requests: [...body.requests, call] } });
  });
  await openView(page, "overview");
  if (!isMobile) {
    await expect(page.locator("[data-phone-tiles]")).toHaveCount(0);
    await expect(page.locator("[data-stat-cards] [data-stat]")).toHaveCount(6);
    return;
  }
  const tiles = page.locator("[data-phone-tiles]");
  await expect(tiles).toBeVisible();
  await expect(page.locator("[data-stat-cards]")).toHaveCount(0);
  await expect(tiles.locator("#ovw-window")).toHaveText("Last hour");
  await expect(tiles.locator("[data-stat]")).toHaveCount(3);
  await expect(tiles.locator('[data-stat="spend"]')).toContainText("1 unpriced");
  // all three in the first screen, each figure on one line inside its tile
  const viewport = page.viewportSize();
  const fits = await tiles.evaluate((root) =>
    [...root.querySelectorAll<HTMLElement>("[data-stat]")].map((tile) => {
      const value = tile.querySelector<HTMLElement>(".num");
      const box = tile.getBoundingClientRect();
      const line = value ? Number.parseFloat(getComputedStyle(value).lineHeight) : 0;
      return {
        stat: tile.getAttribute("data-stat"),
        bottom: Math.round(box.bottom),
        oneLine: value !== null && value.getBoundingClientRect().height < line * 1.5,
        inside: value !== null && value.scrollWidth <= value.clientWidth,
      };
    }),
  );
  for (const fit of fits) {
    expect(fit.bottom, `${fit.stat} sits in the first screen`).toBeLessThanOrEqual(viewport?.height ?? 0);
    expect(fit, `${fit.stat} on one line`).toMatchObject({ oneLine: true, inside: true });
  }
  await expectAccessible(page);
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
  // yesterday's window holds the flash call, the timed call, the codex turn and the twelve load requests
  await expect(rows.nth(1)).toContainText(`${3 + LOAD_REQUESTS}`);

  // the overview's card carries the same widened scope
  await pickView(page, "Overview");
  await expect(page.locator("[data-block-card]")).toContainText("10k");
});

test("the requests table lists the seeded rows and the inspector opens one", async ({ page }) => {
  await signIn(page);
  // the Requests view by its own address: the Week range brings yesterday's timed call in, and
  // the model chip narrows it to the three seeded calls and that call
  await page.goto(`${theSetup().rootUrl}/#/radar/requests`);
  await page.getByRole("group", { name: "Range" }).getByRole("button", { name: "Week" }).click();
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

  // yesterday's router-only call is the row that measured its streaming latencies; the second
  // seeded call's route line said none
  const timed = page
    .getByRole("row")
    .filter({ hasText: n(TIMED.input) })
    .filter({ hasText: n(TIMED.output) });
  await expect(timed.locator("td[data-label='First byte']")).toHaveText("180ms");
  await expect(timed.locator("td[data-label='First token']")).toHaveText("420ms");
  const plain = page
    .getByRole("row")
    .filter({ hasText: n(SEEDED.requests[1]!.input) })
    .filter({ hasText: n(SEEDED.requests[1]!.output) });
  await expect(plain.locator("td[data-label='First byte']")).toHaveText("–");

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
  await expect(table.locator("[data-http='200']").first()).toBeVisible();
  await expect(table.locator(".ui-badge-fail")).toHaveCount(0);
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

/** The timed call beside it: a router-only glm-5.3 line whose route line measured its streams. */
const TIMED = { input: 777, output: 111 } as const;

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
  // the timed call of yesterday sits inside the seven days with its own 777 in and 111 out
  expect(sum(series.input)).toBe(USAGE.uncached + TIMED.input);
  expect(sum(series.output)).toBe(USAGE.output + TIMED.output);
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

test("the inspector's header is solid, and on the phone the sheet covers the viewport with a Back control", async ({
  page,
  isMobile,
}) => {
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/requests`);
  const first = SEEDED.requests[0];
  await page
    .getByRole("row")
    .filter({ hasText: n(first.input) })
    .filter({ hasText: n(first.output) })
    .click();
  const sheet = page.locator("dialog[open] [data-slide-over]");
  await expect(sheet).toBeVisible();

  // the sticky header paints its own opaque fill, so the body never shows through it
  const alpha = await sheet.evaluate((el) => {
    const head = el.firstElementChild;
    const probe = document.createElement("canvas").getContext("2d");
    if (!(head instanceof HTMLElement) || !probe) return 0;
    probe.fillStyle = getComputedStyle(head).backgroundColor;
    probe.fillRect(0, 0, 1, 1);
    return probe.getImageData(0, 0, 1, 1).data[3] ?? 0;
  });
  expect(alpha).toBe(255);

  const back = page.getByRole("button", { name: "Back to requests" });
  const viewport = page.viewportSize();
  // where the sheet's edges sit once the slide-in has landed
  const landed = async () => {
    const box = await sheet.boundingBox();
    return box === null ? null : { left: Math.round(box.x), right: Math.round(box.x + box.width) };
  };
  await expect.poll(async () => (await landed())?.right).toBe(viewport?.width);
  if (!isMobile) {
    // the desk keeps the slide-over beside the page it floats over
    await expect(back).toBeHidden();
    expect((await landed())?.left ?? 0).toBeGreaterThan(0);
    return;
  }
  // the phone's sheet is the whole viewport: no strip of the page, no tab bar under it
  expect(await landed()).toEqual({ left: 0, right: viewport?.width });
  expect((await sheet.boundingBox())?.height).toBe(viewport?.height);
  const atTabBar = await page.evaluate(
    ([x, y]) => document.elementFromPoint(x ?? 0, y ?? 0)?.closest("[data-slide-over]") !== null,
    [(viewport?.width ?? 0) / 2, (viewport?.height ?? 0) - 10],
  );
  expect(atTabBar).toBe(true);
  // the Back control is a full tap target and closes the sheet back onto the table
  const target = await back.boundingBox();
  expect(target?.width ?? 0).toBeGreaterThanOrEqual(44);
  expect(target?.height ?? 0).toBeGreaterThanOrEqual(44);
  await back.click();
  await expect(sheet).toBeHidden();
  await expect(page.getByRole("row").filter({ hasText: n(first.input) }).first()).toBeVisible();
});

test("the inspector attaches no history to a call the transcript holds no copy of", async ({ page }) => {
  // a router-only call beside the first seeded one: the same session and agent, 12 ms later, with no
  // transcript copy; the context route answers it with the agent's history up to that instant
  const routerOnly = "route:e2e-router-only";
  let seedId = "";
  await page.route("**/api/radar/api/requests?*", async (route) => {
    const answer = await route.fetch();
    const body = (await answer.json()) as { requests: Record<string, unknown>[] };
    const first = SEEDED.requests[0];
    const seed = body.requests.find((r) => (r.tokens as { input?: number } | undefined)?.input === first.input);
    if (seed === undefined) throw new Error("the first seeded request is missing");
    seedId = String(seed.id);
    const call = {
      ...seed,
      id: routerOnly,
      ts: Number(seed.ts) + 12,
      stopReason: null,
      status: "200",
      tokens: { input: 7, output: 3, cacheRead: 0, cacheWrite: 0 },
    };
    await route.fulfill({ response: answer, json: { requests: [...body.requests, call] } });
  });
  await page.route(`**/api/radar/api/history/context/${encodeURIComponent(routerOnly)}?*`, async (route) => {
    const url = route.request().url().replace(encodeURIComponent(routerOnly), encodeURIComponent(seedId));
    await route.fulfill({ response: await route.fetch({ url }) });
  });
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/radar/requests`);
  await page
    .getByRole("row")
    .filter({ has: page.locator("td", { hasText: /^7$/ }) })
    .filter({ has: page.locator("td", { hasText: /^3$/ }) })
    .first()
    .click();
  const sheet = page.locator("dialog[open] [data-slide-over]");
  await expect(sheet).toBeVisible();
  await sheet.getByRole("tab", { name: /Context/ }).click();
  await expect(sheet.getByText("The transcript holds no copy of this call.")).toBeVisible();
  await expect(sheet.locator(".ctx-msg")).toHaveCount(0);
  await expect(sheet.locator("[data-context-totals]")).toHaveCount(0);
});

/** Radar's stream as the hub serves it. */
const RADAR_STREAM = "/api/radar/api/stream";

test("a Radar stream gone silent is replaced within 40 s", async ({ page }) => {
  await test.step("Given the Radar tab on a stream that goes quiet with no error", async () => {
    await cuttableStreams(page);
    await page.clock.install();
    await signIn(page);
    await openTab(page, "Radar");
    await expect.poll(() => flowingStreams(page, RADAR_STREAM)).toBe(1);
    expect(await cutStreams(page, RADAR_STREAM)).toBe(1);
  });

  await test.step("Then 40 s of silence replace the stream with one that flows", async () => {
    await page.clock.fastForward(40_000);
    await expect.poll(() => flowingStreams(page, RADAR_STREAM), { timeout: 5_000 }).toBe(1);
  });
});

test("a Radar page back from the background re-subscribes within 5 s", async ({ page }) => {
  await test.step("Given the Radar tab on a stream that goes quiet with no error", async () => {
    await cuttableStreams(page);
    await signIn(page);
    await openTab(page, "Radar");
    await expect.poll(() => flowingStreams(page, RADAR_STREAM)).toBe(1);
    expect(await cutStreams(page, RADAR_STREAM)).toBe(1);
  });

  await test.step("When the page hides, 30 s pass, and it turns visible again", async () => {
    await returnAfter(page, 30_000);
  });

  await test.step("Then a fresh stream flows within 5 s", async () => {
    await expect.poll(() => flowingStreams(page, RADAR_STREAM), { timeout: 5_000 }).toBe(1);
  });
});
