// notify-icon.spec.ts — the browser side of the huddle notification icon: the white badge PNG
// the service worker's pushes carry and the full-colour icon the page's notifications name are
// both served as real PNGs, and the served worker names the badge. The pixels themselves are
// the icon generator's unit tests' subject; this proves the served path.

import { expect, test } from "@playwright/test";
import { signIn, theSetup } from "./helpers.ts";

test("the badge and the icon answer as PNGs, and the worker badges pushes with the badge", async ({
  page,
}) => {
  await signIn(page);
  const root = theSetup().rootUrl;
  for (const path of ["/icons/icon-badge-96.png", "/icons/icon-192.png"]) {
    const answer = await page.request.get(`${root}${path}`);
    expect(answer.status(), path).toBe(200);
    expect(answer.headers()["content-type"], path).toContain("image/png");
  }
  const worker = await page.request.get(`${root}/sw.js`);
  expect(worker.status()).toBe(200);
  expect(await worker.text()).toContain(`badge: "/icons/icon-badge-96.png"`);
});
