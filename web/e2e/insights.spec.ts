// insights.spec.ts — the Insights card on Sessions: the tips GET /api/insights answers, each one
// sentence with its code and source, and a tip's session as a link. The fixture answer pins what
// the card draws and that it fits a phone; the live answer proves the route serves the owner; an
// answer without a tip leaves the Now list without the card.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { openTab, signIn, theSetup } from "./helpers.ts";

const INSIGHTS_ROUTE = "**/api/insights";

/** One always-allowed tip, as the route answers it. */
const FIXTURE = {
  tips: [
    {
      id: "always_allowed:Bash(go test:*)",
      rule: "always_allowed",
      sentence:
        "You allowed `Bash(go test:*)` 5 times in 7 days, waiting 2 min each time. An allow rule would skip the prompt.",
      count: 5,
      wait_ms: 120000,
    },
  ],
};

test("the Insights card on Sessions shows a tip's sentence, code and source", async ({ page }) => {
  // the authenticator set-up the first sign-in offers is not this page's subject
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await page.route(INSIGHTS_ROUTE, (route) => route.fulfill({ json: FIXTURE }));
  await signIn(page);
  await openTab(page, "Sessions");
  const card = page.locator("[data-insights]");
  await expect(card).toBeVisible();
  await expect(card.locator("code")).toHaveText("Bash(go test:*)");
  await expect(card).toContainText("An allow rule would skip the prompt");
  await expect(card).toContainText("From your permission asks, last 7 days");
  await expectAccessible(page);
});

test("the Insights card fits a phone", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await page.setViewportSize({ width: 393, height: 852 });
  await page.route(INSIGHTS_ROUTE, (route) => route.fulfill({ json: FIXTURE }));
  await signIn(page);
  await openTab(page, "Sessions");
  const card = page.locator("[data-insights]");
  await card.scrollIntoViewIfNeeded();
  await expect(card).toBeVisible();
  const box = await card.boundingBox();
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(393);
});

test("the live route answers the owner, and an answer without a tip shows no card", async ({
  page,
}) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  const answer = await page.request.get(`${theSetup().rootUrl}/api/insights`);
  expect(answer.status()).toBe(200);
  const body = (await answer.json()) as { tips: unknown[] };
  expect(Array.isArray(body.tips)).toBe(true);
  await page.route(INSIGHTS_ROUTE, (route) => route.fulfill({ json: { tips: [] } }));
  await openTab(page, "Sessions");
  await expect(page.locator("[data-insights]")).toHaveCount(0);
});
