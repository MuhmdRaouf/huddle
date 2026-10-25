// now.spec.ts — the owner's first screen, on the phone and the desktop: the sign-in link lands
// the browser on the hub, and the Now list shows the session the run seeded into the registry
// (with the desktop's main pane keeping its pointer beside it while none is picked), one whose
// last event asks for permission waits in Needs you with its state word, behind the filter too,
// an agent-team teammate that went idle shows under its lead's row with the word idle, a seeded
// permission ask is allowed with one tap on the 48 px button at its card's foot, and the Radar
// card carries today's seeded cost and requests. The phone's page scrolls nothing sideways and
// every target on it is at least 44 px; axe finds nothing serious on either.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, SEEDED, signIn, theSetup, TOTALS } from "./helpers.ts";

/** Today's three seeded glm-5.3 calls at the price table's 1.40 / 4.40 / 0.26 USD per million. */
const TODAY_USD = (TOTALS.input * 1.4 + TOTALS.output * 4.4 + TOTALS.cacheRead * 0.26) / 1e6;

// the authenticator set-up the first sign-in offers is not this page's subject: the browser
// starts with its "Later" already kept
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

test("the sign-in link opens the hub and the Now list shows the seeded session", async ({
  page,
  isMobile,
}) => {
  await signIn(page);
  await openTab(page, "Now");

  // the seeded session sits in the Working group, one row strong
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
  await expect(page.getByText(theSetup().sessionTitle)).toBeVisible();
  // sessions stand and none is picked: the desktop's main pane keeps its pointer at the list
  // (the phone paints the list alone)
  if (!isMobile) await expect(page.getByText("No session open")).toBeVisible();
});

test("a session whose last event asks for permission waits in Needs you first", async ({
  page,
}) => {
  await signIn(page);
  await openTab(page, "Now");

  // a session reports a permission request and nothing after: the registry reads needs-you
  await api("/api/registry/report", {
    session_id: "e2e-permit",
    machine: "e2e-machine",
    project: "e2e",
    title: "Waits on a permission",
    last_event: "PermissionRequest",
    last_event_at: new Date().toISOString(),
  });
  await expect(page.getByRole("heading", { name: "Needs you · 1" })).toBeVisible();
  const card = page.locator("article[data-row='e2e-permit']");
  await expect(card).toBeVisible();
  await expect(card.getByRole("link", { name: /Waits on a permission/ })).toBeVisible();
  // the state rides as a word, not colour alone
  await expect(card.locator("[data-state='needs-you']")).toHaveText("needs you");

  // the filter holds the needs-you list alone, then gives the whole list back
  await page.getByRole("button", { name: "Needs you" }).click();
  await expect(page.getByRole("heading", { name: "Needs you · 1" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeHidden();
  await page.getByRole("button", { name: "Needs you" }).click();
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
});

test("a teammate that went idle shows under its lead's row", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Now");

  // the lead's TeammateIdle hook reports the teammate under the lead's session, then the lead's
  // own turn ends: the teammate stays listed under the idle lead (and the Working count the
  // other cases read stays as it was)
  const lead = { session_id: "e2e-lead", machine: "e2e-machine", project: "e2e", title: "Leads a team" };
  await api("/api/registry/report", {
    ...lead,
    teammate: { name: "researcher", state: "idle" },
    last_event: "TeammateIdle",
    last_event_at: new Date().toISOString(),
  });
  await api("/api/registry/report", { ...lead, last_event: "Stop", last_event_at: new Date().toISOString() });
  const row = page.locator("[data-select-row='e2e-lead'], [data-hold-row='e2e-lead']");
  await expect(row.getByText("Leads a team")).toBeVisible();
  const mate = row.locator("[data-teammate='researcher']");
  await expect(mate).toBeVisible();
  await expect(mate).toContainText("researcher");
  // the teammate's state rides as a word
  await expect(mate.locator("[data-state='idle']")).toHaveText("idle");
});

test("a seeded permission ask is allowed with one tap at its card's foot", async ({ page, isMobile }, info) => {
  // the session stopped, then its hook asked: the open item alone holds it in Needs you, so the
  // decision moves it to Idle and the other cases' counts stay as they were
  const session = `e2e-allow-${info.project.name}`;
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Asks to run the tests",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const { id } = await api<{ id: number }>("/api/needs", {
    session_id: session,
    tool_name: "Bash",
    tool_input: { command: "bun run test" },
  });
  await signIn(page);
  await openTab(page, "Now");

  const card = page.locator(`article[data-row='${session}']`);
  await expect(card.getByText("bun run test")).toBeVisible();
  const allow = card.getByRole("button", { name: "Allow once" });
  const deny = card.getByRole("button", { name: "Deny" });
  // both answers sit side by side in the card's footer, 48 px tall: one thumb reaches either
  for (const button of [allow, deny]) {
    const box = await button.boundingBox();
    expect(box?.height).toBeGreaterThanOrEqual(48);
  }
  const cardBox = await card.boundingBox();
  const allowBox = await allow.boundingBox();
  expect(cardBox && allowBox && cardBox.y + cardBox.height - (allowBox.y + allowBox.height)).toBeLessThanOrEqual(16);

  const decided = page.waitForResponse((r) => r.url().endsWith(`/api/needs/${id}/decide`));
  if (isMobile) await allow.tap();
  else await allow.click();
  const answer = await decided;
  expect(answer.status()).toBe(200);
  expect(((await answer.json()) as { decision?: string }).decision).toBe("allow");
  // the ask left the list: the card is gone and the session reads idle
  await expect(card).toHaveCount(0);
  await expect(page.locator(`[data-row='${session}'] [data-state='idle']`)).toHaveText("idle");
});

test("the Radar card shows today's seeded cost and requests", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Now");
  const radar = page.getByRole("region", { name: "Today in Radar" });
  await expect(radar).toBeVisible();
  const cost = radar.locator("[data-radar-cost]");
  await expect(cost).toHaveText("$0.0073");
  expect(Number(await cost.getAttribute("data-radar-cost"))).toBeCloseTo(TODAY_USD, 9);
  await expect(radar.locator("[data-radar-requests]")).toHaveText(String(SEEDED.requests.length));
});

test("the desktop sets the list and the open pane side by side", async ({ page, isMobile }) => {
  test.skip(isMobile, "the phone paints the list alone");
  await signIn(page);
  await openTab(page, "Now");
  const list = await page.locator("[data-now-pane]").boundingBox();
  const pane = await page.getByText("No session open").boundingBox();
  expect(list && pane && list.x + list.width).toBeLessThanOrEqual(pane?.x ?? 0);
  await expect(page.getByRole("region", { name: "Today in Radar" })).toBeVisible();
});

test("the phone's Now scrolls nothing sideways and every target is at least 44 px", async ({
  page,
  isMobile,
}) => {
  test.skip(!isMobile, "the thumb's targets are the phone's");
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Now");
  await expect(page.getByRole("region", { name: "Today in Radar" })).toBeVisible();
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(over).toBeLessThanOrEqual(0);
  const small = await page.evaluate(() =>
    // the first-run checklist rides above the list on its own component's terms
    [...document.querySelectorAll<HTMLElement>("[data-now-pane] :is(a, button, input, select, textarea)")]
      .filter((el) => !el.closest("[data-first-run]"))
      .filter((el) => el.getClientRects().length > 0)
      .map((el) => ({ el, r: el.getBoundingClientRect() }))
      .filter(({ r }) => r.width < 44 || r.height < 44)
      .map(({ el, r }) => `${el.tagName.toLowerCase()} "${(el.textContent ?? "").trim().slice(0, 30)}" ${Math.round(r.width)}x${Math.round(r.height)}`),
  );
  expect(small).toEqual([]);
});

test("the Now page passes axe", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Now");
  await expect(page.getByRole("region", { name: "Today in Radar" })).toBeVisible();
  await expectAccessible(page);
});
