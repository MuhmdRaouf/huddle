// insights.spec.ts — the Insights card on Sessions: the tips GET /api/insights answers, each one
// sentence with its code and source, and a tip's session as a link. The fixture answer pins what
// the card draws and that it fits a phone; the live answer proves the route serves the owner; an
// answer without a tip leaves the Now list without the card. A tip's Not useful mutes it: the tip
// leaves, the count drops and the route then answers without it. Settings' Start over forgets
// after the confirm and the switches read their defaults.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { openTab, signIn, theSetup } from "./helpers.ts";

const INSIGHTS_ROUTE = "**/api/insights";

/** The mute POST's own pattern: a glob must match the whole URL, so `/api/insights` never
 *  catches `/api/insights/mute`. */
const MUTE_ROUTE = "**/api/insights/mute";

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

/** One same-command tip beside the always-allowed one. */
const SAME_COMMAND = {
  id: "same_command:git",
  rule: "same_command",
  sentence: "You ran `git status` 12 times before a commit this week. A saved ask would skip the typing.",
  count: 12,
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

test("Not useful mutes a tip, the card drops it and the route then answers without it", async ({
  page,
}) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  let tips = [SAME_COMMAND, ...FIXTURE.tips];
  await page.route(INSIGHTS_ROUTE, (route) => route.fulfill({ json: { tips } }));
  await page.route(MUTE_ROUTE, (route) => {
    const { rule } = route.request().postDataJSON() as { rule: string };
    tips = tips.filter((t) => t.rule !== rule);
    route.fulfill({ json: { muted: tips.map((t) => t.rule) } });
  });
  await signIn(page);
  await openTab(page, "Sessions");
  const card = page.locator("[data-insights]");
  await expect(card).toBeVisible();
  await expect(card.locator(".ui-card-meta")).toHaveText("2");
  await card.scrollIntoViewIfNeeded();
  await page.getByRole("button", { name: "Not useful: same command" }).click();
  await expect(card.locator('[data-tip="same_command"]')).toHaveCount(0);
  await expect(card.locator('[data-tip="always_allowed"]')).toHaveCount(1);
  await expect(card.locator(".ui-card-meta")).toHaveText("1");
  await expect(page.locator(".ui-toast", { hasText: "No more tips like this for 30 days." })).toBeVisible();
  const after = (await page.evaluate(async () => await (await fetch("/api/insights")).json())) as {
    tips: { rule: string }[];
  };
  expect(after.tips.map((t) => t.rule)).toEqual(["always_allowed"]);
  await expectAccessible(page);
});

test("Settings' Forget runs after the confirm and the switches read their defaults", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/settings/insights`);
  await expect(page.locator("[data-settings]")).toBeVisible();
  const tipsToSessions = page.getByRole("switch", { name: "Tips to my sessions" });
  // the forget turns the switches back to their defaults, so one starts on for real: the flip is
  // the hub's own, and Forget has to read it back off
  const [put] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === "PUT" &&
        r.url().endsWith("/api/insights/settings") &&
        (r.request().postData() ?? "").includes("tips_to_sessions"),
    ),
    tipsToSessions.click(),
  ]);
  expect(put.status()).toBe(200);
  await expect(tipsToSessions).toHaveAttribute("aria-checked", "true");
  await page.getByRole("button", { name: "Forget what you learned" }).click();
  await expect(
    page.getByText("Huddle forgets the muted tips and turns both switches back to their defaults."),
  ).toBeVisible();
  const [forget] = await Promise.all([
    page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/api/insights/forget")),
    page.getByRole("button", { name: "Forget", exact: true }).click(),
  ]);
  expect(forget.status()).toBe(200);
  await expect(page.locator(".ui-toast", { hasText: "Insights forgot what it learned." })).toBeVisible();
  await expect(page.getByRole("button", { name: "Forget what you learned" })).toBeVisible();
  await expect(tipsToSessions).toHaveAttribute("aria-checked", "false");
  await expect(page.getByRole("switch", { name: "Tips for me", exact: true })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await expect(page.getByText("Removes insights.json on this machine.")).toBeVisible();
  await expectAccessible(page);
});
