// phone-perf.spec.ts — the hub on a slow phone, against the built binary over TLS (the only
// origin a browser offers brotli to): an iPhone-sized Chromium with its CPU slowed 4x on the
// CDP "Slow 4G" link (1.6 Mbps down, 150 ms round trip) opens the Sessions page cold, then again.
// The cold open stays under the byte budget and turns interactive under the time budget, the
// second open moves no asset byte, and no URL is asked for more than twice in the first 10 s.

import { expect, type CDPSession, type Page, test } from "@playwright/test";
import { theSetup } from "./helpers.ts";

/** Everything a cold open of the Sessions page may move over the wire, compressed. */
const firstLoadBudgetBytes = 300_000;

/** From navigation start to the Now list on screen, on the slowed phone. */
const interactiveBudgetMs = 3000;

/** The window the request counter watches. */
const watchMs = 10_000;

/** CDP's "Slow 4G" preset: bytes per second down and up, and the round trip. */
const SLOW_4G = {
  offline: false,
  downloadThroughput: (1.6 * 1024 * 1024) / 8,
  uploadThroughput: (750 * 1024) / 8,
  latency: 150,
};

/** What one load moved: encoded bytes per URL, as the network stack counted them. */
type Transfer = { bytes: Map<string, number>; encodings: Map<string, string> };

/** Starts counting the encoded bytes each finished request moved. */
function countTransfer(cdp: CDPSession): Transfer {
  const urls = new Map<string, string>();
  const transfer: Transfer = { bytes: new Map(), encodings: new Map() };
  cdp.on("Network.responseReceived", (e) => {
    urls.set(e.requestId, e.response.url);
    const headers = Object.fromEntries(
      Object.entries(e.response.headers).map(([k, v]) => [k.toLowerCase(), String(v)]),
    );
    transfer.encodings.set(e.response.url, headers["content-encoding"] ?? "");
  });
  cdp.on("Network.loadingFinished", (e) => {
    const url = urls.get(e.requestId);
    if (url !== undefined) transfer.bytes.set(url, (transfer.bytes.get(url) ?? 0) + e.encodedDataLength);
  });
  return transfer;
}

const total = (t: Transfer): number => [...t.bytes.values()].reduce((a, b) => a + b, 0);

/** Signs the browser in on the TLS hub with a fresh one-time code, then leaves the hub. */
async function signInOverTls(page: Page): Promise<void> {
  const { hubRootUrl, hubToken, hubUrl } = theSetup();
  const r = await fetch(`${hubRootUrl}/api/login`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-huddle-action": "/api/login",
      "x-huddle-token": hubToken,
    },
    body: "{}",
  });
  if (!r.ok) throw new Error(`/api/login answered ${r.status}`);
  const { code } = (await r.json()) as { code: string };
  // the authenticator set-up stays skipped, the way its "Later" keeps it: the measured opens
  // land on the Sessions page itself
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  const settled = page.waitForURL((url) => !url.searchParams.has("code"));
  await page.goto(`${hubUrl}/?code=${code}`);
  await settled;
  await page.goto("about:blank");
}

/** Opens the Sessions page and answers the milliseconds from navigation start to its list on screen. */
async function openSessions(page: Page): Promise<number> {
  await page.goto(`${theSetup().hubUrl}/#/sessions`);
  await expect(page.getByRole("heading", { name: /^Working · \d+$/ })).toBeVisible({ timeout: 20_000 });
  return page.evaluate(() => Date.now() - performance.timing.navigationStart);
}

test("the Sessions page opens fast on a slow phone, and the second open moves no asset byte", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await signInOverTls(page);

  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Network.enable");
  await cdp.send("Network.clearBrowserCache");
  await cdp.send("Network.emulateNetworkConditions", SLOW_4G);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });

  const asked = new Map<string, number>();
  page.on("request", (request) => asked.set(request.url(), (asked.get(request.url()) ?? 0) + 1));

  const cold = countTransfer(cdp);
  const started = Date.now();
  const interactive = await openSessions(page);
  const firstBytes = total(cold);
  console.log(`phone-perf: first open ${firstBytes} bytes, interactive in ${interactive} ms`);
  for (const [url, bytes] of [...cold.bytes].sort((a, b) => b[1] - a[1])) {
    console.log(`  ${bytes}\t${cold.encodings.get(url) || "identity"}\t${url}`);
  }

  const app = [...cold.encodings].find(([url]) => /\/static\/app\.[^/]+\.js$/.test(url));
  expect(app?.[1], "the app module travels as brotli").toBe("br");
  expect(firstBytes).toBeLessThan(firstLoadBudgetBytes);
  expect(interactive).toBeLessThan(interactiveBudgetMs);

  await page.waitForTimeout(Math.max(0, watchMs - (Date.now() - started)));
  const twice = [...asked].filter(([, count]) => count > 2);
  expect(twice, "no URL is asked for more than twice in the first 10 s").toEqual([]);

  // the second open: the shell page itself goes to the network (never stored), every asset
  // answers from the cache
  await page.goto("about:blank");
  const warm = countTransfer(cdp);
  await openSessions(page);
  expect(warm.bytes.has(`${theSetup().hubUrl}/`), "the shell page goes to the network again").toBe(true);
  const assetBytes = [...warm.bytes]
    .filter(([url]) => /\/static\//.test(new URL(url).pathname))
    .reduce((sum, [, bytes]) => sum + bytes, 0);
  console.log(`phone-perf: second open ${total(warm)} bytes, ${assetBytes} of them assets`);
  expect(assetBytes).toBe(0);
});
