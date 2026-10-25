// fetchrate.spec.ts — the render-loop guard: every page the hub's router serves (the five tabs,
// every Radar view, every page of the seeded channel) is opened in its own signed-in browser
// context and watched for five seconds, and no URL may be requested more than twice a second
// in that window. An effect that refetches on every repaint (the 2026-10-22 hub crash, about
// 500 reads of /api/channels a second) fails here naming the page and the URL.

import { type Browser, type BrowserContext, expect, test } from "@playwright/test";
import { DESTS, RADAR_VIEWS, TABS } from "../hub/src/app/router.ts";
import { signIn, theSetup } from "./helpers.ts";

/** How long each page is watched. */
const WINDOW_MS = 5_000;

/** The most requests one URL may take per second of the window. */
const MAX_PER_SECOND = 2;

/** The channel global-setup seeds. */
const CHANNEL = "e2e";

/** Every hash the router serves a page at: the tabs, each Radar view, each channel page. */
const PAGES: readonly string[] = [
  ...TABS.map(([, , href]) => href),
  ...RADAR_VIEWS.map((view) => `#/radar/${view}`),
  ...["overview", "today", ...DESTS.map(([dest]) => dest)].map((dest) => `#/channels/${CHANNEL}/${dest}`),
];

/** One page's busiest URL over the window: the URL (method, origin and path, the query left
 *  out so a loop that changes a cursor still counts as one URL) and how often it was asked. */
type Busiest = { page: string; url: string; count: number };

/** The cookies and storage a signed-in context carries. */
type SignedIn = Awaited<ReturnType<BrowserContext["storageState"]>>;

/** The request key: the method plus the URL without its query or fragment. */
function keyOf(method: string, raw: string): string {
  const url = new URL(raw);
  return `${method} ${url.origin}${url.pathname}`;
}

/** Opens one hash in a fresh tab of a context that carries the sign-in, counts every request
 *  per URL from the navigation on for the window, and answers the busiest one. */
async function watch(browser: Browser, state: SignedIn, hash: string): Promise<Busiest> {
  // a context of its own per page: Chromium's six connections per host are per context, and
  // each page holds its live streams open
  const context = await browser.newContext({ storageState: state, serviceWorkers: "block" });
  try {
    const page = await context.newPage();
    const counts = new Map<string, number>();
    page.on("request", (request) => {
      const key = keyOf(request.method(), request.url());
      counts.set(key, (counts.get(key) ?? 0) + 1);
    });
    await page.goto(`${theSetup().rootUrl}/${hash}`);
    // signed in is the shell itself: a page that fell back to the landing page counts nothing
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
    await page.waitForTimeout(WINDOW_MS);
    let busiest: Busiest = { page: hash, url: "", count: 0 };
    for (const [url, count] of counts) {
      if (count > busiest.count) busiest = { page: hash, url, count };
    }
    return busiest;
  } finally {
    await context.close();
  }
}

test("no hub page asks one URL more than twice a second", async ({ browser, page }) => {
  test.setTimeout(90_000);
  await signIn(page);
  const state = await page.context().storageState();
  const busiest = await Promise.all(PAGES.map((hash) => watch(browser, state, hash)));
  const limit = (MAX_PER_SECOND * WINDOW_MS) / 1000;
  const over = busiest.filter((b) => b.count > limit).map((b) => `${b.page}: ${b.url} x${b.count}`);
  expect(over, `pages above ${limit} requests to one URL in ${WINDOW_MS / 1000} s`).toEqual([]);
});
